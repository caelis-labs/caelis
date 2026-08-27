package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	protocolacp "github.com/caelis-labs/caelis/protocol/acp"
)

// serverMaxFrameSize accommodates the product's 32 MB decoded aggregate image
// limit after base64 expansion and JSON envelope overhead while keeping the
// formerly unbounded stdio reader finite.
const serverMaxFrameSize = 64 * 1024 * 1024

// New-session clients may bind their UI/session routing only after session/new
// resolves. Preserve the established compatibility window for ordinary ACP
// clients that do not provide a response hook.
const availableCommandsAfterSessionNewDelay = 100 * time.Millisecond

// ServeStdio serves one agent-side ACP connection over NDJSON stdio.
func ServeStdio(ctx context.Context, agent protocolacp.Agent, in io.Reader, out io.Writer) error {
	if agent == nil {
		return errors.New("acp: agent is required")
	}
	if in == nil || out == nil {
		return errors.New("acp: stdio streams are required")
	}
	conn := &serverConn{
		agent:     agent,
		lifecycle: ctx,
		rpcReady:  make(chan struct{}),
	}
	connectionInput, closeInputOnFailure, err := sdkOwnedServerInput(in)
	if err != nil {
		return err
	}
	connectionOutput, closeOutputOnFailure, err := sdkOwnedServerOutput(out)
	if err != nil {
		closeInputOnFailure()
		return err
	}
	rpc, err := acpsdk.NewConnectionWithOptions(conn.handle, connectionOutput, connectionInput, acpsdk.ConnectionOptions{
		MaxFrameSize: serverMaxFrameSize,
	})
	if err != nil {
		closeInputOnFailure()
		closeOutputOnFailure()
		return err
	}
	conn.rpc.Store(rpc)
	close(conn.rpcReady)
	defer rpc.Close()
	if err := rpc.Wait(ctx); err != nil {
		if ctx.Err() != nil {
			return context.Cause(ctx)
		}
		if errors.Is(err, acpsdk.ErrPeerClosed) || errors.Is(err, acpsdk.ErrConnectionClosed) {
			return nil
		}
		return err
	}
	return nil
}

type serverConn struct {
	agent     protocolacp.Agent
	rpc       atomic.Pointer[acpsdk.Connection]
	rpcReady  chan struct{}
	lifecycle context.Context
}

type serverInboundRequest struct {
	mu       sync.Mutex
	callback func(context.Context) error
}

func (r *serverInboundRequest) runAfterResponse(ctx context.Context) error {
	r.mu.Lock()
	callback := r.callback
	r.mu.Unlock()
	if callback == nil {
		return nil
	}
	return callback(ctx)
}

