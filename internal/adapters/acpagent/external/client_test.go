package external

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/jsonrpc"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestClientPromptNormalizesACPUpdates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, server, closePipes := newClientServer(t, Config{AgentID: "agent-1", AgentName: "reviewer"})
	defer closePipes()

	serverErr := serveFakeAgent(ctx, server, fakeAgentBehavior{
		OnPrompt: func(ctx context.Context, conn *jsonrpc.Conn, req schema.PromptRequest) schema.PromptResponse {
			_ = conn.Notify(schema.MethodSessionUpdate, schema.SessionNotification{
				SessionID: req.SessionID,
				Update: schema.ContentChunk{
					SessionUpdate: schema.UpdateAgentThought,
					Content:       schema.TextContent{Type: "text", Text: "thinking"},
				},
			})
			_ = conn.Notify(schema.MethodSessionUpdate, schema.SessionNotification{
				SessionID: req.SessionID,
				Update: schema.ToolCall{
					SessionUpdate: schema.UpdateToolCall,
					ToolCallID:    "call-1",
					Title:         "Read file",
					Kind:          schema.ToolKindRead,
					Status:        schema.ToolStatusPending,
					RawInput:      map[string]any{"path": "a.txt"},
				},
			})
			_ = conn.Notify(schema.MethodSessionUpdate, schema.SessionNotification{
				SessionID: req.SessionID,
				Update: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo,
					ToolCallID:    "call-1",
					Status:        stringPtr(schema.ToolStatusCompleted),
					Content:       []schema.ToolCallContent{{Type: "text", Content: "file body"}},
				},
			})
			_ = conn.Notify(schema.MethodSessionUpdate, schema.SessionNotification{
				SessionID: req.SessionID,
				Update: schema.ContentChunk{
					SessionUpdate: schema.UpdateAgentMessage,
					Content:       schema.TextContent{Type: "text", Text: "done"},
				},
			})
			return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}
		},
	})

	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	newSession, err := client.NewSession(ctx, "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Prompt(ctx, newSession.SessionID, []model.ContentPart{{Type: model.ContentPartText, Text: "inspect"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want end_turn", result.StopReason)
	}
	wantTypes := []session.EventType{
		session.EventAssistant,
		session.EventToolCall,
		session.EventToolResult,
		session.EventAssistant,
	}
	if got := eventTypes(result.Events); !equalEventTypes(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	if result.Events[0].Message == nil || result.Events[0].Message.Parts[0].Reasoning == nil {
		t.Fatalf("first event = %#v, want reasoning message", result.Events[0])
	}
	if result.Events[1].Tool == nil || result.Events[1].Tool.Input["path"] != "a.txt" {
		t.Fatalf("tool call = %#v, want read file input", result.Events[1].Tool)
	}
	if got := session.EventText(result.Events[3]); got != "done" {
		t.Fatalf("assistant text = %q, want done", got)
	}

	closePipes()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("fake server did not stop")
	}
}

