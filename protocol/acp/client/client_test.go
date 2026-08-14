package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestCancelSendsNotification(t *testing.T) {
	var out bytes.Buffer
	client := &Client{conn: jsonrpc.New(nil, &out)}

	if err := client.Cancel(context.Background(), "session-1"); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}

	var msg jsonrpc.Message
	if err := json.Unmarshal(out.Bytes(), &msg); err != nil {
		t.Fatalf("Unmarshal(cancel message) error = %v; payload=%q", err, out.String())
	}
	if msg.ID != nil {
		t.Fatalf("cancel message id = %#v, want notification without id", msg.ID)
	}
	if msg.Method != MethodSessionCancel {
		t.Fatalf("cancel method = %q, want %q", msg.Method, MethodSessionCancel)
	}
	var req CancelRequest
	if err := json.Unmarshal(msg.Params, &req); err != nil {
		t.Fatalf("Unmarshal(cancel params) error = %v", err)
	}
	if req.SessionID != "session-1" {
		t.Fatalf("cancel session id = %q, want session-1", req.SessionID)
	}
}

func TestDecodeUpdatePreservesUnknownRawUpdate(t *testing.T) {
	raw := json.RawMessage(`{"sessionUpdate":"vendor/custom","value":42,"nested":{"ok":true}}`)
	update, err := decodeUpdate(raw)
	if err != nil {
		t.Fatalf("decodeUpdate() error = %v", err)
	}
	typed, ok := update.(schema.RawUpdate)
	if !ok {
		t.Fatalf("update = %T, want RawUpdate", update)
	}
	if typed.SessionUpdate != "vendor/custom" {
		t.Fatalf("SessionUpdate = %q, want vendor/custom", typed.SessionUpdate)
	}
	encoded, err := json.Marshal(typed)
	if err != nil {
		t.Fatalf("Marshal(RawUpdate) error = %v", err)
	}
	var got, want map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("Unmarshal(encoded) error = %v", err)
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("Unmarshal(raw) error = %v", err)
	}
	if got["value"] != want["value"] || got["sessionUpdate"] != want["sessionUpdate"] {
		t.Fatalf("encoded raw update = %#v, want %#v", got, want)
	}
}

func TestDecodeUpdatePreservesClientLocalPayloadTypes(t *testing.T) {
	t.Run("content chunk", func(t *testing.T) {
		raw := json.RawMessage(`{"sessionUpdate":"agent_message_chunk","content":{"type":"text","text":"hello"},"messageId":"message-1"}`)
		update, err := decodeUpdate(raw)
		if err != nil {
			t.Fatal(err)
		}
		chunk, ok := update.(ContentChunk)
		if !ok {
			t.Fatalf("update = %T, want ContentChunk", update)
		}
		if chunk.MessageID != "message-1" || string(chunk.Content) != `{"type":"text","text":"hello"}` {
			t.Fatalf("ContentChunk = %#v", chunk)
		}
	})

	t.Run("available commands", func(t *testing.T) {
		raw := json.RawMessage(`{"sessionUpdate":"available_commands_update","availableCommands":[{"name":"review","description":"Review changes","input":{"hint":"scope"}}]}`)
		update, err := decodeUpdate(raw)
		if err != nil {
			t.Fatal(err)
		}
		commands, ok := update.(AvailableCommandsUpdate)
		if !ok {
			t.Fatalf("update = %T, want AvailableCommandsUpdate", update)
		}
		if len(commands.AvailableCommands) != 1 || commands.AvailableCommands[0]["name"] != "review" {
			t.Fatalf("AvailableCommandsUpdate = %#v", commands)
		}
	})

	t.Run("config options", func(t *testing.T) {
		raw := json.RawMessage(`{"sessionUpdate":"config_option_update","configOptions":[]}`)
		update, err := decodeUpdate(raw)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := update.(ConfigOptionUpdate); !ok {
			t.Fatalf("update = %T, want ConfigOptionUpdate", update)
		}
	})
}

