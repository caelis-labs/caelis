package acp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"
)

// ─── Mock Agent ──────────────────────────────────────────────────────

type mockAgent struct {
	sessions map[string]bool
	updates  []SessionNotification
}

func newMockAgent() *mockAgent {
	return &mockAgent{sessions: make(map[string]bool)}
}

func (a *mockAgent) Initialize(_ context.Context, req InitializeRequest) (InitializeResponse, error) {
	return InitializeResponse{
		ProtocolVersion: 1,
		AgentCapabilities: AgentCapabilities{
			LoadSession: true,
			Tools:       []ToolCapability{{Name: "READ"}, {Name: "WRITE"}},
			Streaming:   true,
		},
	}, nil
}

func (a *mockAgent) NewSession(_ context.Context, req NewSessionRequest) (NewSessionResponse, error) {
	id := "sess-" + timeNowStr()
	a.sessions[id] = true
	return NewSessionResponse{SessionID: id}, nil
}

func (a *mockAgent) Prompt(_ context.Context, req PromptRequest, cb PromptCallbacks) (PromptResponse, error) {
	// Send a session/update notification.
	cb.OnUpdate(SessionNotification{
		SessionID: req.SessionID,
		Update: ContentChunk{
			SessionUpdate: UpdateAgentMessage,
			Content:       TextContent{Type: "text", Text: "echo: hello"},
		},
	})
	a.updates = append(a.updates, SessionNotification{
		SessionID: req.SessionID,
	})
	return PromptResponse{StopReason: "end_turn"}, nil
}

func (a *mockAgent) Cancel(_ context.Context, sessionID string) error {
	return nil
}

func (a *mockAgent) CloseSession(_ context.Context, sessionID string) error {
	delete(a.sessions, sessionID)
	return nil
}

type approvalAgent struct {
	*mockAgent
	response RequestPermissionResponse
}

func (a *approvalAgent) Prompt(_ context.Context, req PromptRequest, cb PromptCallbacks) (PromptResponse, error) {
	resp, err := cb.OnPermissionRequest(RequestPermissionRequest{
		SessionID: req.SessionID,
		ToolCall: ToolCallUpdate{
			SessionUpdate: UpdateToolCallInfo,
			ToolCallID:    "tc-1",
			Title:         "RUN_COMMAND",
			Kind:          "execute",
		},
		Options: []PermissionOption{{OptionID: PermAllowOnce, Name: "Allow once", Kind: PermAllowOnce}},
	})
	if err != nil {
		return PromptResponse{}, err
	}
	a.response = resp
	return PromptResponse{StopReason: StopReasonEndTurn}, nil
}

type disconnectApprovalAgent struct {
	*mockAgent
	done chan<- error
}

func (a *disconnectApprovalAgent) Prompt(_ context.Context, req PromptRequest, cb PromptCallbacks) (PromptResponse, error) {
	_, err := cb.OnPermissionRequest(RequestPermissionRequest{
		SessionID: req.SessionID,
		ToolCall: ToolCallUpdate{
			SessionUpdate: UpdateToolCallInfo,
			ToolCallID:    "tc-1",
			Title:         "RUN_COMMAND",
			Kind:          "execute",
		},
		Options: []PermissionOption{{OptionID: PermAllowOnce, Name: "Allow once", Kind: PermAllowOnce}},
	})
	a.done <- err
	if err != nil {
		return PromptResponse{}, err
	}
	return PromptResponse{StopReason: StopReasonEndTurn}, nil
}

type terminalCallbackAgent struct {
	*mockAgent
	terminalID string
}

func (a *terminalCallbackAgent) Prompt(ctx context.Context, req PromptRequest, cb PromptCallbacks) (PromptResponse, error) {
	terminal, ok := cb.(TerminalClientCallbacks)
	if !ok {
		return PromptResponse{}, fmt.Errorf("callbacks do not support terminal")
	}
	resp, err := terminal.CreateTerminal(ctx, CreateTerminalRequest{
		SessionID: req.SessionID,
		Command:   "npm",
		Args:      []string{"test"},
	})
	if err != nil {
		return PromptResponse{}, err
	}
	a.terminalID = resp.TerminalID
	return PromptResponse{StopReason: StopReasonEndTurn}, nil
}

// ─── Handler tests ───────────────────────────────────────────────────

func TestHandler_Initialize(t *testing.T) {
	agent := newMockAgent()
	h := NewHandler(agent)

	params, _ := json.Marshal(map[string]any{"protocolVersion": 1})
	result, rpcErr := h.HandleRequest(context.Background(), "initialize", params)
	if rpcErr != nil {
		t.Fatalf("error: %v", rpcErr)
	}
	resp, ok := result.(InitializeResponse)
	if !ok {
		t.Fatalf("expected InitializeResponse, got %T", result)
	}
	if resp.ProtocolVersion != 1 {
		t.Errorf("version: %d", resp.ProtocolVersion)
	}
	if !resp.AgentCapabilities.LoadSession {
		t.Error("expected loadSession")
	}
}