func TestClientHandlesExternalPermissionRequest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var permissionSeen schema.RequestPermissionRequest
	client, server, closePipes := newClientServer(t, Config{
		AgentID:   "agent-1",
		AgentName: "reviewer",
		Permission: func(_ context.Context, req schema.RequestPermissionRequest) (schema.RequestPermissionResponse, error) {
			permissionSeen = req
			return schema.RequestPermissionResponse{
				Outcome: schema.PermissionOutcome{
					Outcome:  schema.PermAllowOnce,
					OptionID: schema.PermAllowOnce,
				},
			}, nil
		},
	})
	defer closePipes()

	serverErr := serveFakeAgent(ctx, server, fakeAgentBehavior{
		OnPrompt: func(ctx context.Context, conn *jsonrpc.Conn, req schema.PromptRequest) schema.PromptResponse {
			var resp schema.RequestPermissionResponse
			err := conn.Call(ctx, schema.MethodSessionReqPermission, schema.RequestPermissionRequest{
				SessionID: req.SessionID,
				ToolCall: schema.ToolCallUpdate{
					SessionUpdate: schema.UpdateToolCallInfo,
					ToolCallID:    "call-1",
					Title:         stringPtr("Shell"),
					Kind:          stringPtr(schema.ToolKindExecute),
					Status:        stringPtr(schema.ToolStatusInProgress),
					RawInput:      map[string]any{"command": "date"},
				},
				Options: []schema.PermissionOption{{
					OptionID: schema.PermAllowOnce,
					Name:     "Allow once",
					Kind:     "allow",
				}},
			}, &resp)
			if err != nil {
				t.Errorf("permission call error = %v", err)
			}
			_ = conn.Notify(schema.MethodSessionUpdate, schema.SessionNotification{
				SessionID: req.SessionID,
				Update: schema.ContentChunk{
					SessionUpdate: schema.UpdateAgentMessage,
					Content:       schema.TextContent{Type: "text", Text: "allowed"},
				},
			})
			return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}
		},
	})

	if _, err := client.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	newSession, err := client.NewSession(ctx, "/tmp/project")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Prompt(ctx, newSession.SessionID, []model.ContentPart{{Type: model.ContentPartText, Text: "run"}})
	if err != nil {
		t.Fatal(err)
	}
	if permissionSeen.ToolCall.ToolCallID != "call-1" {
		t.Fatalf("permission seen = %#v, want call-1", permissionSeen)
	}
	wantTypes := []session.EventType{
		session.EventApproval,
		session.EventApproval,
		session.EventAssistant,
	}
	if got := eventTypes(result.Events); !equalEventTypes(got, wantTypes) {
		t.Fatalf("event types = %v, want %v", got, wantTypes)
	}
	if result.Events[0].Approval == nil || result.Events[0].Approval.Status != session.ApprovalPending {
		t.Fatalf("pending approval = %#v", result.Events[0])
	}
	if result.Events[1].Approval == nil || result.Events[1].Approval.Status != session.ApprovalApproved {
		t.Fatalf("approved approval = %#v", result.Events[1])
	}

	closePipes()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("fake server did not stop")
	}
}

type fakeAgentBehavior struct {
	OnPrompt func(context.Context, *jsonrpc.Conn, schema.PromptRequest) schema.PromptResponse
}

func newClientServer(t *testing.T, cfg Config) (*Client, *jsonrpc.Conn, func()) {
	t.Helper()
	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	client := New(serverToClientReader, clientToServerWriter, cfg)
	server := jsonrpc.New(clientToServerReader, serverToClientWriter)
	closePipes := func() {
		_ = clientToServerReader.Close()
		_ = clientToServerWriter.Close()
		_ = serverToClientReader.Close()
		_ = serverToClientWriter.Close()
	}
	return client, server, closePipes
}

func serveFakeAgent(ctx context.Context, conn *jsonrpc.Conn, behavior fakeAgentBehavior) <-chan error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- conn.Serve(ctx, func(ctx context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
			switch msg.Method {
			case schema.MethodInitialize:
				return schema.InitializeResponse{
					ProtocolVersion: schema.CurrentProtocolVersion,
					AgentCapabilities: schema.AgentCapabilities{
						PromptCapabilities: schema.PromptCapabilities{Image: true},
					},
					AgentInfo: &schema.Implementation{Name: "fake", Version: "test"},
				}, nil
			case schema.MethodSessionNew:
				return schema.NewSessionResponse{SessionID: "remote-session"}, nil
			case schema.MethodSessionPrompt:
				var req schema.PromptRequest
				if err := json.Unmarshal(msg.Params, &req); err != nil {
					return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
				}
				if behavior.OnPrompt == nil {
					return schema.PromptResponse{StopReason: schema.StopReasonEndTurn}, nil
				}
				return behavior.OnPrompt(ctx, conn, req), nil
			default:
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
		}, nil)
	}()
	return errCh
}

func eventTypes(events []session.Event) []session.EventType {
	out := make([]session.EventType, 0, len(events))
	for _, event := range events {
		if event.Type != "" {
			out = append(out, event.Type)
		}
	}
	return out
}

func equalEventTypes(a, b []session.EventType) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func stringPtr(value string) *string {
	return &value
}
