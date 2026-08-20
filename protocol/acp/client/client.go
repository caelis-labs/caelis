package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/protocol/acp/transport/stdio"
)

type RequestHandler func(context.Context, jsonrpc.Message) (any, *jsonrpc.RPCError)
type NotificationHandler func(context.Context, jsonrpc.Message)
type PermissionHandler func(context.Context, RequestPermissionRequest) (RequestPermissionResponse, error)

type TerminalHandler interface {
	CreateTerminal(context.Context, CreateTerminalRequest) (CreateTerminalResponse, error)
	TerminalOutput(context.Context, TerminalOutputRequest) (TerminalOutputResponse, error)
	TerminalWaitForExit(context.Context, WaitForTerminalExitRequest) (WaitForTerminalExitResponse, error)
	TerminalKill(context.Context, KillTerminalRequest) error
	TerminalRelease(context.Context, ReleaseTerminalRequest) error
}

type FileSystemHandler interface {
	ReadTextFile(context.Context, ReadTextFileRequest) (ReadTextFileResponse, error)
	WriteTextFile(context.Context, WriteTextFileRequest) (WriteTextFileResponse, error)
}

type Config struct {
	Command             string
	Args                []string
	Env                 map[string]string
	WorkDir             string
	ClientInfo          *Implementation
	OnUpdate            func(UpdateEnvelope)
	OnPermissionRequest PermissionHandler
	Terminal            TerminalHandler
	TerminalAuth        bool
	FileSystem          FileSystemHandler
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
	clientCapabilities := map[string]any{
		"terminal": c.cfg.Terminal != nil,
		"_meta": map[string]any{
			metautil.TerminalOutputKey: true,
		},
	}
	if c.cfg.TerminalAuth {
		clientCapabilities["auth"] = map[string]any{"terminal": true}
	}
	if c.cfg.FileSystem != nil {
		clientCapabilities["fs"] = map[string]any{"readTextFile": true, "writeTextFile": true}
	}
	err := c.conn.Call(ctx, MethodInitialize, InitializeRequest{
		ProtocolVersion:    1,
		ClientCapabilities: clientCapabilities,
		ClientInfo:         c.cfg.ClientInfo,
	}, &resp)
	return resp, err
}

// Authenticate invokes the stable ACP v1 agent-managed authentication flow.
// Terminal authentication is out of band and must never call this method.
func (c *Client) Authenticate(ctx context.Context, methodID string) error {
	return c.conn.Call(ctx, MethodAuthenticate, AuthenticateRequest{
		MethodID: strings.TrimSpace(methodID),
	}, &AuthenticateResponse{})
}

func (c *Client) NewSession(ctx context.Context, cwd string, meta map[string]any) (NewSessionResponse, error) {
	var resp NewSessionResponse
	err := c.conn.Call(ctx, MethodSessionNew, NewSessionRequest{
		CWD:        cwd,
		MCPServers: []json.RawMessage{},
		Meta:       metautil.CloneMap(meta),
	}, &resp)
	return resp, err
}