func TestHandler_NewSession(t *testing.T) {
	agent := newMockAgent()
	h := NewHandler(agent)

	params, _ := json.Marshal(map[string]any{"cwd": "/tmp"})
	result, rpcErr := h.HandleRequest(context.Background(), "session/new", params)
	if rpcErr != nil {
		t.Fatalf("error: %v", rpcErr)
	}
	resp, ok := result.(NewSessionResponse)
	if !ok {
		t.Fatalf("expected NewSessionResponse, got %T", result)
	}
	if resp.SessionID == "" {
		t.Error("expected non-empty session ID")
	}
}

func TestHandler_Prompt(t *testing.T) {
	agent := newMockAgent()
	h := NewHandler(agent)

	// Create a session first.
	params, _ := json.Marshal(map[string]any{"cwd": "/tmp"})
	sessionResp, _ := h.HandleRequest(context.Background(), "session/new", params)
	sid := sessionResp.(NewSessionResponse).SessionID

	// Prompt the session.
	promptParams, _ := json.Marshal(map[string]any{
		"sessionId": sid,
		"prompt":    []map[string]any{{"type": "text", "text": "hello"}},
	})
	result, rpcErr := h.HandleRequest(context.Background(), "session/prompt", promptParams)
	if rpcErr != nil {
		t.Fatalf("error: %v", rpcErr)
	}
	resp, ok := result.(PromptResponse)
	if !ok {
		t.Fatalf("expected PromptResponse, got %T", result)
	}
	if resp.StopReason != "end_turn" {
		t.Errorf("stopReason: %q", resp.StopReason)
	}

	// Verify the agent received an update callback.
	if len(agent.updates) == 0 {
		t.Error("expected at least one update notification")
	}
}

func TestHandler_Cancel(t *testing.T) {
	agent := newMockAgent()
	h := NewHandler(agent)

	params, _ := json.Marshal(map[string]any{"sessionId": "sess-1"})
	result, rpcErr := h.HandleRequest(context.Background(), "session/cancel", params)
	if rpcErr != nil {
		t.Fatalf("error: %v", rpcErr)
	}
	if result != nil {
		t.Errorf("expected nil result, got %v", result)
	}
}

func TestHandler_CloseSession(t *testing.T) {
	agent := newMockAgent()
	h := NewHandler(agent)

	// Create and close.
	params, _ := json.Marshal(map[string]any{"cwd": "/tmp"})
	sessionResp, _ := h.HandleRequest(context.Background(), "session/new", params)
	sid := sessionResp.(NewSessionResponse).SessionID

	closeParams, _ := json.Marshal(map[string]any{"sessionId": sid})
	_, rpcErr := h.HandleRequest(context.Background(), "session/close", closeParams)
	if rpcErr != nil {
		t.Fatalf("error: %v", rpcErr)
	}

	// Verify session is gone.
	if _, ok := agent.sessions[sid]; ok {
		t.Error("session should be closed")
	}
}

func TestHandler_UnknownMethod(t *testing.T) {
	h := NewHandler(newMockAgent())
	_, rpcErr := h.HandleRequest(context.Background(), "unknown/method", nil)
	if rpcErr == nil {
		t.Fatal("expected error")
	}
	if rpcErr.Code != -32601 {
		t.Errorf("code: %d", rpcErr.Code)
	}
}

func TestHandler_InvalidParams(t *testing.T) {
	h := NewHandler(newMockAgent())
	_, rpcErr := h.HandleRequest(context.Background(), "initialize", []byte("invalid"))
	if rpcErr == nil {
		t.Fatal("expected error")
	}
	if rpcErr.Code != -32602 {
		t.Errorf("code: %d", rpcErr.Code)
	}
}

// ─── Loopback tests ─────────────────────────────────────────────────

