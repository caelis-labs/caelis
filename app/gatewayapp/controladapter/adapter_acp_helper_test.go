package controladapter

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	acpclient "github.com/caelis-labs/caelis/protocol/acp/client"
	"github.com/caelis-labs/caelis/protocol/acp/jsonrpc"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

func TestAdapterACPHelperProcess(t *testing.T) {
	if os.Getenv("CAELIS_ADAPTER_ACP_HELPER") != "1" {
		return
	}
	remoteSessionID := strings.TrimSpace(os.Getenv("CAELIS_ADAPTER_ACP_REMOTE_SESSION"))
	if remoteSessionID == "" {
		remoteSessionID = "adapter-helper-remote-session"
	}
	conn := jsonrpc.New(os.Stdin, os.Stdout)
	err := conn.Serve(context.Background(), func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
		switch msg.Method {
		case acpclient.MethodInitialize:
			return acpclient.InitializeResponse{
				ProtocolVersion:   1,
				AgentCapabilities: schema.AgentCapabilities{},
				AgentInfo: &acpclient.Implementation{
					Name:    "adapter-test-acp",
					Title:   "Adapter Test ACP",
					Version: "test",
				},
			}, nil
		case acpclient.MethodSessionNew:
			var req acpclient.NewSessionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if strings.TrimSpace(req.CWD) == "" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "session/new cwd is required"}
			}
			return acpclient.NewSessionResponse{SessionID: remoteSessionID}, nil
		case acpclient.MethodSessionResume:
			var req acpclient.ResumeSessionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			if strings.TrimSpace(req.SessionID) == "" {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: "session/resume id is required"}
			}
			return acpclient.ResumeSessionResponse{}, nil
		case acpclient.MethodSessionPrompt:
			return acpclient.PromptResponse{StopReason: "end_turn"}, nil
		default:
			return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
		}
	}, nil)
	if err != nil {
		t.Fatalf("helper Serve() error = %v", err)
	}
	os.Exit(0)
}

func adapterACPHelperCommandForTest(t *testing.T) string {
	t.Helper()
	command := os.Args[0]
	if filepath.IsAbs(command) {
		return command
	}
	abs, err := filepath.Abs(command)
	if err != nil {
		t.Fatalf("Abs(%q) error = %v", command, err)
	}
	return abs
}