func (c *Client) ListSessions(ctx context.Context, req SessionListRequest) (SessionListResponse, error) {
	var resp SessionListResponse
	err := c.conn.Call(ctx, MethodSessionList, req, &resp)
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

func (c *Client) ResumeSession(ctx context.Context, sessionID string, cwd string, meta map[string]any) (ResumeSessionResponse, error) {
	var resp ResumeSessionResponse
	err := c.conn.Call(ctx, MethodSessionResume, ResumeSessionRequest{
		SessionID:  sessionID,
		CWD:        cwd,
		MCPServers: []json.RawMessage{},
		Meta:       metautil.CloneMap(meta),
	}, &resp)
	return resp, err
}

func (c *Client) CloseSession(ctx context.Context, sessionID string) error {
	return c.conn.Call(ctx, MethodSessionClose, CloseSessionRequest{SessionID: strings.TrimSpace(sessionID)}, &CloseSessionResponse{})
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

func (c *Client) SetModel(ctx context.Context, sessionID string, modelID string) error {
	return c.conn.Call(ctx, MethodSessionSetModel, SetSessionModelRequest{
		SessionID: sessionID,
		ModelID:   modelID,
	}, &SetSessionModelResponse{})
}

func (c *Client) Prompt(ctx context.Context, sessionID string, text string, meta map[string]any) (PromptResponse, error) {
	return c.PromptParts(ctx, sessionID, []json.RawMessage{
		jsonrpc.MustMarshalRaw(TextContent{Type: "text", Text: text}),
	}, meta)
}

func (c *Client) PromptParts(ctx context.Context, sessionID string, prompt []json.RawMessage, meta map[string]any) (PromptResponse, error) {
	call, err := c.PreparePromptParts(sessionID, prompt, meta)
	if err != nil {
		return PromptResponse{}, err
	}
	if err := call.Dispatch(ctx); err != nil {
		return PromptResponse{}, err
	}
	return call.Wait(ctx)
}

// PromptCall separates writing one standard session/prompt request from
// waiting for its Turn-terminal response. It lets a target runner transfer the
// pending response to its own lifecycle after the complete request was written.
type PromptCall struct {
	inner *jsonrpc.PreparedCall
}

// PreparePromptParts encodes and registers a standard session/prompt request
// without touching the transport. Meta is retained for API symmetry; ACP prompt
// metadata is not currently part of the interoperable request shape.
func (c *Client) PreparePromptParts(sessionID string, prompt []json.RawMessage, meta map[string]any) (*PromptCall, error) {
	if c == nil || c.conn == nil {
		return nil, errors.New("acp client is unavailable")
	}
	_ = meta
	call, err := c.conn.PrepareCall(MethodSessionPrompt, PromptRequest{
		SessionID: sessionID,
		Prompt:    append([]json.RawMessage(nil), prompt...),
	})
	if err != nil {
		return nil, err
	}
	return &PromptCall{inner: call}, nil
}

// Dispatch writes the complete prompt request under caller-owned admission.
func (c *PromptCall) Dispatch(ctx context.Context) error {
	if c == nil || c.inner == nil {
		return errors.New("acp prompt call is unavailable")
	}
	return c.inner.Dispatch(ctx)
}

// DispatchWithAbort writes the prompt and revokes the exact transport when
// caller cancellation wins after writing starts. It is used by stable endpoint
// owners that can close and quarantine that transport.
func (c *PromptCall) DispatchWithAbort(ctx context.Context, abort func()) error {
	if c == nil || c.inner == nil {
		return errors.New("acp prompt call is unavailable")
	}
	return c.inner.DispatchWithAbort(ctx, abort)
}

// ObserveAuthRequired installs a short callback that runs before an
// auth-required prompt response becomes visible to Wait. The callback belongs
// to the client adapter boundary so bridge/runtime code need not inspect raw
// JSON-RPC messages or error payloads.
func (c *PromptCall) ObserveAuthRequired(observer func() error) error {
	if c == nil || c.inner == nil {
		return errors.New("acp prompt call is unavailable")
	}
	return c.inner.ObserveResponse(func(message jsonrpc.Message) error {
		if message.Error == nil || message.Error.Code != ErrorCodeAuthRequired || observer == nil {
			return nil
		}
		return observer()
	})
}

// Wait waits for the prompt response under the target producer's context.
func (c *PromptCall) Wait(ctx context.Context) (PromptResponse, error) {
	if c == nil || c.inner == nil {
		return PromptResponse{}, errors.New("acp prompt call is unavailable")
	}
	var resp PromptResponse
	err := c.inner.Wait(ctx, &resp)
	return resp, err
}

// Abandon releases pending state after a proven pre-write failure or transport
// isolation.
func (c *PromptCall) Abandon() {
	if c != nil && c.inner != nil {
		c.inner.Abandon()
	}
}

// Steer sends one text content block through the interoperable ACP steering
// extension.
func (c *Client) Steer(ctx context.Context, sessionID string, text string, meta map[string]json.RawMessage) (SessionSteeringResponse, error) {
	return c.SteerParts(ctx, sessionID, []json.RawMessage{
		jsonrpc.MustMarshalRaw(TextContent{Type: "text", Text: text}),
	}, meta)
}

// SteerParts sends ACP content blocks without assigning a session/prompt
// lifecycle to this client call.
func (c *Client) SteerParts(ctx context.Context, sessionID string, prompt []json.RawMessage, meta map[string]json.RawMessage) (SessionSteeringResponse, error) {
	return c.steerParts(ctx, sessionID, prompt, meta, nil)
}

// SteerPartsWithAbort gives an endpoint owner a way to revoke the exact
// transport when cancellation races a started steering write.
func (c *Client) SteerPartsWithAbort(
	ctx context.Context,
	sessionID string,
	prompt []json.RawMessage,
	meta map[string]json.RawMessage,
	abort func(),
) (SessionSteeringResponse, error) {
	return c.steerParts(ctx, sessionID, prompt, meta, abort)
}

func (c *Client) steerParts(
	ctx context.Context,
	sessionID string,
	prompt []json.RawMessage,
	meta map[string]json.RawMessage,
	abort func(),
) (SessionSteeringResponse, error) {
	var resp SessionSteeringResponse
	request := SessionSteeringRequest{
		SessionID: sessionID,
		Prompt:    append([]json.RawMessage(nil), prompt...),
		Meta:      cloneRawMessages(meta),
	}
	var err error
	if abort == nil {
		err = c.conn.Call(ctx, MethodSessionSteering, request, &resp)
	} else {
		err = c.conn.CallWithAbort(ctx, MethodSessionSteering, request, &resp, abort)
	}
	return resp, err
}

func cloneRawMessages(in map[string]json.RawMessage) map[string]json.RawMessage {
	if in == nil {
		return nil
	}
	out := make(map[string]json.RawMessage, len(in))
	for key, value := range in {
		out[key] = append(json.RawMessage(nil), value...)
	}
	return out
}

func (c *Client) Cancel(ctx context.Context, sessionID string) error {
	return c.conn.NotifyContext(ctx, MethodSessionCancel, CancelRequest{SessionID: sessionID})
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
	case MethodTerminalCreate:
		if c.cfg.Terminal == nil {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
		var req CreateTerminalRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
		}
		resp, err := c.cfg.Terminal.CreateTerminal(ctx, req)
		return responseOrRPCError(resp, err)
	case MethodTerminalOutput:
		if c.cfg.Terminal == nil {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
		var req TerminalOutputRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
		}
		resp, err := c.cfg.Terminal.TerminalOutput(ctx, req)
		return responseOrRPCError(resp, err)
	case MethodTerminalWaitForExit:
		if c.cfg.Terminal == nil {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
		var req WaitForTerminalExitRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
		}
		resp, err := c.cfg.Terminal.TerminalWaitForExit(ctx, req)
		return responseOrRPCError(resp, err)
	case MethodTerminalKill:
		if c.cfg.Terminal == nil {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
		var req KillTerminalRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
		}
		return responseOrRPCError(struct{}{}, c.cfg.Terminal.TerminalKill(ctx, req))
	case MethodTerminalRelease:
		if c.cfg.Terminal == nil {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
		var req ReleaseTerminalRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
		}
		return responseOrRPCError(struct{}{}, c.cfg.Terminal.TerminalRelease(ctx, req))
	case MethodReadTextFile:
		if c.cfg.FileSystem == nil {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
		var req ReadTextFileRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
		}
		resp, err := c.cfg.FileSystem.ReadTextFile(ctx, req)
		return responseOrRPCError(resp, err)
	case MethodWriteTextFile:
		if c.cfg.FileSystem == nil {
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
		var req WriteTextFileRequest
		if err := decodeParams(msg.Params, &req); err != nil {
			return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
		}
		resp, err := c.cfg.FileSystem.WriteTextFile(ctx, req)
		return responseOrRPCError(resp, err)
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
					Update:    NormalizeInboundUpdate(update),
					Raw:       append(json.RawMessage(nil), note.Update...),
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

func responseOrRPCError(resp any, err error) (any, *jsonrpc.RPCError) {
	if err != nil {
		return nil, &jsonrpc.RPCError{Code: -32000, Message: err.Error()}
	}
	return resp, nil
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
		var update ContentChunk
		if err := json.Unmarshal(raw, &update); err != nil {
			return nil, err
		}
		return update, nil
	case UpdateAvailableCmds:
		var update AvailableCommandsUpdate
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
	}
	return schema.DecodeUpdateJSON(raw)
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
