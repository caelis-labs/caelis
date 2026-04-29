package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/acp/jsonrpc"
	"github.com/OnslaughtSnail/caelis/acp/transport/stdio"
)

type RequestHandler func(context.Context, jsonrpc.Message) (any, *jsonrpc.RPCError)
type NotificationHandler func(context.Context, jsonrpc.Message)
type PermissionHandler func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error)

type Config struct {
	Command             string
	Args                []string
	Env                 map[string]string
	WorkDir             string
	ClientInfo          *Implementation
	OnUpdate            func(UpdateEnvelope)
	OnPermissionRequest PermissionHandler
	OnRequest           RequestHandler
	OnNotification      NotificationHandler
}

type Client struct {
	conn *jsonrpc.Conn
	proc *stdio.Process
	cfg  Config

	cancel context.CancelFunc
	done   chan error

	stderrMu  sync.Mutex
	stderrBuf bytes.Buffer
}

func Start(ctx context.Context, cfg Config) (*Client, error) {
	proc, err := stdio.Start(ctx, stdio.Config{
		Command: cfg.Command,
		Args:    append([]string(nil), cfg.Args...),
		Env:     cfg.Env,
		WorkDir: cfg.WorkDir,
	})
	if err != nil {
		return nil, err
	}
	return NewProcessClient(ctx, proc, cfg), nil
}

func NewProcessClient(ctx context.Context, proc *stdio.Process, cfg Config) *Client {
	serveCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	var stdout io.Reader
	var stdin io.Writer
	if proc != nil {
		stdout = proc.Stdout
		stdin = proc.Stdin
	}
	conn := jsonrpc.New(stdout, stdin)
	client := &Client{
		conn:   conn,
		proc:   proc,
		cfg:    cfg,
		cancel: cancel,
		done:   make(chan error, 1),
	}
	go func() {
		client.done <- conn.Serve(serveCtx, client.handleRequest, client.handleNotification)
	}()
	if proc != nil && proc.Stderr != nil {
		go func() {
			_, _ = io.Copy(stderrBufferWriter{client: client}, proc.Stderr)
		}()
	}
	return client
}

func (c *Client) Initialize(ctx context.Context) (InitializeResponse, error) {
	var resp InitializeResponse
	err := c.conn.Call(ctx, MethodInitialize, InitializeRequest{
		ProtocolVersion: 1,
		ClientCapabilities: map[string]any{
			"fs":       map[string]any{"readTextFile": true, "writeTextFile": true},
			"terminal": true,
		},
		ClientInfo: c.cfg.ClientInfo,
	}, &resp)
	return resp, err
}

func (c *Client) NewSession(ctx context.Context, cwd string, meta map[string]any) (NewSessionResponse, error) {
	var resp NewSessionResponse
	err := c.conn.Call(ctx, MethodSessionNew, NewSessionRequest{
		CWD:        cwd,
		MCPServers: []json.RawMessage{},
	}, &resp)
	_ = meta
	return resp, err
}

func (c *Client) LoadSession(ctx context.Context, sessionID string, cwd string, meta map[string]any) (LoadSessionResponse, error) {
	var resp LoadSessionResponse
	err := c.conn.Call(ctx, MethodSessionLoad, LoadSessionRequest{
		SessionID:  sessionID,
		CWD:        cwd,
		MCPServers: []json.RawMessage{},
	}, &resp)
	_ = meta
	return resp, err
}

func (c *Client) SetMode(ctx context.Context, sessionID string, modeID string) error {
	return c.conn.Call(ctx, MethodSessionSetMode, SetSessionModeRequest{
		SessionID: sessionID,
		ModeID:    modeID,
	}, &SetSessionModeResponse{})
}

func (c *Client) SetConfigOption(ctx context.Context, sessionID string, configID string, value any) (SetSessionConfigOptionResponse, error) {
	var resp SetSessionConfigOptionResponse
	err := c.conn.Call(ctx, MethodSessionSetConfig, SetSessionConfigOptionRequest{
		SessionID: sessionID,
		ConfigID:  configID,
		Value:     value,
	}, &resp)
	return resp, err
}

func (c *Client) Prompt(ctx context.Context, sessionID string, text string, meta map[string]any) (PromptResponse, error) {
	return c.PromptParts(ctx, sessionID, []json.RawMessage{
		jsonrpc.MustMarshalRaw(TextContent{Type: "text", Text: text}),
	}, meta)
}

func (c *Client) PromptParts(ctx context.Context, sessionID string, prompt []json.RawMessage, meta map[string]any) (PromptResponse, error) {
	var resp PromptResponse
	err := c.conn.Call(ctx, MethodSessionPrompt, PromptRequest{
		SessionID: sessionID,
		Prompt:    append([]json.RawMessage(nil), prompt...),
	}, &resp)
	_ = meta
	return resp, err
}

func (c *Client) Cancel(ctx context.Context, sessionID string) error {
	return c.conn.Call(ctx, MethodSessionCancel, CancelRequest{SessionID: sessionID}, &CancelResponse{})
}

func (c *Client) TerminalOutput(ctx context.Context, sessionID, terminalID string) (TerminalOutputResponse, error) {
	var resp TerminalOutputResponse
	err := c.conn.Call(ctx, MethodTerminalOutput, TerminalOutputRequest{
		SessionID:  strings.TrimSpace(sessionID),
		TerminalID: strings.TrimSpace(terminalID),
	}, &resp)
	return resp, err
}