func (r *serverInboundRequest) setAfterResponse(callback func(context.Context) error) error {
	if r == nil {
		return acpsdk.ErrAfterResponseUnavailable
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.callback != nil {
		return acpsdk.ErrAfterResponseRegistered
	}
	r.callback = callback
	return nil
}

// handle restores the ACP request/notification split that the SDK's shared
// MethodHandler intentionally leaves to product wire adapters. Registering the
// one response hook also provides the SDK's public request-context signal.
func (c *serverConn) handle(ctx context.Context, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
	inbound := &serverInboundRequest{}
	err := acpsdk.AfterResponse(ctx, inbound.runAfterResponse)
	switch {
	case err == nil:
		return c.handleRequest(ctx, inbound, method, params)
	case errors.Is(err, acpsdk.ErrAfterResponseUnavailable):
		return c.handleNotification(ctx, method, params)
	default:
		return nil, responseError(err)
	}
}

func (c *serverConn) handleRequest(ctx context.Context, inbound *serverInboundRequest, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
	switch method {
	case protocolacp.MethodInitialize:
		var req protocolacp.InitializeRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		resp, err := c.agent.Initialize(ctx, req)
		return responseOrError(resp, err)
	case protocolacp.MethodAuthenticate:
		var req protocolacp.AuthenticateRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		resp, err := c.agent.Authenticate(ctx, req)
		return responseOrError(resp, err)
	case protocolacp.MethodSessionNew:
		var req protocolacp.NewSessionRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		resp, err := c.agent.NewSession(ctx, req)
		if err != nil {
			return responseOrError(resp, err)
		}
		if err := c.afterAvailableCommands(inbound, resp.SessionID, availableCommandsAfterSessionNewDelay); err != nil {
			return nil, responseError(err)
		}
		return resp, nil
	case protocolacp.MethodSessionList:
		var req protocolacp.SessionListRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(protocolacp.SessionListAdapter)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.ListSessions(ctx, req)
		return responseOrError(resp, err)
	case protocolacp.MethodSessionLoad:
		var req protocolacp.LoadSessionRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(protocolacp.SessionLoader)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.LoadSession(ctx, req, c)
		if err != nil {
			return responseOrError(resp, err)
		}
		if err := c.afterAvailableCommands(inbound, req.SessionID, 0); err != nil {
			return nil, responseError(err)
		}
		return resp, nil
	case protocolacp.MethodSessionResume:
		var req protocolacp.ResumeSessionRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(protocolacp.ResumeSessionAdapter)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.ResumeSession(ctx, req)
		if err != nil {
			return responseOrError(resp, err)
		}
		if err := c.afterAvailableCommands(inbound, req.SessionID, 0); err != nil {
			return nil, responseError(err)
		}
		return resp, nil
	case protocolacp.MethodSessionClose:
		var req protocolacp.CloseSessionRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(protocolacp.CloseSessionAdapter)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.CloseSession(ctx, req)
		return responseOrError(resp, err)
	case protocolacp.MethodSessionSetMode:
		var req protocolacp.SetSessionModeRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(protocolacp.SessionModeAdapter)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.SetSessionMode(ctx, req)
		return responseOrError(resp, err)
	case protocolacp.MethodSessionSetConfig:
		var req protocolacp.SetSessionConfigOptionRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(protocolacp.SessionConfigAdapter)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.SetSessionConfigOption(ctx, req)
		return responseOrError(resp, err)
	case protocolacp.MethodSessionSetModel:
		var req protocolacp.SetSessionModelRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(protocolacp.SessionModelAdapter)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.SetSessionModel(ctx, req)
		return responseOrError(resp, err)
	case protocolacp.MethodSessionPrompt:
		var req protocolacp.PromptRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		resp, err := c.agent.Prompt(ctx, req, c.promptCallbacks())
		return responseOrError(resp, err)
	case protocolacp.MethodSessionSteering:
		var req protocolacp.SessionSteeringRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		if err := validateSessionSteeringRequest(req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(protocolacp.SessionSteeringAdapter)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.SteerSession(ctx, req)
		return responseOrError(resp, err)
	default:
		return nil, methodNotFound()
	}
}

func (c *serverConn) handleNotification(ctx context.Context, method string, params json.RawMessage) (any, *acpsdk.RequestError) {
	switch method {
	case protocolacp.MethodSessionCancel:
		var req protocolacp.CancelNotification
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		return responseOrError(struct{}{}, c.agent.Cancel(ctx, req))
	default:
		return nil, methodNotFound()
	}
}

func (c *serverConn) SessionUpdate(_ context.Context, notification protocolacp.SessionNotification) error {
	rpc, err := c.connection(c.lifecycle)
	if err != nil {
		return err
	}
	return rpc.SendNotification(c.lifecycle, protocolacp.MethodSessionUpdate, notification)
}

func (c *serverConn) afterAvailableCommands(inbound *serverInboundRequest, sessionID string, delay time.Duration) error {
	handler, ok := c.agent.(protocolacp.CommandProvider)
	sessionID = strings.TrimSpace(sessionID)
	if !ok || sessionID == "" {
		return nil
	}
	return inbound.setAfterResponse(func(callbackCtx context.Context) error {
		emit := func() {
			c.emitAvailableCommands(callbackCtx, handler, sessionID)
		}
		if delay <= 0 {
			emit()
			return nil
		}
		go func() {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-callbackCtx.Done():
			case <-timer.C:
				emit()
			}
		}()
		return nil
	})
}

func (c *serverConn) emitAvailableCommands(ctx context.Context, handler protocolacp.CommandProvider, sessionID string) {
	cmds, err := handler.AvailableCommands(ctx, sessionID)
	if err != nil || len(cmds) == 0 {
		return
	}
	_ = c.SessionUpdate(ctx, protocolacp.SessionNotification{
		SessionID: sessionID,
		Update: protocolacp.AvailableCommandsUpdate{
			SessionUpdate:     protocolacp.UpdateAvailableCmds,
			AvailableCommands: cmds,
		},
	})
}

func (c *serverConn) RequestPermission(ctx context.Context, req protocolacp.RequestPermissionRequest) (protocolacp.RequestPermissionResponse, error) {
	rpc, err := c.connection(ctx)
	if err != nil {
		return protocolacp.RequestPermissionResponse{}, err
	}
	return acpsdk.SendRequest[protocolacp.RequestPermissionResponse](rpc, ctx, protocolacp.MethodSessionReqPermission, req)
}

type serverPromptCallbacks struct {
	conn *serverConn
}

func (c serverPromptCallbacks) SessionUpdate(ctx context.Context, req protocolacp.SessionNotification) error {
	return c.conn.SessionUpdate(ctx, req)
}

func (c serverPromptCallbacks) RequestPermission(ctx context.Context, req protocolacp.RequestPermissionRequest) (protocolacp.RequestPermissionResponse, error) {
	return c.conn.RequestPermission(ctx, req)
}

func (c *serverConn) promptCallbacks() protocolacp.PromptCallbacks {
	return serverPromptCallbacks{conn: c}
}

func (c *serverConn) connection(ctx context.Context) (*acpsdk.Connection, error) {
	if rpc := c.rpc.Load(); rpc != nil {
		return rpc, nil
	}
	if c.rpcReady == nil {
		return nil, errors.New("acp: server connection is not ready")
	}
	select {
	case <-c.rpcReady:
		rpc := c.rpc.Load()
		if rpc == nil {
			return nil, errors.New("acp: server connection is not ready")
		}
		return rpc, nil
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
}

func decodeParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, target)
}

