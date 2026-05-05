package client

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/acp/jsonrpc"
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
	seen := make(chan string, 3)
	go func() {
		_ = agentConn.Serve(ctx, func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
			seen <- msg.Method
			switch msg.Method {
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
				if req.SessionID != "session-1" || req.CWD != "/tmp/project" {
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

	list, err := client.ListSessions(ctx, SessionListRequest{CWD: "/tmp/project", Cursor: "cursor-1"})
	if err != nil {
		t.Fatalf("ListSessions() error = %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].SessionID != "session-1" {
		t.Fatalf("ListSessions() = %#v, want session-1", list)
	}
	if _, err := client.ResumeSession(ctx, "session-1", "/tmp/project", nil); err != nil {
		t.Fatalf("ResumeSession() error = %v", err)
	}
	if err := client.CloseSession(ctx, "session-1"); err != nil {
		t.Fatalf("CloseSession() error = %v", err)
	}
	for _, want := range []string{MethodSessionList, MethodSessionResume, MethodSessionClose} {
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
		Terminal:   recordingTerminalHandler{},
		FileSystem: recordingFileSystemHandler{},
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
		fs, ok := req.ClientCapabilities["fs"].(map[string]any)
		if !ok || fs["readTextFile"] != true || fs["writeTextFile"] != true {
			t.Fatalf("fs capability = %#v, want read/write true", req.ClientCapabilities["fs"])
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for initialize request")
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