func TestDecodeUpdateRecognizesUsageUpdate(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"sessionUpdate":"usage_update","size":200000,"used":42000,"cost":{"total":0.47,"currency":"USD"},"_meta":{"vendor":{"trace":"abc"}}}`)
	update, err := decodeUpdate(raw)
	if err != nil {
		t.Fatalf("decodeUpdate() error = %v", err)
	}
	typed, ok := update.(schema.UsageUpdate)
	if !ok {
		t.Fatalf("update = %T, want UsageUpdate", update)
	}
	if typed.Size != 200000 || typed.Used != 42000 {
		t.Fatalf("usage update = %#v, want size/used preserved", typed)
	}
	if typed.Cost == nil || typed.Cost.Total != 0.47 || typed.Cost.Currency != "USD" {
		t.Fatalf("cost = %#v, want total/currency", typed.Cost)
	}
	vendor, _ := typed.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "abc" {
		t.Fatalf("meta = %#v, want vendor trace", typed.Meta)
	}
}

func TestStableSessionLifecycleMethodsSendExpectedRequests(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientToAgentReader, clientToAgentWriter := io.Pipe()
	agentToClientReader, agentToClientWriter := io.Pipe()
	defer clientToAgentReader.Close()
	defer clientToAgentWriter.Close()
	defer agentToClientReader.Close()
	defer agentToClientWriter.Close()

	agentConn := jsonrpc.New(clientToAgentReader, agentToClientWriter)
	seen := make(chan string, 4)
	go func() {
		_ = agentConn.Serve(ctx, func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
			seen <- msg.Method
			switch msg.Method {
			case MethodSessionNew:
				var req NewSessionRequest
				if err := json.Unmarshal(msg.Params, &req); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
				}
				if req.CWD != "/tmp/project" || metautil.String(
					req.Meta,
					metautil.Root,
					metautil.Runtime,
					metautil.RuntimeSession,
					metautil.RuntimeSessionKind,
				) != metautil.RuntimeSessionKindSubagent {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/new params"}
				}
				return NewSessionResponse{SessionID: "session-new"}, nil
			case MethodSessionList:
				var req SessionListRequest
				if err := json.Unmarshal(msg.Params, &req); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
				}
				if req.CWD != "/tmp/project" || req.Cursor != "cursor-1" {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/list params"}
				}
				return SessionListResponse{Sessions: []SessionSummary{{SessionID: "session-1", CWD: "/tmp/project"}}}, nil
			case MethodSessionResume:
				var req ResumeSessionRequest
				if err := json.Unmarshal(msg.Params, &req); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
				}
				if req.SessionID != "session-1" || req.CWD != "/tmp/project" || metautil.String(
					req.Meta,
					metautil.Root,
					metautil.Runtime,
					metautil.RuntimeSession,
					metautil.RuntimeTaskID,
				) != "task-resume" {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/resume params"}
				}
				return ResumeSessionResponse{}, nil
			case MethodSessionClose:
				var req CloseSessionRequest
				if err := json.Unmarshal(msg.Params, &req); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
				}
				if req.SessionID != "session-1" {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected session/close params"}
				}
				return CloseSessionResponse{}, nil
			default:
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
		}, nil)
	}()

	client := &Client{conn: jsonrpc.New(agentToClientReader, clientToAgentWriter)}
	go func() {
		_ = client.conn.Serve(ctx, client.handleRequest, client.handleNotification)
	}()

	meta := metautil.WithCompactRuntimeSection(nil, metautil.RuntimeSession, map[string]any{
		metautil.RuntimeSessionKind: metautil.RuntimeSessionKindSubagent,
	})
	created, err := client.NewSession(ctx, "/tmp/project", meta)
	if err != nil {
		t.Fatalf("NewSession() error = %v", err)
	}
	if created.SessionID != "session-new" {
		t.Fatalf("NewSession() = %#v, want session-new", created)
	}
	list, err := client.ListSessions(ctx, SessionListRequest{CWD: "/tmp/project", Cursor: "cursor-1"})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != "session-1" {
		t.Fatalf("ListSessions() = %#v, want session-1", list)
	}
	resumeMeta := metautil.WithCompactRuntimeSection(nil, metautil.RuntimeSession, map[string]any{
		metautil.RuntimeTaskID: "task-resume",
	})
	if _, err := client.ResumeSession(ctx, "session-1", "/tmp/project", resumeMeta); err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if err := client.CloseSession(ctx, "session-1"); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
	for _, want := range []string{MethodSessionNew, MethodSessionList, MethodSessionResume, MethodSessionClose} {
		select {
		case got := <-seen:
			if got != want {
				t.Fatalf("method = %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatalf("timed out waiting for %s", want)
		}
	}
}

func TestInitializeAdvertisesClientCapabilitiesFromHandlers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientToAgentReader, clientToAgentWriter := io.Pipe()
	agentToClientReader, agentToClientWriter := io.Pipe()
	defer clientToAgentReader.Close()
	defer clientToAgentWriter.Close()
	defer agentToClientReader.Close()
	defer agentToClientWriter.Close()

	requests := make(chan InitializeRequest, 1)
	agentConn := jsonrpc.New(clientToAgentReader, agentToClientWriter)
	go func() {
		_ = agentConn.Serve(ctx, func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
			if msg.Method != MethodInitialize {
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
			var req InitializeRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			requests <- req
			return InitializeResponse{}, nil
		}, nil)
	}()

	client := &Client{conn: jsonrpc.New(agentToClientReader, clientToAgentWriter), cfg: Config{
		Terminal:     recordingTerminalHandler{},
		TerminalAuth: true,
		FileSystem:   recordingFileSystemHandler{},
		OnSessionMessage: func(context.Context, SessionMessageRequest) (SessionMessageResponse, error) {
			return SessionMessageResponse{Accepted: true}, nil
		},
	}}
	go func() {
		_ = client.conn.Serve(ctx, client.handleRequest, client.handleNotification)
	}()
	if _, err := client.Initialize(ctx); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	select {
	case req := <-requests:
		if terminal, ok := req.ClientCapabilities["terminal"].(bool); !ok || !terminal {
			t.Fatalf("terminal capability = %#v, want true", req.ClientCapabilities["terminal"])
		}
		meta, ok := req.ClientCapabilities["_meta"].(map[string]any)
		if !ok || meta[metautil.TerminalOutputKey] != true {
			t.Fatalf("terminal output extension capability = %#v, want true", meta)
		}
		auth, ok := req.ClientCapabilities["auth"].(map[string]any)
		if !ok || auth["terminal"] != true {
			t.Fatalf("auth capability = %#v, want terminal true", req.ClientCapabilities["auth"])
		}
		fs, ok := req.ClientCapabilities["fs"].(map[string]any)
		if !ok || fs["readTextFile"] != true || fs["writeTextFile"] != true {
			t.Fatalf("fs capability = %#v, want read/write true", req.ClientCapabilities["fs"])
		}
		if _, ok := req.ClientCapabilities[MethodSessionMessage]; !ok {
			t.Fatalf("message capability missing: %#v", req.ClientCapabilities)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for initialize request")
	}
}

func TestSessionUpdateNotificationConsumesTerminalOutputCompatibilityAlias(t *testing.T) {
	t.Parallel()

	raw := json.RawMessage(`{"sessionUpdate":"tool_call_update","toolCallId":"command-1","_meta":{"terminal_output_delta":{"terminal_id":"command-1","data":"compatibility output\n"}}}`)
	params, err := json.Marshal(SessionNotification{SessionID: "remote-1", Update: raw})
	if err != nil {
		t.Fatal(err)
	}
	updates := make(chan UpdateEnvelope, 1)
	client := &Client{cfg: Config{OnUpdate: func(env UpdateEnvelope) { updates <- env }}}
	client.handleNotification(context.Background(), jsonrpc.Message{
		Method: MethodSessionUpdate,
		Params: params,
	})

	var env UpdateEnvelope
	select {
	case env = <-updates:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for normalized session update")
	}
	toolUpdate, ok := env.Update.(ToolCallUpdate)
	if !ok {
		t.Fatalf("session update = %T, want ToolCallUpdate", env.Update)
	}
	output, ok := metautil.TerminalOutput(toolUpdate.Meta)
	if !ok || output.TerminalID != "command-1" || output.Data != "compatibility output\n" {
		t.Fatalf("terminal output = %#v, %v; want normalized compatibility output", output, ok)
	}
	if _, ok := toolUpdate.Meta[metautil.TerminalOutputDeltaKey]; ok {
		t.Fatalf("normalized update retained compatibility alias: %#v", toolUpdate.Meta)
	}
}

func TestAuthenticateSendsDeclaredMethodID(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	clientToAgentReader, clientToAgentWriter := io.Pipe()
	agentToClientReader, agentToClientWriter := io.Pipe()
	defer clientToAgentReader.Close()
	defer clientToAgentWriter.Close()
	defer agentToClientReader.Close()
	defer agentToClientWriter.Close()

	agentConn := jsonrpc.New(clientToAgentReader, agentToClientWriter)
	go func() {
		_ = agentConn.Serve(ctx, func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
			if msg.Method != MethodAuthenticate {
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
			var req AuthenticateRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if req.MethodID != "agent-login" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "unexpected method id"}
			}
			return AuthenticateResponse{}, nil
		}, nil)
	}()

	acpClient := &Client{conn: jsonrpc.New(agentToClientReader, clientToAgentWriter)}
	go func() {
		_ = acpClient.conn.Serve(ctx, acpClient.handleRequest, acpClient.handleNotification)
	}()
	if err := acpClient.Authenticate(ctx, " agent-login "); err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
}

func TestClientHandlesTerminalAndFileSystemRequests(t *testing.T) {
	client := &Client{cfg: Config{
		Terminal:   recordingTerminalHandler{},
		FileSystem: recordingFileSystemHandler{},
	}}

	result, rpcErr := client.handleRequest(context.Background(), jsonrpc.Message{
		Method: MethodTerminalCreate,
		Params: jsonrpc.MustMarshalRaw(CreateTerminalRequest{
			SessionID: "session-1",
			Command:   "go",
			Args:      []string{"test"},
		}),
	})
	if rpcErr != nil {
		t.Fatalf("terminal/create rpc error = %v", rpcErr)
	}
	if resp, ok := result.(CreateTerminalResponse); !ok || resp.TerminalID != "term-1" {
		t.Fatalf("terminal/create result = %#v, want term-1", result)
	}

	result, rpcErr = client.handleRequest(context.Background(), jsonrpc.Message{
		Method: MethodReadTextFile,
		Params: jsonrpc.MustMarshalRaw(ReadTextFileRequest{
			SessionID: "session-1",
			Path:      "/tmp/file.txt",
		}),
	})
	if rpcErr != nil {
		t.Fatalf("fs/read_text_file rpc error = %v", rpcErr)
	}
	if resp, ok := result.(ReadTextFileResponse); !ok || resp.Content != "file contents" {
		t.Fatalf("fs/read_text_file result = %#v, want file contents", result)
	}
}

func TestClientDoesNotHandleTerminalWithoutHandler(t *testing.T) {
	client := &Client{}
	_, rpcErr := client.handleRequest(context.Background(), jsonrpc.Message{
		Method: MethodTerminalCreate,
		Params: jsonrpc.MustMarshalRaw(CreateTerminalRequest{
			SessionID: "session-1",
			Command:   "go",
		}),
	})
	if rpcErr == nil || rpcErr.Code != -32601 {
		t.Fatalf("terminal/create rpc error = %#v, want method not found", rpcErr)
	}
}

type recordingTerminalHandler struct{}

func (recordingTerminalHandler) CreateTerminal(context.Context, CreateTerminalRequest) (CreateTerminalResponse, error) {
	return CreateTerminalResponse{TerminalID: "term-1"}, nil
}

func (recordingTerminalHandler) TerminalOutput(context.Context, TerminalOutputRequest) (TerminalOutputResponse, error) {
	return TerminalOutputResponse{Output: "ok\n"}, nil
}

func (recordingTerminalHandler) TerminalWaitForExit(context.Context, WaitForTerminalExitRequest) (WaitForTerminalExitResponse, error) {
	return WaitForTerminalExitResponse{}, nil
}

func (recordingTerminalHandler) TerminalKill(context.Context, KillTerminalRequest) error {
	return nil
}

func (recordingTerminalHandler) TerminalRelease(context.Context, ReleaseTerminalRequest) error {
	return nil
}

type recordingFileSystemHandler struct{}

func (recordingFileSystemHandler) ReadTextFile(context.Context, ReadTextFileRequest) (ReadTextFileResponse, error) {
	return ReadTextFileResponse{Content: "file contents"}, nil
}

func (recordingFileSystemHandler) WriteTextFile(context.Context, WriteTextFileRequest) (WriteTextFileResponse, error) {
	return WriteTextFileResponse{}, nil
}
