package client

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/acp"
)

type testWriteCloser struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *testWriteCloser) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.Write(p)
}

func (w *testWriteCloser) Close() error { return nil }

func (w *testWriteCloser) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}

func TestClientJSONRPCRequest(t *testing.T) {
	params, err := marshalRaw(map[string]any{"protocolVersion": 1})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	msg := jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      int64(1),
		Method:  "initialize",
		Params:  params,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed jsonrpcMessage
	json.Unmarshal(data, &parsed)
	if parsed.Method != "initialize" {
		t.Errorf("method: %v", parsed.Method)
	}
}

func TestClientNotification(t *testing.T) {
	params, err := marshalRaw(map[string]any{"sessionId": "sess-1"})
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	msg := jsonrpcMessage{
		JSONRPC: "2.0",
		Method:  "session/cancel",
		Params:  params,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var parsed jsonrpcMessage
	json.Unmarshal(data, &parsed)
	if parsed.Method != "session/cancel" {
		t.Errorf("method: %v", parsed.Method)
	}
	if parsed.ID != nil {
		t.Error("notification should not have id")
	}
}

func TestDispatchNotification_Update(t *testing.T) {
	var received acp.SessionNotification
	c := &Client{
		handlers: Handlers{
			OnUpdate: func(n acp.SessionNotification) {
				received = n
			},
		},
	}

	params := []byte(`{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"}}}`)
	c.dispatchNotification("session/update", params)

	if received.SessionID != "sess-1" {
		t.Errorf("session: %q", received.SessionID)
	}
}

func TestDispatchRequest_Permission(t *testing.T) {
	var received acp.RequestPermissionRequest
	c := &Client{
		handlers: Handlers{
			OnPermissionRequest: func(_ context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
				received = req
				return acp.PermissionSelectedOutcome(acp.PermAllowOnce), nil
			},
		},
	}

	params := []byte(`{"sessionId":"sess-1","toolCall":{"toolCallId":"tc-1"},"options":[{"optionId":"opt-1","name":"Allow","kind":"allow_once"}]}`)
	result, rpcErr := c.dispatchRequest("session/request_permission", params)

	if rpcErr != nil {
		t.Fatalf("unexpected error: %v", rpcErr)
	}
	if received.ToolCall.ToolCallID != "tc-1" {
		t.Errorf("tool call id: %q", received.ToolCall.ToolCallID)
	}
	if result == nil {
		t.Error("expected result")
	}
}

func TestDispatchRequest_PermissionDenied(t *testing.T) {
	c := &Client{
		handlers: Handlers{
			OnPermissionRequest: func(_ context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
				return acp.PermissionSelectedOutcome(acp.PermRejectOnce), nil
			},
		},
	}

	params := []byte(`{"sessionId":"sess-1","toolCall":{"toolCallId":"tc-1"}}`)
	result, rpcErr := c.dispatchRequest("session/request_permission", params)

	if rpcErr != nil {
		t.Fatalf("unexpected error: %v", rpcErr)
	}
	resp, ok := result.(acp.RequestPermissionResponse)
	if !ok {
		t.Fatalf("expected RequestPermissionResponse, got %T", result)
	}
	if resp.Outcome.Outcome != "selected" || resp.Outcome.OptionID != acp.PermRejectOnce {
		t.Errorf("outcome: %#v", resp.Outcome)
	}
}

func TestDispatchRequest_PermissionError(t *testing.T) {
	c := &Client{
		handlers: Handlers{
			OnPermissionRequest: func(_ context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
				return acp.RequestPermissionResponse{}, fmt.Errorf("handler failed")
			},
		},
	}

	params := []byte(`{"sessionId":"sess-1","toolCall":{"toolCallId":"tc-1"}}`)
	_, rpcErr := c.dispatchRequest("session/request_permission", params)

	if rpcErr == nil {
		t.Fatal("expected RPC error")
	}
	if rpcErr.Code != -32000 {
		t.Errorf("code: %d", rpcErr.Code)
	}
}

func TestDispatchRequest_NoHandler(t *testing.T) {
	c := &Client{}

	_, rpcErr := c.dispatchRequest("session/request_permission", []byte(`{"toolCall":{"toolCallId":"tc-1"}}`))
	if rpcErr == nil {
		t.Fatal("expected error")
	}
	if rpcErr.Code != -32601 {
		t.Errorf("code: %d", rpcErr.Code)
	}
}

func TestDispatchRequest_Terminal(t *testing.T) {
	terminal := &recordingTerminal{}
	c := &Client{
		handlers: Handlers{
			Terminal: terminal,
		},
	}

	result, rpcErr := c.dispatchRequest("terminal/create", []byte(`{"sessionId":"sess-1","command":"npm","args":["test"]}`))
	if rpcErr != nil {
		t.Fatalf("error: %v", rpcErr)
	}
	if terminal.created.Command != "npm" || len(terminal.created.Args) != 1 || terminal.created.Args[0] != "test" {
		t.Errorf("created request: %#v", terminal.created)
	}
	resp, ok := result.(acp.CreateTerminalResponse)
	if !ok || resp.TerminalID != "t-1" {
		t.Fatalf("result: %#v", result)
	}
}

func TestDispatchRequest_FileSystem(t *testing.T) {
	fs := &recordingFileSystem{}
	c := &Client{
		handlers: Handlers{
			FileSystem: fs,
		},
	}

	result, rpcErr := c.dispatchRequest("fs/read_text_file", []byte(`{"sessionId":"sess-1","path":"README.md"}`))
	if rpcErr != nil {
		t.Fatalf("error: %v", rpcErr)
	}
	if fs.read.Path != "README.md" {
		t.Errorf("read request: %#v", fs.read)
	}
	resp, ok := result.(acp.ReadTextFileResponse)
	if !ok || resp.Content != "contents" {
		t.Fatalf("result: %#v", result)
	}
}

func TestDispatchRequest_UnknownMethod(t *testing.T) {
	c := &Client{}

	_, rpcErr := c.dispatchRequest("unknown/method", nil)
	if rpcErr == nil {
		t.Fatal("expected error")
	}
	if rpcErr.Code != -32601 {
		t.Errorf("code: %d", rpcErr.Code)
	}
}

func TestToIDInt64(t *testing.T) {
	id, ok := toInt64(float64(42))
	if !ok || id != 42 {
		t.Errorf("float64: %d, %v", id, ok)
	}
	id, ok = toInt64(int64(100))
	if !ok || id != 100 {
		t.Errorf("int64: %d, %v", id, ok)
	}
	_, ok = toInt64("not-a-number")
	if ok {
		t.Error("string should not convert")
	}
}

func TestTrimNewline(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"hello\n", "hello"},
		{"hello\r\n", "hello"},
		{"hello", "hello"},
		{"", ""},
	}
	for _, tt := range tests {
		got := string(trimNewline([]byte(tt.in)))
		if got != tt.want {
			t.Errorf("trimNewline(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestMarshalRaw(t *testing.T) {
	data, err := marshalRaw(map[string]any{"key": "value"})
	if err != nil {
		t.Fatalf("marshalRaw: %v", err)
	}
	if len(data) == 0 {
		t.Error("expected non-empty")
	}
	var parsed map[string]any
	json.Unmarshal(data, &parsed)
	if parsed["key"] != "value" {
		t.Errorf("key: %v", parsed["key"])
	}
}

func TestMarshalRawReturnsError(t *testing.T) {
	if _, err := marshalRaw(map[string]any{"bad": func() {}}); err == nil {
		t.Fatal("expected marshal error")
	}
}

func TestClose(t *testing.T) {
	c := &Client{
		closed: make(chan struct{}),
	}
	close(c.closed)
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestCallReturnsRawResultThroughClientPath(t *testing.T) {
	stdin := &testWriteCloser{}
	c := &Client{
		stdin:   stdin,
		pending: make(map[int64]chan *jsonrpcMessage),
		closed:  make(chan struct{}),
	}

	type result struct {
		raw json.RawMessage
		err error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		raw, err := c.call(ctx, "session/new", map[string]any{"cwd": "/tmp"})
		done <- result{raw: raw, err: err}
	}()

	var ch chan *jsonrpcMessage
	deadline := time.After(time.Second)
	for ch == nil {
		c.mu.Lock()
		ch = c.pending[1]
		c.mu.Unlock()
		select {
		case <-deadline:
			t.Fatal("pending request was not registered")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	if !strings.Contains(stdin.String(), `"method":"session/new"`) {
		t.Fatalf("request was not written to stdin: %s", stdin.String())
	}
	ch <- &jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      float64(1),
		Result:  json.RawMessage(`{"sessionId":"s1"}`),
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("call returned error: %v", got.err)
		}
		var parsed struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(got.raw, &parsed); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if parsed.SessionID != "s1" {
			t.Fatalf("sessionId: got %q, want s1", parsed.SessionID)
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}
}

func TestTypedNewSessionMethod(t *testing.T) {
	stdin := &testWriteCloser{}
	c := &Client{
		stdin:   stdin,
		pending: make(map[int64]chan *jsonrpcMessage),
		closed:  make(chan struct{}),
	}

	type result struct {
		resp acp.NewSessionResponse
		err  error
	}
	done := make(chan result, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		resp, err := c.NewSession(ctx, acp.NewSessionRequest{CWD: "/tmp/project"})
		done <- result{resp: resp, err: err}
	}()

	var ch chan *jsonrpcMessage
	deadline := time.After(time.Second)
	for ch == nil {
		c.mu.Lock()
		ch = c.pending[1]
		c.mu.Unlock()
		select {
		case <-deadline:
			t.Fatal("pending request was not registered")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	written := stdin.String()
	if !strings.Contains(written, `"method":"session/new"`) || !strings.Contains(written, `"cwd":"/tmp/project"`) {
		t.Fatalf("typed request was not written correctly: %s", written)
	}
	ch <- &jsonrpcMessage{
		JSONRPC: "2.0",
		ID:      float64(1),
		Result:  json.RawMessage(`{"sessionId":"s1"}`),
	}

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("NewSession returned error: %v", got.err)
		}
		if got.resp.SessionID != "s1" {
			t.Fatalf("session id: got %q, want s1", got.resp.SessionID)
		}
	case <-ctx.Done():
		t.Fatal("timeout")
	}
}

func TestDispatchLineRespondsToIncomingStringIDRequest(t *testing.T) {
	stdin := &testWriteCloser{}
	c := &Client{
		stdin:   stdin,
		pending: make(map[int64]chan *jsonrpcMessage),
		closed:  make(chan struct{}),
		handlers: Handlers{
			OnPermissionRequest: func(_ context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
				if req.ToolCall.ToolCallID != "tc-1" {
					t.Fatalf("tool call id: got %q, want tc-1", req.ToolCall.ToolCallID)
				}
				return acp.PermissionSelectedOutcome(acp.PermAllowOnce), nil
			},
		},
	}

	c.dispatchLine([]byte(`{"jsonrpc":"2.0","id":"req-1","method":"session/request_permission","params":{"toolCall":{"toolCallId":"tc-1"}}}`))

	var line string
	deadline := time.After(time.Second)
	for strings.TrimSpace(line) == "" {
		line = stdin.String()
		select {
		case <-deadline:
			t.Fatal("response was not written")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	var resp struct {
		ID     any `json:"id"`
		Result struct {
			Outcome struct {
				Outcome  string `json:"outcome"`
				OptionID string `json:"optionId"`
			} `json:"outcome"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("unmarshal response %q: %v", line, err)
	}
	if resp.ID != "req-1" {
		t.Fatalf("id: got %#v, want req-1", resp.ID)
	}
	if resp.Result.Outcome.Outcome != "selected" || resp.Result.Outcome.OptionID != acp.PermAllowOnce {
		t.Fatalf("result: %#v", resp.Result)
	}
}

func TestDispatchLineRespondsToInvalidRequest(t *testing.T) {
	stdin := &testWriteCloser{}
	c := &Client{
		stdin:   stdin,
		pending: make(map[int64]chan *jsonrpcMessage),
		closed:  make(chan struct{}),
	}

	c.dispatchLine([]byte(`{"jsonrpc":"2.0","id":"bad-1"}`))

	var line string
	deadline := time.After(time.Second)
	for strings.TrimSpace(line) == "" {
		line = stdin.String()
		select {
		case <-deadline:
			t.Fatal("response was not written")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if !strings.Contains(line, `"id":"bad-1"`) || !strings.Contains(line, `"code":-32600`) {
		t.Fatalf("expected invalid request response, got: %s", line)
	}
}

func TestDispatchLineMalformedPendingResponseUnblocksCaller(t *testing.T) {
	stdin := &testWriteCloser{}
	c := &Client{
		stdin:   stdin,
		pending: make(map[int64]chan *jsonrpcMessage),
		closed:  make(chan struct{}),
	}

	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	go func() {
		_, err := c.call(ctx, "session/new", map[string]any{"cwd": "/tmp"})
		done <- err
	}()

	var registered bool
	deadline := time.After(time.Second)
	for !registered {
		c.mu.Lock()
		_, registered = c.pending[1]
		c.mu.Unlock()
		select {
		case <-deadline:
			t.Fatal("pending request was not registered")
		default:
			time.Sleep(time.Millisecond)
		}
	}

	c.dispatchLine([]byte(`{"jsonrpc":"2.0","id":1}`))

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "RPC error -32600") {
			t.Fatalf("expected invalid response error, got %v", err)
		}
	case <-ctx.Done():
		t.Fatal("caller timed out instead of receiving invalid response error")
	}
}

func TestDispatchRequestUsesConnectionContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := &Client{
		ctx: ctx,
		handlers: Handlers{
			OnPermissionRequest: func(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
				return acp.RequestPermissionResponse{}, ctx.Err()
			},
		},
	}

	_, rpcErr := c.dispatchRequest("session/request_permission", []byte(`{"sessionId":"sess-1","toolCall":{"toolCallId":"tc-1"}}`))
	if rpcErr == nil || !strings.Contains(rpcErr.Message, context.Canceled.Error()) {
		t.Fatalf("expected canceled callback context, got %#v", rpcErr)
	}
}

func TestReadLoopHandlesLargeSessionUpdate(t *testing.T) {
	const payloadSize = 80 * 1024
	text := strings.Repeat("x", payloadSize)
	line := fmt.Sprintf(`{"jsonrpc":"2.0","method":"session/update","params":{"sessionId":"sess-1","update":{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":%q}}}}`, text) + "\n"
	received := make(chan acp.SessionNotification, 1)
	c := &Client{
		reader:  bufio.NewReader(strings.NewReader(line)),
		stderr:  bufio.NewScanner(strings.NewReader("")),
		pending: make(map[int64]chan *jsonrpcMessage),
		closed:  make(chan struct{}),
		handlers: Handlers{
			OnUpdate: func(n acp.SessionNotification) {
				received <- n
			},
		},
	}

	c.readLoop()

	select {
	case n := <-received:
		chunk, ok := n.Update.(acp.ContentChunk)
		if !ok {
			t.Fatalf("update = %T, want ContentChunk", n.Update)
		}
		content, ok := chunk.Content.(map[string]any)
		if !ok {
			t.Fatalf("content = %T, want map", chunk.Content)
		}
		if got := content["text"]; got != text {
			t.Fatalf("large content length: got %d, want %d", len(fmt.Sprint(got)), len(text))
		}
	case <-time.After(time.Second):
		t.Fatal("large update was not delivered")
	}
}

type recordingTerminal struct {
	created acp.CreateTerminalRequest
}

func (r *recordingTerminal) CreateTerminal(_ context.Context, req acp.CreateTerminalRequest) (acp.CreateTerminalResponse, error) {
	r.created = req
	return acp.CreateTerminalResponse{TerminalID: "t-1"}, nil
}

func (r *recordingTerminal) TerminalOutput(context.Context, acp.TerminalOutputRequest) (acp.TerminalOutputResponse, error) {
	return acp.TerminalOutputResponse{}, nil
}

func (r *recordingTerminal) TerminalWaitForExit(context.Context, acp.TerminalWaitForExitRequest) (acp.TerminalWaitForExitResponse, error) {
	return acp.TerminalWaitForExitResponse{}, nil
}

func (r *recordingTerminal) TerminalKill(context.Context, acp.TerminalKillRequest) error {
	return nil
}

func (r *recordingTerminal) TerminalRelease(context.Context, acp.TerminalReleaseRequest) error {
	return nil
}

type recordingFileSystem struct {
	read    acp.ReadTextFileRequest
	written acp.WriteTextFileRequest
}

func (r *recordingFileSystem) ReadTextFile(_ context.Context, req acp.ReadTextFileRequest) (acp.ReadTextFileResponse, error) {
	r.read = req
	return acp.ReadTextFileResponse{Content: "contents"}, nil
}

func (r *recordingFileSystem) WriteTextFile(_ context.Context, req acp.WriteTextFileRequest) (acp.WriteTextFileResponse, error) {
	r.written = req
	return acp.WriteTextFileResponse{}, nil
}
