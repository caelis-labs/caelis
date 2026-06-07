package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
)

// Handler dispatches incoming JSON-RPC messages to an Agent implementation.
// It is the server-side counterpart to client.Client.
type Handler struct {
	agent Agent
}

// NewHandler creates a handler that dispatches to the given agent.
func NewHandler(agent Agent) *Handler {
	return &Handler{agent: agent}
}

// HandleRequest processes a JSON-RPC request and returns a result or error.
func (h *Handler) HandleRequest(ctx context.Context, method string, params json.RawMessage) (any, *RPCError) {
	return h.handleRequest(ctx, method, params, &noopCallbacks{})
}

func (h *Handler) handleRequest(ctx context.Context, method string, params json.RawMessage, callbacks PromptCallbacks) (any, *RPCError) {
	switch method {
	case MethodInitialize:
		var req InitializeRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		resp, err := h.agent.Initialize(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodAuthenticate:
		var req AuthenticateRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		auth, ok := h.agent.(Authenticator)
		if !ok {
			return nil, methodNotFound(method)
		}
		resp, err := auth.Authenticate(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodSessionNew:
		var req NewSessionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		resp, err := h.agent.NewSession(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodSessionList:
		var req SessionListRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		lister, ok := h.agent.(SessionLister)
		if !ok {
			return nil, methodNotFound(method)
		}
		resp, err := lister.ListSessions(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodSessionLoad:
		var req LoadSessionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		loader, ok := h.agent.(SessionLoader)
		if !ok {
			return nil, methodNotFound(method)
		}
		resp, err := loader.LoadSession(ctx, req, callbacks)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodSessionResume:
		var req ResumeSessionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		resumer, ok := h.agent.(SessionResumer)
		if !ok {
			return nil, methodNotFound(method)
		}
		resp, err := resumer.ResumeSession(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodSessionPrompt:
		var req PromptRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		resp, err := h.agent.Prompt(ctx, req, callbacks)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodSessionCancel:
		var req CancelNotification
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		if err := h.agent.Cancel(ctx, req.SessionID); err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return nil, nil

	case MethodSessionClose:
		var req CloseSessionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		if err := h.agent.CloseSession(ctx, req.SessionID); err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return CloseSessionResponse{}, nil

	case MethodSessionSetMode:
		var req SetSessionModeRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		setter, ok := h.agent.(SessionModeSetter)
		if !ok {
			return nil, methodNotFound(method)
		}
		resp, err := setter.SetSessionMode(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodSessionSetConfig:
		var req SetSessionConfigOptionRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		setter, ok := h.agent.(SessionConfigSetter)
		if !ok {
			return nil, methodNotFound(method)
		}
		resp, err := setter.SetSessionConfigOption(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodSessionSetModel:
		var req SetSessionModelRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		setter, ok := h.agent.(SessionModelSetter)
		if !ok {
			return nil, methodNotFound(method)
		}
		resp, err := setter.SetSessionModel(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodTerminalCreate:
		var req CreateTerminalRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		terminal, ok := h.agent.(TerminalProvider)
		if !ok {
			return nil, methodNotFound(method)
		}
		resp, err := terminal.CreateTerminal(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodTerminalOutput:
		var req TerminalOutputRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		terminal, ok := h.agent.(TerminalProvider)
		if !ok {
			return nil, methodNotFound(method)
		}
		resp, err := terminal.TerminalOutput(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodTerminalWaitForExit:
		var req TerminalWaitForExitRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		terminal, ok := h.agent.(TerminalProvider)
		if !ok {
			return nil, methodNotFound(method)
		}
		resp, err := terminal.TerminalWaitForExit(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodTerminalKill:
		var req TerminalKillRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		terminal, ok := h.agent.(TerminalProvider)
		if !ok {
			return nil, methodNotFound(method)
		}
		if err := terminal.TerminalKill(ctx, req); err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return nil, nil

	case MethodTerminalRelease:
		var req TerminalReleaseRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		terminal, ok := h.agent.(TerminalProvider)
		if !ok {
			return nil, methodNotFound(method)
		}
		if err := terminal.TerminalRelease(ctx, req); err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return nil, nil

	case MethodReadTextFile:
		var req ReadTextFileRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		fs, ok := h.agent.(FileSystemProvider)
		if !ok {
			return nil, methodNotFound(method)
		}
		resp, err := fs.ReadTextFile(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	case MethodWriteTextFile:
		var req WriteTextFileRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, invalidParams()
		}
		fs, ok := h.agent.(FileSystemProvider)
		if !ok {
			return nil, methodNotFound(method)
		}
		resp, err := fs.WriteTextFile(ctx, req)
		if err != nil {
			return nil, &RPCError{Code: -32000, Message: err.Error()}
		}
		return resp, nil

	default:
		return nil, methodNotFound(method)
	}
}

// HandleNotification processes a JSON-RPC notification (no response).
func (h *Handler) HandleNotification(ctx context.Context, method string, params json.RawMessage) {
	switch method {
	case MethodSessionCancel:
		var req CancelNotification
		if err := json.Unmarshal(params, &req); err == nil {
			h.agent.Cancel(ctx, req.SessionID)
		}
	}
}

// RPCError is a JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("RPC error %d: %s", e.Code, e.Message)
}

func invalidParams() *RPCError {
	return &RPCError{Code: -32602, Message: "invalid params"}
}

func methodNotFound(method string) *RPCError {
	return &RPCError{Code: -32601, Message: fmt.Sprintf("unhandled method: %s", method)}
}

// noopCallbacks is a no-op implementation of PromptCallbacks.
type noopCallbacks struct{}

func (c *noopCallbacks) OnUpdate(SessionNotification) {}
func (c *noopCallbacks) OnPermissionRequest(RequestPermissionRequest) (RequestPermissionResponse, error) {
	return PermissionSelectedOutcome(PermRejectOnce), nil
}

// ─── Loopback transport ──────────────────────────────────────────────

// Loopback creates an in-memory JSON-RPC connection between a handler
// and a client, useful for testing ACP agent implementations.
type Loopback struct {
	Client  *clientForLoopback
	handler *Handler
}

// NewLoopback creates a loopback connection.
func NewLoopback(handler *Handler) *Loopback {
	return &Loopback{
		Client:  &clientForLoopback{handler: handler},
		handler: handler,
	}
}

// clientForLoopback is a minimal client that dispatches directly to a handler.
type clientForLoopback struct {
	handler *Handler
}

// Call sends a request and returns the raw result.
func (c *clientForLoopback) Call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	data, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshal params: %w", err)
	}
	result, rpcErr := c.handler.HandleRequest(ctx, method, data)
	if rpcErr != nil {
		return nil, rpcErr
	}
	if result == nil {
		return json.RawMessage("null"), nil
	}
	return json.Marshal(result)
}

// ─── IO-based server ─────────────────────────────────────────────────

// Serve reads JSON-RPC messages from r, dispatches to handler, and writes
// responses, notifications, and callback requests to w. It keeps reading while
// requests are handled so prompt callbacks can synchronously wait for client
// responses on the same connection.
func Serve(ctx context.Context, handler *Handler, r io.Reader, w io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	conn := &serverConn{
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		handler: handler,
		reader:  newLineReader(r),
		writer:  newLineWriter(w),
		pending: make(map[int64]chan serverCallResult),
	}
	return conn.serve(ctx)
}

type serverConn struct {
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	closeMu  sync.Once
	closeErr error
	handler  *Handler
	reader   *lineReader
	writer   *lineWriter

	mu      sync.Mutex
	nextID  atomic.Int64
	pending map[int64]chan serverCallResult
	wg      sync.WaitGroup
}

type serverCallResult struct {
	raw json.RawMessage
	err error
}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

func (c *serverConn) serve(ctx context.Context) error {
	defer c.close(io.ErrClosedPipe)
	for {
		line, err := c.reader.readLine()
		if err != nil {
			c.close(err)
			c.wg.Wait()
			if err == io.EOF {
				return nil
			}
			return err
		}

		var msg rpcMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			_ = c.writeParseError()
			continue
		}

		switch {
		case msg.ID != nil && msg.Method != "":
			c.wg.Add(1)
			go func() {
				defer c.wg.Done()
				c.handleIncomingRequest(ctx, msg)
			}()
		case msg.ID != nil && len(msg.Result) == 0 && msg.Error == nil:
			if c.deliverMalformedResponse(msg.ID) {
				continue
			}
			_ = c.writeResponse(msg.ID, nil, &RPCError{Code: -32600, Message: "invalid request"})
		case msg.ID != nil:
			c.handleIncomingResponse(msg)
		case msg.Method != "":
			c.wg.Add(1)
			go func() {
				defer c.wg.Done()
				c.handler.HandleNotification(ctx, msg.Method, msg.Params)
			}()
		}
	}
}

func (c *serverConn) close(err error) {
	c.closeMu.Do(func() {
		c.closeErr = err
		if c.cancel != nil {
			c.cancel()
		}
		close(c.done)
	})
}

func (c *serverConn) handleIncomingRequest(ctx context.Context, msg rpcMessage) {
	result, rpcErr := c.handler.handleRequest(ctx, msg.Method, msg.Params, c)
	_ = c.writeResponse(msg.ID, result, rpcErr)
}

func (c *serverConn) handleIncomingResponse(msg rpcMessage) {
	id, ok := rpcIDToInt64(msg.ID)
	if !ok {
		return
	}
	c.mu.Lock()
	ch, ok := c.pending[id]
	c.mu.Unlock()
	if !ok {
		return
	}
	if msg.Error != nil {
		ch <- serverCallResult{err: msg.Error}
		return
	}
	if len(msg.Result) == 0 {
		ch <- serverCallResult{raw: json.RawMessage("null")}
		return
	}
	ch <- serverCallResult{raw: msg.Result}
}

func (c *serverConn) deliverMalformedResponse(id any) bool {
	n, ok := rpcIDToInt64(id)
	if !ok {
		return false
	}
	c.mu.Lock()
	ch, ok := c.pending[n]
	c.mu.Unlock()
	if !ok {
		return false
	}
	ch <- serverCallResult{err: &RPCError{Code: -32600, Message: "invalid response"}}
	return true
}

func (c *serverConn) OnUpdate(n SessionNotification) {
	_ = c.writeNotification(MethodSessionUpdate, n)
}

func (c *serverConn) OnPermissionRequest(req RequestPermissionRequest) (RequestPermissionResponse, error) {
	var resp RequestPermissionResponse
	ctx := c.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	if err := c.call(ctx, MethodSessionReqPermission, req, &resp); err != nil {
		return RequestPermissionResponse{}, err
	}
	return resp, nil
}

func (c *serverConn) CreateTerminal(ctx context.Context, req CreateTerminalRequest) (CreateTerminalResponse, error) {
	var resp CreateTerminalResponse
	if err := c.call(contextWithFallback(ctx, c.ctx), MethodTerminalCreate, req, &resp); err != nil {
		return CreateTerminalResponse{}, err
	}
	return resp, nil
}

func (c *serverConn) TerminalOutput(ctx context.Context, req TerminalOutputRequest) (TerminalOutputResponse, error) {
	var resp TerminalOutputResponse
	if err := c.call(contextWithFallback(ctx, c.ctx), MethodTerminalOutput, req, &resp); err != nil {
		return TerminalOutputResponse{}, err
	}
	return resp, nil
}

func (c *serverConn) TerminalWaitForExit(ctx context.Context, req TerminalWaitForExitRequest) (TerminalWaitForExitResponse, error) {
	var resp TerminalWaitForExitResponse
	if err := c.call(contextWithFallback(ctx, c.ctx), MethodTerminalWaitForExit, req, &resp); err != nil {
		return TerminalWaitForExitResponse{}, err
	}
	return resp, nil
}

func (c *serverConn) TerminalKill(ctx context.Context, req TerminalKillRequest) error {
	return c.call(contextWithFallback(ctx, c.ctx), MethodTerminalKill, req, nil)
}

func (c *serverConn) TerminalRelease(ctx context.Context, req TerminalReleaseRequest) error {
	return c.call(contextWithFallback(ctx, c.ctx), MethodTerminalRelease, req, nil)
}

func (c *serverConn) ReadTextFile(ctx context.Context, req ReadTextFileRequest) (ReadTextFileResponse, error) {
	var resp ReadTextFileResponse
	if err := c.call(contextWithFallback(ctx, c.ctx), MethodReadTextFile, req, &resp); err != nil {
		return ReadTextFileResponse{}, err
	}
	return resp, nil
}

func (c *serverConn) WriteTextFile(ctx context.Context, req WriteTextFileRequest) (WriteTextFileResponse, error) {
	var resp WriteTextFileResponse
	if err := c.call(contextWithFallback(ctx, c.ctx), MethodWriteTextFile, req, &resp); err != nil {
		return WriteTextFileResponse{}, err
	}
	return resp, nil
}

func contextWithFallback(primary context.Context, fallback context.Context) context.Context {
	if primary != nil {
		return primary
	}
	if fallback != nil {
		return fallback
	}
	return context.Background()
}

func (c *serverConn) call(ctx context.Context, method string, params any, out any) error {
	id := c.nextID.Add(1)
	ch := make(chan serverCallResult, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	if err := c.writeMessage(rpcMessage{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  rawParams,
	}); err != nil {
		return err
	}

	select {
	case result := <-ch:
		if result.err != nil {
			return result.err
		}
		raw := result.raw
		if out == nil || string(raw) == "null" {
			return nil
		}
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("unmarshal %s response: %w", method, err)
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		if c.closeErr != nil {
			return c.closeErr
		}
		return io.ErrClosedPipe
	}
}

func (c *serverConn) writeNotification(method string, params any) error {
	rawParams, err := json.Marshal(params)
	if err != nil {
		return fmt.Errorf("marshal params: %w", err)
	}
	return c.writeMessage(rpcMessage{JSONRPC: "2.0", Method: method, Params: rawParams})
}

func (c *serverConn) writeResponse(id any, result any, rpcErr *RPCError) error {
	msg := rpcMessage{JSONRPC: "2.0", ID: id}
	if rpcErr != nil {
		msg.Error = rpcErr
	} else {
		raw, err := json.Marshal(result)
		if err != nil {
			msg.Error = &RPCError{Code: -32603, Message: fmt.Sprintf("marshal response: %v", err)}
		} else {
			msg.Result = raw
		}
	}
	return c.writeMessage(msg)
}

func (c *serverConn) writeParseError() error {
	data, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      nil,
		"error":   &RPCError{Code: -32700, Message: "parse error"},
	})
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writer.writeLine(data)
}

func (c *serverConn) writeMessage(msg rpcMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writer.writeLine(data)
}

func rpcIDToInt64(id any) (int64, bool) {
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

type lineReader struct {
	r   io.Reader
	buf []byte
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{r: r}
}

func (lr *lineReader) readLine() ([]byte, error) {
	for {
		if idx := indexOfNewline(lr.buf); idx >= 0 {
			line := lr.buf[:idx]
			lr.buf = lr.buf[idx+1:]
			return line, nil
		}
		tmp := make([]byte, 4096)
		n, err := lr.r.Read(tmp)
		if n > 0 {
			lr.buf = append(lr.buf, tmp[:n]...)
		}
		if err != nil {
			if len(lr.buf) > 0 {
				line := lr.buf
				lr.buf = nil
				return line, nil
			}
			return nil, err
		}
	}
}

func indexOfNewline(b []byte) int {
	for i, c := range b {
		if c == '\n' {
			return i
		}
	}
	return -1
}

type lineWriter struct {
	w io.Writer
}

func newLineWriter(w io.Writer) *lineWriter {
	return &lineWriter{w: w}
}

func (lw *lineWriter) writeLine(data []byte) error {
	data = append(data, '\n')
	_, err := lw.w.Write(data)
	return err
}
