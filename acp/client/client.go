package client

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"sync/atomic"

	"github.com/OnslaughtSnail/caelis/acp"
)

// Client manages a JSON-RPC connection to an ACP agent over stdio.
type Client struct {
	stdin  io.WriteCloser
	reader *bufio.Reader // unbounded line reading (no 64K scanner limit)
	stderr *bufio.Scanner
	proc   *os.Process

	mu       sync.Mutex
	nextID   atomic.Int64
	pending  map[int64]chan *jsonrpcMessage
	handlers Handlers

	ctx    context.Context
	cancel context.CancelFunc
	closed chan struct{}
	errMu  sync.RWMutex
	err    error
}

// Handlers defines callbacks for incoming ACP requests and notifications.
type Handlers struct {
	// OnUpdate receives session/update notifications.
	OnUpdate func(acp.SessionNotification)
	// OnPermissionRequest handles session/request_permission requests.
	// The response is sent back to the agent automatically.
	OnPermissionRequest func(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)
	// Terminal handles typed terminal/* requests from the agent.
	Terminal acp.TerminalClientCallbacks
	// FileSystem handles typed fs/* requests from the agent.
	FileSystem acp.FileSystemClientCallbacks
}

// Config holds configuration for starting an ACP client.
type Config struct {
	Command  string
	Args     []string
	Env      []string
	WorkDir  string
	Handlers Handlers
}

// Start launches an ACP agent process and returns a connected client.
func Start(ctx context.Context, cfg Config) (*Client, error) {
	cmd := exec.CommandContext(ctx, cfg.Command, cfg.Args...)
	if cfg.WorkDir != "" {
		cmd.Dir = cfg.WorkDir
	}
	if len(cfg.Env) > 0 {
		cmd.Env = append(os.Environ(), cfg.Env...)
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("stderr pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start process: %w", err)
	}

	clientCtx, cancel := context.WithCancel(ctx)
	c := &Client{
		stdin:    stdin,
		reader:   bufio.NewReader(stdout),
		stderr:   bufio.NewScanner(stderr),
		proc:     cmd.Process,
		pending:  make(map[int64]chan *jsonrpcMessage),
		handlers: cfg.Handlers,
		ctx:      clientCtx,
		cancel:   cancel,
		closed:   make(chan struct{}),
	}

	go c.readLoop()
	go c.stderrLoop()

	return c, nil
}

// ─── Session lifecycle methods ───────────────────────────────────────

// Initialize performs the ACP initialize handshake.
func (c *Client) Initialize(ctx context.Context, req acp.InitializeRequest) (acp.InitializeResponse, error) {
	var resp acp.InitializeResponse
	if req.ProtocolVersion == 0 {
		req.ProtocolVersion = 1
	}
	err := c.callInto(ctx, acp.MethodInitialize, req, &resp)
	return resp, err
}

// Authenticate performs an ACP authenticate request.
func (c *Client) Authenticate(ctx context.Context, req acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	var resp acp.AuthenticateResponse
	err := c.callInto(ctx, acp.MethodAuthenticate, req, &resp)
	return resp, err
}

// NewSession creates a new ACP session.
func (c *Client) NewSession(ctx context.Context, req acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	var resp acp.NewSessionResponse
	err := c.callInto(ctx, acp.MethodSessionNew, req, &resp)
	return resp, err
}

// ListSessions lists sessions from the agent.
func (c *Client) ListSessions(ctx context.Context, req acp.SessionListRequest) (acp.SessionListResponse, error) {
	var resp acp.SessionListResponse
	err := c.callInto(ctx, acp.MethodSessionList, req, &resp)
	return resp, err
}

// LoadSession loads an existing ACP session.
func (c *Client) LoadSession(ctx context.Context, req acp.LoadSessionRequest) (acp.LoadSessionResponse, error) {
	var resp acp.LoadSessionResponse
	err := c.callInto(ctx, acp.MethodSessionLoad, req, &resp)
	return resp, err
}

