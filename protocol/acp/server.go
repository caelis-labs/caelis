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
func ServeStdio(ctx context.Context, agent Agent, in io.Reader, out io.Writer) error {
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
	agent          Agent
	rpc            atomic.Pointer[acpsdk.Connection]
	rpcReady       chan struct{}
	lifecycle      context.Context
	clientTerminal atomic.Bool
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
	case MethodInitialize:
		var req InitializeRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		c.clientTerminal.Store(clientCapabilityBool(req.ClientCapabilities, "terminal"))
		resp, err := c.agent.Initialize(ctx, req)
		return responseOrError(resp, err)
	case MethodAuthenticate:
		var req AuthenticateRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		resp, err := c.agent.Authenticate(ctx, req)
		return responseOrError(resp, err)
	case MethodSessionNew:
		var req NewSessionRequest
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
	case MethodSessionList:
		var req SessionListRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(SessionListAdapter)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.ListSessions(ctx, req)
		return responseOrError(resp, err)
	case MethodSessionLoad:
		var req LoadSessionRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(SessionLoader)
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
	case MethodSessionResume:
		var req ResumeSessionRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(ResumeSessionAdapter)
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
	case MethodSessionClose:
		var req CloseSessionRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(CloseSessionAdapter)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.CloseSession(ctx, req)
		return responseOrError(resp, err)
	case MethodSessionSetMode:
		var req SetSessionModeRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(SessionModeAdapter)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.SetSessionMode(ctx, req)
		return responseOrError(resp, err)
	case MethodSessionSetConfig:
		var req SetSessionConfigOptionRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(SessionConfigAdapter)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.SetSessionConfigOption(ctx, req)
		return responseOrError(resp, err)
	case MethodSessionSetModel:
		var req SetSessionModelRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(SessionModelAdapter)
		if !ok {
			return nil, methodNotFound()
		}
		resp, err := handler.SetSessionModel(ctx, req)
		return responseOrError(resp, err)
	case MethodSessionPrompt:
		var req PromptRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		resp, err := c.agent.Prompt(ctx, req, c.promptCallbacks())
		return responseOrError(resp, err)
	case MethodSessionSteering:
		var req SessionSteeringRequest
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		if err := validateSessionSteeringRequest(req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := c.agent.(SessionSteeringAdapter)
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
	case MethodSessionCancel:
		var req CancelNotification
		if err := decodeParams(params, &req); err != nil {
			return nil, invalidParams(err)
		}
		return responseOrError(struct{}{}, c.agent.Cancel(ctx, req))
	default:
		return nil, methodNotFound()
	}
}

func (c *serverConn) SessionUpdate(_ context.Context, notification SessionNotification) error {
	rpc, err := c.connection(c.lifecycle)
	if err != nil {
		return err
	}
	return rpc.SendNotification(c.lifecycle, MethodSessionUpdate, notification)
}