func TestLoopback_InitializeAndPrompt(t *testing.T) {
	agent := newMockAgent()
	handler := NewHandler(agent)
	loopback := NewLoopback(handler)
	ctx := context.Background()

	// Initialize.
	result, err := loopback.Client.Call(ctx, "initialize", map[string]any{"protocolVersion": 1})
	if err != nil {
		t.Fatalf("Initialize: %v", err)
	}
	var initResp InitializeResponse
	if err := json.Unmarshal(result, &initResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if initResp.ProtocolVersion != 1 {
		t.Errorf("version: %d", initResp.ProtocolVersion)
	}

	// New session.
	result, err = loopback.Client.Call(ctx, "session/new", map[string]any{"cwd": "/tmp"})
	if err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	var sessionResp NewSessionResponse
	if err := json.Unmarshal(result, &sessionResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if sessionResp.SessionID == "" {
		t.Fatal("expected session ID")
	}

	// Prompt.
	result, err = loopback.Client.Call(ctx, "session/prompt", map[string]any{
		"sessionId": sessionResp.SessionID,
		"prompt":    []map[string]any{{"type": "text", "text": "hello"}},
	})
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	var promptResp PromptResponse
	if err := json.Unmarshal(result, &promptResp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if promptResp.StopReason != "end_turn" {
		t.Errorf("stopReason: %q", promptResp.StopReason)
	}

	// Verify agent received update.
	if len(agent.updates) == 0 {
		t.Error("expected update notification")
	}
}

func TestLoopback_FullLifecycle(t *testing.T) {
	agent := newMockAgent()
	handler := NewHandler(agent)
	loopback := NewLoopback(handler)
	ctx := context.Background()

	// Initialize.
	loopback.Client.Call(ctx, "initialize", map[string]any{"protocolVersion": 1})

	// New session.
	result, _ := loopback.Client.Call(ctx, "session/new", map[string]any{"cwd": "/tmp"})
	var sess NewSessionResponse
	json.Unmarshal(result, &sess)

	// Prompt.
	loopback.Client.Call(ctx, "session/prompt", map[string]any{
		"sessionId": sess.SessionID,
		"prompt":    []map[string]any{{"type": "text", "text": "test"}},
	})

	// Cancel.
	_, err := loopback.Client.Call(ctx, "session/cancel", map[string]any{"sessionId": sess.SessionID})
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}

	// Close.
	_, err = loopback.Client.Call(ctx, "session/close", map[string]any{"sessionId": sess.SessionID})
	if err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Verify session is gone.
	if _, ok := agent.sessions[sess.SessionID]; ok {
		t.Error("session should be closed after close")
	}
}

func TestLoopback_RPCErrors(t *testing.T) {
	handler := NewHandler(newMockAgent())
	loopback := NewLoopback(handler)
	ctx := context.Background()

	// Unknown method.
	_, err := loopback.Client.Call(ctx, "unknown/method", nil)
	if err == nil {
		t.Fatal("expected error")
	}
}

// ─── IO-based Serve tests ────────────────────────────────────────────

func TestServe_IO(t *testing.T) {
	agent := newMockAgent()
	handler := NewHandler(agent)

	// Create a pipe for IO-based testing.
	pr, pw := pipe()

	// Write a JSON-RPC request.
	req := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1}}` + "\n"
	go func() {
		pw.Write([]byte(req))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Serve reads from pr and writes to a buffer.
	var outBuf strings.Builder
	err := Serve(ctx, handler, pr, &outBuf)
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}

	output := outBuf.String()
	if !strings.Contains(output, `"protocolVersion":1`) {
		t.Errorf("expected protocolVersion in response: %s", output)
	}
	if !strings.Contains(output, `"jsonrpc":"2.0"`) {
		t.Errorf("expected jsonrpc in response: %s", output)
	}
}

func TestServe_PromptStreamsSessionUpdate(t *testing.T) {
	handler := NewHandler(newMockAgent())
	pr, pw := pipe()
	req := `{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"sess-1","prompt":[{"type":"text","text":"hello"}]}}` + "\n"
	go func() {
		pw.Write([]byte(req))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var out strings.Builder
	if err := Serve(ctx, handler, pr, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, `"method":"session/update"`) {
		t.Fatalf("expected session/update notification, got: %s", output)
	}
	if !strings.Contains(output, `"stopReason":"end_turn"`) {
		t.Fatalf("expected prompt response, got: %s", output)
	}
}

func TestServe_PromptPermissionRoundTrip(t *testing.T) {
	agent := &approvalAgent{mockAgent: newMockAgent()}
	handler := NewHandler(agent)
	pr, pw := pipe()
	go func() {
		pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"sess-1","prompt":[{"type":"text","text":"run"}]}}` + "\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"outcome":{"outcome":"selected","optionId":"allow_once"}}}` + "\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var out strings.Builder
	if err := Serve(ctx, handler, pr, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if agent.response.Outcome.OptionID != PermAllowOnce {
		t.Fatalf("permission response: %#v", agent.response)
	}
	output := out.String()
	if !strings.Contains(output, `"method":"session/request_permission"`) {
		t.Fatalf("expected request_permission request, got: %s", output)
	}
	if !strings.Contains(output, `"stopReason":"end_turn"`) {
		t.Fatalf("expected prompt response, got: %s", output)
	}
}