// ResumeSession resumes an existing ACP session.
func (c *Client) ResumeSession(ctx context.Context, req acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	var resp acp.ResumeSessionResponse
	err := c.callInto(ctx, acp.MethodSessionResume, req, &resp)
	return resp, err
}

// Prompt sends a text prompt to the agent.
func (c *Client) Prompt(ctx context.Context, req acp.PromptRequest) (acp.PromptResponse, error) {
	var resp acp.PromptResponse
	err := c.callInto(ctx, acp.MethodSessionPrompt, req, &resp)
	return resp, err
}

// Cancel sends a cancel notification (no response expected).
func (c *Client) Cancel(ctx context.Context, sessionID string) error {
	return c.notify(ctx, acp.MethodSessionCancel, acp.CancelNotification{
		SessionID: sessionID,
	})
}

// CloseSession closes a session.
func (c *Client) CloseSession(ctx context.Context, req acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	var resp acp.CloseSessionResponse
	err := c.callInto(ctx, acp.MethodSessionClose, req, &resp)
	return resp, err
}

// SetSessionMode changes the current session mode.
func (c *Client) SetSessionMode(ctx context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	var resp acp.SetSessionModeResponse
	err := c.callInto(ctx, acp.MethodSessionSetMode, req, &resp)
	return resp, err
}

// SetSessionConfigOption changes one session config option.
func (c *Client) SetSessionConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	var resp acp.SetSessionConfigOptionResponse
	err := c.callInto(ctx, acp.MethodSessionSetConfig, req, &resp)
	return resp, err
}

// SetSessionModel changes the current session model.
func (c *Client) SetSessionModel(ctx context.Context, req acp.SetSessionModelRequest) (acp.SetSessionModelResponse, error) {
	var resp acp.SetSessionModelResponse
	err := c.callInto(ctx, acp.MethodSessionSetModel, req, &resp)
	return resp, err
}

// CreateTerminal asks the agent to create a terminal.
func (c *Client) CreateTerminal(ctx context.Context, req acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	var resp acp.CreateTerminalResponse
	err := c.callInto(ctx, acp.MethodTerminalCreate, req, &resp)
	return resp, err
}

// TerminalOutput reads terminal output from the agent.
func (c *Client) TerminalOutput(ctx context.Context, req acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	var resp acp.TerminalOutputResponse
	err := c.callInto(ctx, acp.MethodTerminalOutput, req, &resp)
	return resp, err
}

// TerminalWaitForExit waits for a terminal to exit.
func (c *Client) TerminalWaitForExit(ctx context.Context, req acp.TerminalWaitForExitRequest) (acp.TerminalWaitForExitResponse, error) {
	var resp acp.TerminalWaitForExitResponse
	err := c.callInto(ctx, acp.MethodTerminalWaitForExit, req, &resp)
	return resp, err
}

// TerminalKill kills a terminal.
func (c *Client) TerminalKill(ctx context.Context, req acp.TerminalKillRequest) error {
	_, err := c.call(ctx, acp.MethodTerminalKill, req)
	return err
}

// TerminalRelease releases a terminal.
func (c *Client) TerminalRelease(ctx context.Context, req acp.TerminalReleaseRequest) error {
	_, err := c.call(ctx, acp.MethodTerminalRelease, req)
	return err
}

// ReadTextFile reads a text file through the agent.
func (c *Client) ReadTextFile(ctx context.Context, req acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	var resp acp.ReadTextFileResponse
	err := c.callInto(ctx, acp.MethodReadTextFile, req, &resp)
	return resp, err
}

// WriteTextFile writes a text file through the agent.
func (c *Client) WriteTextFile(ctx context.Context, req acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	var resp acp.WriteTextFileResponse
	err := c.callInto(ctx, acp.MethodWriteTextFile, req, &resp)
	return resp, err
}

// ─── JSON-RPC transport ──────────────────────────────────────────────