func (c *serverConn) afterAvailableCommands(inbound *serverInboundRequest, sessionID string, delay time.Duration) error {
	handler, ok := c.agent.(CommandProvider)
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

func (c *serverConn) emitAvailableCommands(ctx context.Context, handler CommandProvider, sessionID string) {
	cmds, err := handler.AvailableCommands(ctx, sessionID)
	if err != nil || len(cmds) == 0 {
		return
	}
	_ = c.SessionUpdate(ctx, SessionNotification{
		SessionID: sessionID,
		Update: AvailableCommandsUpdate{
			SessionUpdate:     UpdateAvailableCmds,
			AvailableCommands: cmds,
		},
	})
}

func (c *serverConn) RequestPermission(ctx context.Context, req RequestPermissionRequest) (RequestPermissionResponse, error) {
	rpc, err := c.connection(ctx)
	if err != nil {
		return RequestPermissionResponse{}, err
	}
	return acpsdk.SendRequest[RequestPermissionResponse](rpc, ctx, MethodSessionReqPermission, req)
}

type serverPromptCallbacks struct {
	conn *serverConn
}

func (c serverPromptCallbacks) SessionUpdate(ctx context.Context, req SessionNotification) error {
	return c.conn.SessionUpdate(ctx, req)
}

func (c serverPromptCallbacks) RequestPermission(ctx context.Context, req RequestPermissionRequest) (RequestPermissionResponse, error) {
	return c.conn.RequestPermission(ctx, req)
}

func (c serverPromptCallbacks) CreateTerminal(ctx context.Context, req CreateTerminalRequest) (CreateTerminalResponse, error) {
	return c.conn.CreateTerminal(ctx, req)
}

func (c serverPromptCallbacks) TerminalOutput(ctx context.Context, req TerminalOutputRequest) (TerminalOutputResponse, error) {
	return c.conn.TerminalOutput(ctx, req)
}

func (c serverPromptCallbacks) TerminalWaitForExit(ctx context.Context, req TerminalWaitForExitRequest) (TerminalWaitForExitResponse, error) {
	return c.conn.TerminalWaitForExit(ctx, req)
}

func (c serverPromptCallbacks) TerminalKill(ctx context.Context, req TerminalKillRequest) error {
	return c.conn.TerminalKill(ctx, req)
}

func (c serverPromptCallbacks) TerminalRelease(ctx context.Context, req TerminalReleaseRequest) error {
	return c.conn.TerminalRelease(ctx, req)
}

func (c *serverConn) promptCallbacks() PromptCallbacks {
	return serverPromptCallbacks{conn: c}
}

func (c *serverConn) CreateTerminal(ctx context.Context, req CreateTerminalRequest) (CreateTerminalResponse, error) {
	if !c.clientTerminal.Load() {
		return CreateTerminalResponse{}, ErrCapabilityUnsupported
	}
	rpc, err := c.connection(ctx)
	if err != nil {
		return CreateTerminalResponse{}, err
	}
	return acpsdk.SendRequest[CreateTerminalResponse](rpc, ctx, MethodTerminalCreate, req)
}

func (c *serverConn) TerminalOutput(ctx context.Context, req TerminalOutputRequest) (TerminalOutputResponse, error) {
	if !c.clientTerminal.Load() {
		return TerminalOutputResponse{}, ErrCapabilityUnsupported
	}
	rpc, err := c.connection(ctx)
	if err != nil {
		return TerminalOutputResponse{}, err
	}
	return acpsdk.SendRequest[TerminalOutputResponse](rpc, ctx, MethodTerminalOutput, req)
}

func (c *serverConn) TerminalWaitForExit(ctx context.Context, req TerminalWaitForExitRequest) (TerminalWaitForExitResponse, error) {
	if !c.clientTerminal.Load() {
		return TerminalWaitForExitResponse{}, ErrCapabilityUnsupported
	}
	rpc, err := c.connection(ctx)
	if err != nil {
		return TerminalWaitForExitResponse{}, err
	}
	return acpsdk.SendRequest[TerminalWaitForExitResponse](rpc, ctx, MethodTerminalWaitForExit, req)
}

func (c *serverConn) TerminalKill(ctx context.Context, req TerminalKillRequest) error {
	if !c.clientTerminal.Load() {
		return ErrCapabilityUnsupported
	}
	rpc, err := c.connection(ctx)
	if err != nil {
		return err
	}
	return rpc.SendRequestNoResult(ctx, MethodTerminalKill, req)
}

func (c *serverConn) TerminalRelease(ctx context.Context, req TerminalReleaseRequest) error {
	if !c.clientTerminal.Load() {
		return ErrCapabilityUnsupported
	}
	rpc, err := c.connection(ctx)
	if err != nil {
		return err
	}
	return rpc.SendRequestNoResult(ctx, MethodTerminalRelease, req)
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

func clientCapabilityBool(caps map[string]any, key string) bool {
	value, ok := caps[key]
	if !ok {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case *bool:
		return typed != nil && *typed
	default:
		return false
	}
}

func decodeParams(raw json.RawMessage, target any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, target)
}

func validateSessionSteeringRequest(req SessionSteeringRequest) error {
	if strings.TrimSpace(req.SessionID) == "" {
		return fmt.Errorf("sessionId is required")
	}
	if len(req.Prompt) == 0 {
		return fmt.Errorf("prompt must contain at least one content block")
	}
	_, err := DecodeSessionSteeringOptions(req.Meta)
	return err
}

func responseOrError(result any, err error) (any, *acpsdk.RequestError) {
	if err == nil {
		return result, nil
	}
	if errors.Is(err, ErrCapabilityUnsupported) {
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

var _ PromptCallbacks = serverPromptCallbacks{}
var _ TerminalClientCallbacks = serverPromptCallbacks{}
var _ TerminalClientCallbacks = (*serverConn)(nil)
