package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"

	"github.com/OnslaughtSnail/caelis/acp/jsonrpc"
)

// ServeStdio serves one agent-side ACP connection over NDJSON stdio.
func ServeStdio(ctx context.Context, agent Agent, in io.Reader, out io.Writer) error {
	if agent == nil {
		return errors.New("acp: agent is required")
	}
	if in == nil || out == nil {
		return errors.New("acp: stdio streams are required")
	}
	conn := &serverConn{
		agent: agent,
		rpc:   jsonrpc.New(in, out),
	}
	if err := conn.rpc.Serve(ctx, conn.handleRequest, conn.handleNotification); err != nil {
		if err == nil || errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

type serverConn struct {
	agent          Agent
	rpc            *jsonrpc.Conn
	clientTerminal atomic.Bool
}

func (c *serverConn) handleRequest(ctx context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
	switch msg.Method {
	case MethodInitialize:
		var req InitializeRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		c.clientTerminal.Store(clientCapabilityBool(req.ClientCapabilities, "terminal"))
		resp, err := c.agent.Initialize(ctx, req)
		return responseOrError(resp, err)
	case MethodAuthenticate:
		var req AuthenticateRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		resp, err := c.agent.Authenticate(ctx, req)
		return responseOrError(resp, err)
	case MethodSessionNew:
		var req NewSessionRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		resp, err := c.agent.NewSession(ctx, req)
		if err != nil {
			return responseOrError(resp, err)
		}
		return c.withAvailableCommands(ctx, resp, resp.SessionID), nil
	case MethodSessionList:
		var req SessionListRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := AsSessionListAdapter(c.agent)
		if !ok {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
		}
		resp, err := handler.ListSessions(ctx, req)
		return responseOrError(resp, err)
	case MethodSessionLoad:
		var req LoadSessionRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := AsLoadSessionAdapter(c.agent)
		if !ok {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
		}
		resp, err := handler.LoadSession(ctx, req, c)
		if err != nil {
			return responseOrError(resp, err)
		}
		return c.withAvailableCommands(ctx, resp, req.SessionID), nil
	case MethodSessionResume:
		var req ResumeSessionRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := AsResumeSessionAdapter(c.agent)
		if !ok {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
		}
		resp, err := handler.ResumeSession(ctx, req)
		if err != nil {
			return responseOrError(resp, err)
		}
		return c.withAvailableCommands(ctx, resp, req.SessionID), nil
	case MethodSessionClose:
		var req CloseSessionRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := AsCloseSessionAdapter(c.agent)
		if !ok {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
		}
		resp, err := handler.CloseSession(ctx, req)
		return responseOrError(resp, err)
	case MethodSessionSetMode:
		var req SetSessionModeRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := AsSessionModeAdapter(c.agent)
		if !ok {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
		}
		resp, err := handler.SetSessionMode(ctx, req)
		return responseOrError(resp, err)
	case MethodSessionSetConfig:
		var req SetSessionConfigOptionRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := AsSessionConfigAdapter(c.agent)
		if !ok {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
		}
		resp, err := handler.SetSessionConfigOption(ctx, req)
		return responseOrError(resp, err)
	case MethodSessionSetModel:
		var req SetSessionModelRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := AsSessionModelAdapter(c.agent)
		if !ok {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
		}
		resp, err := handler.SetSessionModel(ctx, req)
		return responseOrError(resp, err)
	case MethodSessionPrompt:
		var req PromptRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		resp, err := c.agent.Prompt(ctx, req, c)
		return responseOrError(resp, err)
	case MethodTerminalOutput:
		var req TerminalOutputRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := AsTerminalAdapter(c.agent)
		if !ok {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
		}
		resp, err := handler.Output(ctx, req)
		return responseOrError(resp, err)
	case MethodTerminalWaitForExit:
		var req TerminalWaitForExitRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := AsTerminalAdapter(c.agent)
		if !ok {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
		}
		resp, err := handler.WaitForExit(ctx, req)
		return responseOrError(resp, err)
	case MethodTerminalKill:
		var req TerminalKillRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := AsTerminalAdapter(c.agent)
		if !ok {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
		}
		err := handler.Kill(ctx, req)
		return responseOrError(struct{}{}, err)
	case MethodTerminalRelease:
		var req TerminalReleaseRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, invalidParams(err)
		}
		handler, ok := AsTerminalAdapter(c.agent)
		if !ok {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
		}
		err := handler.Release(ctx, req)
		return responseOrError(struct{}{}, err)
	default:
		return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
	}
}

func (c *serverConn) handleNotification(ctx context.Context, msg jsonrpc.Message) {
	if msg.Method == MethodSessionCancel {
		var req CancelNotification
		if err := decodeParams(msg.Params, &req); err == nil {
			_ = c.agent.Cancel(ctx, req)
		}
	}
}

func (c *serverConn) SessionUpdate(_ context.Context, notification SessionNotification) error {
	return c.rpc.Notify(MethodSessionUpdate, notification)
}

func (c *serverConn) withAvailableCommands(ctx context.Context, payload any, sessionID string) any {
	handler, ok := AsSessionCommandAdapter(c.agent)
	sessionID = strings.TrimSpace(sessionID)
	if !ok || sessionID == "" {
		return payload
	}
	return jsonrpc.PostWriteResult{
		Payload: payload,
		AfterWrite: func() {
			cmds, err := handler.AvailableCommands(context.WithoutCancel(ctx), sessionID)
			if err != nil || len(cmds) == 0 {
				return
			}
			_ = c.SessionUpdate(context.WithoutCancel(ctx), SessionNotification{
				SessionID: sessionID,
				Update: AvailableCommandsUpdate{
					SessionUpdate:     UpdateAvailableCmds,
					AvailableCommands: cmds,
				},
			})
		},
	}
}

func (c *serverConn) RequestPermission(ctx context.Context, req RequestPermissionRequest) (RequestPermissionResponse, error) {
	var resp RequestPermissionResponse
	if err := c.rpc.Call(ctx, MethodSessionReqPermission, req, &resp); err != nil {
		return RequestPermissionResponse{}, err
	}
	return resp, nil
}

func (c *serverConn) CreateTerminal(ctx context.Context, req CreateTerminalRequest) (CreateTerminalResponse, error) {
	if !c.clientTerminal.Load() {
		return CreateTerminalResponse{}, ErrCapabilityUnsupported
	}
	var resp CreateTerminalResponse
	if err := c.rpc.Call(ctx, MethodTerminalCreate, req, &resp); err != nil {
		return CreateTerminalResponse{}, err
	}
	return resp, nil
}

func (c *serverConn) TerminalOutput(ctx context.Context, req TerminalOutputRequest) (TerminalOutputResponse, error) {
	if !c.clientTerminal.Load() {
		return TerminalOutputResponse{}, ErrCapabilityUnsupported
	}
	var resp TerminalOutputResponse
	if err := c.rpc.Call(ctx, MethodTerminalOutput, req, &resp); err != nil {
		return TerminalOutputResponse{}, err
	}
	return resp, nil
}

func (c *serverConn) TerminalWaitForExit(ctx context.Context, req TerminalWaitForExitRequest) (TerminalWaitForExitResponse, error) {
	if !c.clientTerminal.Load() {
		return TerminalWaitForExitResponse{}, ErrCapabilityUnsupported
	}
	var resp TerminalWaitForExitResponse
	if err := c.rpc.Call(ctx, MethodTerminalWaitForExit, req, &resp); err != nil {
		return TerminalWaitForExitResponse{}, err
	}
	return resp, nil
}

func (c *serverConn) TerminalKill(ctx context.Context, req TerminalKillRequest) error {
	if !c.clientTerminal.Load() {
		return ErrCapabilityUnsupported
	}
	return c.rpc.Call(ctx, MethodTerminalKill, req, nil)
}

func (c *serverConn) TerminalRelease(ctx context.Context, req TerminalReleaseRequest) error {
	if !c.clientTerminal.Load() {
		return ErrCapabilityUnsupported
	}
	return c.rpc.Call(ctx, MethodTerminalRelease, req, nil)
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

func responseOrError(result any, err error) (any, *jsonrpc.RPCError) {
	if err == nil {
		return result, nil
	}
	if errors.Is(err, ErrCapabilityUnsupported) {
		return nil, &jsonrpc.RPCError{Code: -32601, Message: "Method not found"}
	}
	return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
}

func invalidParams(err error) *jsonrpc.RPCError {
	return &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
}

var _ PromptCallbacks = (*serverConn)(nil)
var _ TerminalClientCallbacks = (*serverConn)(nil)
var _ = fmt.Sprintf