func (c *Client) TerminalWaitForExit(ctx context.Context, sessionID, terminalID string) (WaitForTerminalExitResponse, error) {
	var resp WaitForTerminalExitResponse
	err := c.conn.Call(ctx, MethodTerminalWaitForExit, WaitForTerminalExitRequest{
		SessionID:  strings.TrimSpace(sessionID),
		TerminalID: strings.TrimSpace(terminalID),
	}, &resp)
	return resp, err
}

func (c *Client) TerminalKill(ctx context.Context, sessionID, terminalID string) error {
	return c.conn.Call(ctx, MethodTerminalKill, KillTerminalRequest{
		SessionID:  strings.TrimSpace(sessionID),
		TerminalID: strings.TrimSpace(terminalID),
	}, nil)
}

func (c *Client) TerminalRelease(ctx context.Context, sessionID, terminalID string) error {
	return c.conn.Call(ctx, MethodTerminalRelease, ReleaseTerminalRequest{
		SessionID:  strings.TrimSpace(sessionID),
		TerminalID: strings.TrimSpace(terminalID),
	}, nil)
}

func (c *Client) Close(ctx context.Context) error {
	if c == nil {
		return nil
	}
	if c.cancel != nil {
		c.cancel()
	}
	select {
	case <-time.After(100 * time.Millisecond):
	case <-c.done:
	}
	if c.proc != nil {
		return c.proc.Close(ctx)
	}
	return nil
}

func (c *Client) StderrTail(limit int) string {
	if c == nil || limit <= 0 {
		return ""
	}
	c.stderrMu.Lock()
	defer c.stderrMu.Unlock()
	data := c.stderrBuf.Bytes()
	if len(data) == 0 {
		return ""
	}
	if len(data) > limit {
		data = data[len(data)-limit:]
	}
	return strings.TrimSpace(string(data))
}

func (c *Client) handleRequest(ctx context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
	switch msg.Method {
	case MethodSessionReqPermission:
		var req RequestPermissionRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
		}
		if c.cfg.OnPermissionRequest != nil {
			resp, err := c.cfg.OnPermissionRequest(ctx, req)
			if err != nil {
				return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
			}
			return resp, nil
		}
		return PermissionSelectedOutcome("reject_once"), nil
	default:
		if c.cfg.OnRequest != nil {
			return c.cfg.OnRequest(ctx, msg)
		}
		return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
	}
}

func (c *Client) handleNotification(ctx context.Context, msg jsonrpc.Message) {
	if c == nil {
		return
	}
	if msg.Method == MethodSessionUpdate && c.cfg.OnUpdate != nil {
		var note SessionNotification
		if err := decodeParams(msg.Params, &note); err == nil {
			if update, err := decodeUpdate(note.Update); err == nil && update != nil {
				c.cfg.OnUpdate(UpdateEnvelope{
					SessionID: strings.TrimSpace(note.SessionID),
					Update:    update,
				})
			}
		}
	}
	if c.cfg.OnNotification != nil {
		c.cfg.OnNotification(ctx, msg)
	}
}

func decodeParams(raw json.RawMessage, out any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func decodeUpdate(raw json.RawMessage) (Update, error) {
	var probe struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, err
	}
	switch probe.SessionUpdate {
	case UpdateUserMessage, UpdateAgentMessage, UpdateAgentThought:
		var chunk ContentChunk
		if err := json.Unmarshal(raw, &chunk); err != nil {
			return nil, err
		}
		return chunk, nil
	case UpdateToolCall:
		var call ToolCall
		if err := json.Unmarshal(raw, &call); err != nil {
			return nil, err
		}
		return call, nil
	case UpdateToolCallState:
		var call ToolCallUpdate
		if err := json.Unmarshal(raw, &call); err != nil {
			return nil, err
		}
		return call, nil
	case UpdatePlan:
		var plan PlanUpdate
		if err := json.Unmarshal(raw, &plan); err != nil {
			return nil, err
		}
		return plan, nil
	case UpdateAvailableCmds:
		var update AvailableCommandsUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return update, nil
	case UpdateCurrentMode:
		var update CurrentModeUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return update, nil
	case UpdateConfigOption:
		var update ConfigOptionUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return update, nil
	case UpdateSessionInfo:
		var update SessionInfoUpdate
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return update, nil
	default:
		return nil, fmt.Errorf("acp/client: unknown session update %q", probe.SessionUpdate)
	}
}

type stderrBufferWriter struct {
	client *Client
}

func (w stderrBufferWriter) Write(p []byte) (int, error) {
	if w.client == nil || len(p) == 0 {
		return len(p), nil
	}
	w.client.stderrMu.Lock()
	defer w.client.stderrMu.Unlock()
	const limit = 32 * 1024
	if w.client.stderrBuf.Len()+len(p) > limit {
		trim := w.client.stderrBuf.Len() + len(p) - limit
		if trim >= w.client.stderrBuf.Len() {
			w.client.stderrBuf.Reset()
		} else if trim > 0 {
			rest := append([]byte(nil), w.client.stderrBuf.Bytes()[trim:]...)
			w.client.stderrBuf.Reset()
			_, _ = w.client.stderrBuf.Write(rest)
		}
	}
	_, err := w.client.stderrBuf.Write(p)
	return len(p), err
}