// jsonrpcMessage is the universal JSON-RPC message type.
// ID is any to support both number and string IDs from external agents.
type jsonrpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// call sends a JSON-RPC request and returns the raw result.
// Returns the raw JSON result so callers can unmarshal into concrete types.
func (c *Client) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan *jsonrpcMessage, 1)

	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	rawParams, err := marshalRaw(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	data, err := json.Marshal(jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  rawParams,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')

	c.mu.Lock()
	_, err = c.stdin.Write(data)
	c.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}

	select {
	case msg := <-ch:
		if msg.Error != nil {
			return nil, fmt.Errorf("RPC error %d: %s", msg.Error.Code, msg.Error.Message)
		}
		return msg.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, c.closedError()
	}
}

func (c *Client) callInto(ctx context.Context, method string, params any, out any) error {
	raw, err := c.call(ctx, method, params)
	if err != nil {
		return err
	}
	if out == nil || string(raw) == "null" {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("unmarshal %s response: %w", method, err)
	}
	return nil
}

func (c *Client) notify(ctx context.Context, method string, params any) error {
	rawParams, err := marshalRaw(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	data, err := json.Marshal(jsonrpcMessage{
		JSONRPC: "2.0",
		Method:  method,
		Params:  rawParams,
	})
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	data = append(data, '\n')

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.closed:
		return c.closedError()
	default:
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.stdin.Write(data)
	return err
}

// respond sends a JSON-RPC response to an incoming request.
func (c *Client) respond(id any, result any, rpcErr *jsonrpcError) error {
	msg := jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      id,
	}
	if rpcErr != nil {
		msg.Error = rpcErr
	} else {
		rawResult, err := marshalRaw(result)
		if err != nil {
			return fmt.Errorf("marshal response result: %w", err)
		}
		msg.Result = rawResult
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal response: %w", err)
	}
	data = append(data, '\n')

	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.stdin.Write(data)
	return err
}