func TestServe_ParseErrorRespondsWithNullID(t *testing.T) {
	handler := NewHandler(newMockAgent())
	pr, pw := pipe()
	go func() {
		pw.Write([]byte("{not-json}\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var out strings.Builder
	if err := Serve(ctx, handler, pr, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, `"id":null`) || !strings.Contains(output, `"code":-32700`) {
		t.Fatalf("expected parse error with id null, got: %s", output)
	}
}

func TestServe_InvalidRequestRespondsWithOriginalID(t *testing.T) {
	handler := NewHandler(newMockAgent())
	pr, pw := pipe()
	go func() {
		pw.Write([]byte(`{"jsonrpc":"2.0","id":"bad-1"}` + "\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var out strings.Builder
	if err := Serve(ctx, handler, pr, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, `"id":"bad-1"`) || !strings.Contains(output, `"code":-32600`) {
		t.Fatalf("expected invalid request response, got: %s", output)
	}
}

func TestServe_MalformedPendingResponseUnblocksCallback(t *testing.T) {
	done := make(chan error, 1)
	agent := &disconnectApprovalAgent{mockAgent: newMockAgent(), done: done}
	handler := NewHandler(agent)
	pr, pw := pipe()
	go func() {
		pw.Write([]byte(`{"jsonrpc":"2.0","id":99,"method":"session/prompt","params":{"sessionId":"sess-1","prompt":[{"type":"text","text":"run"}]}}` + "\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Write([]byte(`{"jsonrpc":"2.0","id":1}` + "\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var out strings.Builder
	if err := Serve(ctx, handler, pr, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "RPC error -32600") {
			t.Fatalf("expected invalid response callback error, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("callback did not unblock")
	}
	if strings.Contains(out.String(), `"id":1`) && strings.Contains(out.String(), `"code":-32600`) {
		t.Fatalf("malformed pending response was incorrectly echoed as request error: %s", out.String())
	}
}

func TestServe_PromptTerminalCallbackRoundTrip(t *testing.T) {
	agent := &terminalCallbackAgent{mockAgent: newMockAgent()}
	handler := NewHandler(agent)
	pr, pw := pipe()
	go func() {
		pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"sess-1","prompt":[{"type":"text","text":"run tests"}]}}` + "\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"terminalId":"term-1"}}` + "\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var out strings.Builder
	if err := Serve(ctx, handler, pr, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	if agent.terminalID != "term-1" {
		t.Fatalf("terminal id: got %q, want term-1", agent.terminalID)
	}
	output := out.String()
	if !strings.Contains(output, `"method":"terminal/create"`) || !strings.Contains(output, `"stopReason":"end_turn"`) {
		t.Fatalf("expected terminal/create callback and prompt response, got: %s", output)
	}
}

func TestServe_PromptCallbackUnblocksOnDisconnect(t *testing.T) {
	done := make(chan error, 1)
	agent := &disconnectApprovalAgent{mockAgent: newMockAgent(), done: done}
	handler := NewHandler(agent)
	pr, pw := pipe()
	go func() {
		pw.Write([]byte(`{"jsonrpc":"2.0","id":1,"method":"session/prompt","params":{"sessionId":"sess-1","prompt":[{"type":"text","text":"run"}]}}` + "\n"))
		time.Sleep(50 * time.Millisecond)
		pw.Close()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	var out strings.Builder
	if err := Serve(ctx, handler, pr, &out); err != nil {
		t.Fatalf("Serve: %v", err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected callback error after disconnect")
		}
	case <-time.After(time.Second):
		t.Fatal("callback did not unblock after disconnect")
	}
}

// ─── Helpers ─────────────────────────────────────────────────────────

func timeNowStr() string {
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// pipe creates an in-memory io.Pipe for testing.
func pipe() (*pipeReader, *pipeWriter) {
	ch := make(chan []byte, 16)
	done := make(chan struct{})
	return &pipeReader{ch: ch, done: done}, &pipeWriter{ch: ch, done: done}
}

type pipeReader struct {
	ch   chan []byte
	done chan struct{}
	buf  []byte
}

func (r *pipeReader) Read(p []byte) (int, error) {
	for {
		if len(r.buf) > 0 {
			n := copy(p, r.buf)
			r.buf = r.buf[n:]
			return n, nil
		}
		select {
		case data, ok := <-r.ch:
			if !ok {
				return 0, io.EOF
			}
			r.buf = data
		case <-time.After(5 * time.Second):
			return 0, fmt.Errorf("timeout")
		}
	}
}

type pipeWriter struct {
	ch   chan []byte
	done chan struct{}
}

func (w *pipeWriter) Write(p []byte) (int, error) {
	cp := make([]byte, len(p))
	copy(cp, p)
	w.ch <- cp
	return len(p), nil
}

func (w *pipeWriter) Close() error {
	close(w.ch)
	return nil
}