func validateSessionSteeringRequest(req protocolacp.SessionSteeringRequest) error {
	if strings.TrimSpace(req.SessionID) == "" {
		return fmt.Errorf("sessionId is required")
	}
	if len(req.Prompt) == 0 {
		return fmt.Errorf("prompt must contain at least one content block")
	}
	_, err := protocolacp.DecodeSessionSteeringOptions(req.Meta)
	return err
}

func responseOrError(result any, err error) (any, *acpsdk.RequestError) {
	if err == nil {
		return result, nil
	}
	if errors.Is(err, protocolacp.ErrCapabilityUnsupported) {
		return nil, methodNotFound()
	}
	return nil, responseError(err)
}

func responseError(err error) *acpsdk.RequestError {
	var requestErr *acpsdk.RequestError
	if errors.As(err, &requestErr) && requestErr != nil {
		return requestErr
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return acpsdk.NewRequestCancelled(map[string]any{"error": err.Error()})
	}
	return acpsdk.NewInternalError(map[string]any{"error": err.Error()})
}

func invalidParams(err error) *acpsdk.RequestError {
	return &acpsdk.RequestError{Code: -32602, Message: err.Error()}
}

func methodNotFound() *acpsdk.RequestError {
	return &acpsdk.RequestError{Code: -32601, Message: "Method not found"}
}

var _ protocolacp.PromptCallbacks = serverPromptCallbacks{}