func marshalRaw(v any) (json.RawMessage, error) {
	if v == nil {
		return json.RawMessage("null"), nil
	}
	if raw, ok := v.(json.RawMessage); ok {
		if len(raw) == 0 {
			return json.RawMessage("null"), nil
		}
		return raw, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// readLoop reads JSON-RPC messages from stdout using ReadBytes (no size limit).
func (c *Client) readLoop() {
	defer func() {
		if c.cancel != nil {
			c.cancel()
		}
		close(c.closed)
	}()

	for {
		line, err := c.reader.ReadBytes('\n')
		if len(line) > 0 {
			line = trimNewline(line)
			if len(line) > 0 {
				c.dispatchLine(line)
			}
		}
		if err != nil {
			if err != io.EOF {
				c.setErr(err)
			}
			return
		}
	}
}

func trimNewline(b []byte) []byte {
	if len(b) > 0 && b[len(b)-1] == '\n' {
		b = b[:len(b)-1]
	}
	if len(b) > 0 && b[len(b)-1] == '\r' {
		b = b[:len(b)-1]
	}
	return b
}

// dispatchLine parses and dispatches a single JSON-RPC message.
func (c *Client) dispatchLine(line []byte) {
	var msg jsonrpcMessage
	if err := json.Unmarshal(line, &msg); err != nil {
		return
	}

	switch {
	case msg.ID != nil && msg.Method != "":
		// Incoming request — dispatch in goroutine, send response.
		id := msg.ID
		method := msg.Method
		params := msg.Params
		go func() {
			result, rpcErr := c.dispatchRequest(method, params)
			_ = c.respond(id, result, rpcErr) // best-effort; cannot propagate error from goroutine
		}()

	case msg.ID != nil && len(msg.Result) == 0 && msg.Error == nil:
		if c.deliverMalformedResponse(msg.ID) {
			return
		}
		_ = c.respond(msg.ID, nil, &jsonrpcError{Code: -32600, Message: "invalid request"})

	case msg.ID != nil:
		// Response to our request — deliver to pending caller.
		if id, ok := toInt64(msg.ID); ok {
			c.mu.Lock()
			ch, ok := c.pending[id]
			c.mu.Unlock()
			if ok {
				ch <- &msg
			}
		}

	case msg.Method != "":
		// Notification — dispatch in goroutine.
		method := msg.Method
		params := msg.Params
		go func() {
			c.dispatchNotification(method, params)
		}()
	}
}

// toInt64 converts a JSON number ID to int64.
func toInt64(id any) (int64, bool) {
	switch v := id.(type) {
	case float64:
		return int64(v), true
	case int64:
		return v, true
	case int:
		return int64(v), true
	default:
		return 0, false
	}
}

func (c *Client) stderrLoop() {
	for c.stderr.Scan() {
		// Could log stderr lines here.
	}
}

func (c *Client) callbackContext() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func (c *Client) setErr(err error) {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	c.err = err
}

func (c *Client) closeErr() error {
	c.errMu.RLock()
	defer c.errMu.RUnlock()
	return c.err
}

func (c *Client) closedError() error {
	if err := c.closeErr(); err != nil {
		return fmt.Errorf("client closed: %w", err)
	}
	return fmt.Errorf("client closed")
}

func (c *Client) deliverMalformedResponse(id any) bool {
	n, ok := toInt64(id)
	if !ok {
		return false
	}
	c.mu.Lock()
	ch, ok := c.pending[n]
	c.mu.Unlock()
	if !ok {
		return false
	}
	ch <- &jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      id,
		Error:   &jsonrpcError{Code: -32600, Message: "invalid response"},
	}
	return true
}

// dispatchRequest handles incoming JSON-RPC requests and returns a result or error.
func (c *Client) dispatchRequest(method string, params json.RawMessage) (any, *jsonrpcError) {
	ctx := c.callbackContext()
	switch method {
	case acp.MethodSessionReqPermission:
		var req acp.RequestPermissionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: "invalid params"}
		}
		if c.handlers.OnPermissionRequest == nil {
			return nil, &jsonrpcError{Code: -32601, Message: "no permission handler"}
		}
		resp, err := c.handlers.OnPermissionRequest(ctx, req)
		if err != nil {
			return nil, &jsonrpcError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case acp.MethodTerminalCreate:
		var req acp.CreateTerminalRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: "invalid params"}
		}
		if c.handlers.Terminal == nil {
			return nil, &jsonrpcError{Code: -32601, Message: "no terminal handler"}
		}
		result, err := c.handlers.Terminal.CreateTerminal(ctx, req)
		if err != nil {
			return nil, &jsonrpcError{Code: -32000, Message: err.Error()}
		}
		return result, nil

	case acp.MethodTerminalOutput:
		var req acp.TerminalOutputRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: "invalid params"}
		}
		if c.handlers.Terminal == nil {
			return nil, &jsonrpcError{Code: -32601, Message: "no terminal handler"}
		}
		result, err := c.handlers.Terminal.TerminalOutput(ctx, req)
		if err != nil {
			return nil, &jsonrpcError{Code: -32000, Message: err.Error()}
		}
		return result, nil

	case acp.MethodTerminalWaitForExit:
		var req acp.TerminalWaitForExitRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: "invalid params"}
		}
		if c.handlers.Terminal == nil {
			return nil, &jsonrpcError{Code: -32601, Message: "no terminal handler"}
		}
		result, err := c.handlers.Terminal.TerminalWaitForExit(ctx, req)
		if err != nil {
			return nil, &jsonrpcError{Code: -32000, Message: err.Error()}
		}
		return result, nil

	case acp.MethodTerminalKill:
		var req acp.TerminalKillRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: "invalid params"}
		}
		if c.handlers.Terminal == nil {
			return nil, &jsonrpcError{Code: -32601, Message: "no terminal handler"}
		}
		if err := c.handlers.Terminal.TerminalKill(ctx, req); err != nil {
			return nil, &jsonrpcError{Code: -32000, Message: err.Error()}
		}
		return nil, nil

	case acp.MethodTerminalRelease:
		var req acp.TerminalReleaseRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: "invalid params"}
		}
		if c.handlers.Terminal == nil {
			return nil, &jsonrpcError{Code: -32601, Message: "no terminal handler"}
		}
		if err := c.handlers.Terminal.TerminalRelease(ctx, req); err != nil {
			return nil, &jsonrpcError{Code: -32000, Message: err.Error()}
		}
		return nil, nil

	case acp.MethodReadTextFile:
		var req acp.ReadTextFileRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: "invalid params"}
		}
		if c.handlers.FileSystem == nil {
			return nil, &jsonrpcError{Code: -32601, Message: "no filesystem handler"}
		}
		result, err := c.handlers.FileSystem.ReadTextFile(ctx, req)
		if err != nil {
			return nil, &jsonrpcError{Code: -32000, Message: err.Error()}
		}
		return result, nil

	case acp.MethodWriteTextFile:
		var req acp.WriteTextFileRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, &jsonrpcError{Code: -32602, Message: "invalid params"}
		}
		if c.handlers.FileSystem == nil {
			return nil, &jsonrpcError{Code: -32601, Message: "no filesystem handler"}
		}
		result, err := c.handlers.FileSystem.WriteTextFile(ctx, req)
		if err != nil {
			return nil, &jsonrpcError{Code: -32000, Message: err.Error()}
		}
		return result, nil

	default:
		return nil, &jsonrpcError{Code: -32601, Message: fmt.Sprintf("unhandled method: %s", method)}
	}
}

// dispatchNotification handles incoming JSON-RPC notifications (no response).
func (c *Client) dispatchNotification(method string, params json.RawMessage) {
	switch method {
	case "session/update":
		var raw struct {
			SessionID string          `json:"sessionId"`
			Update    json.RawMessage `json:"update"`
		}
		if err := json.Unmarshal(params, &raw); err == nil && c.handlers.OnUpdate != nil {
			update := parseUpdate(raw.Update)
			if update != nil {
				c.handlers.OnUpdate(acp.SessionNotification{
					SessionID: raw.SessionID,
					Update:    update,
				})
			}
		}
	}
}

// parseUpdate determines the concrete Update type from raw JSON.
func parseUpdate(raw json.RawMessage) acp.Update {
	if len(raw) == 0 {
		return nil
	}
	var probe struct {
		SessionUpdate string `json:"sessionUpdate"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil
	}

	switch acp.UpdateKind(probe.SessionUpdate) {
	case acp.UpdateUserMessage, acp.UpdateAgentMessage, acp.UpdateAgentThought:
		var chunk acp.ContentChunk
		if err := json.Unmarshal(raw, &chunk); err == nil {
			return chunk
		}
	case acp.UpdateToolCall, acp.UpdateToolCallInfo:
		var tc acp.ToolCallUpdate
		if err := json.Unmarshal(raw, &tc); err == nil {
			return tc
		}
	case acp.UpdatePlan:
		var pu acp.PlanUpdate
		if err := json.Unmarshal(raw, &pu); err == nil {
			return pu
		}
	case acp.UpdateAvailableCmds:
		var u acp.AvailableCommandsUpdate
		if err := json.Unmarshal(raw, &u); err == nil {
			return u
		}
	case acp.UpdateCurrentMode:
		var u acp.CurrentModeUpdate
		if err := json.Unmarshal(raw, &u); err == nil {
			return u
		}
	case acp.UpdateConfigOption:
		var u acp.ConfigOptionUpdate
		if err := json.Unmarshal(raw, &u); err == nil {
			return u
		}
	case acp.UpdateSessionInfo:
		var u acp.SessionInfoUpdate
		if err := json.Unmarshal(raw, &u); err == nil {
			return u
		}
	}
	return nil
}

// Close shuts down the client and terminates the process.
func (c *Client) Close() error {
	if c.cancel != nil {
		c.cancel()
	}
	if c.stdin != nil {
		_ = c.stdin.Close() // best-effort cleanup
	}
	if c.proc != nil {
		_ = c.proc.Kill() // best-effort cleanup
	}
	return nil
}
