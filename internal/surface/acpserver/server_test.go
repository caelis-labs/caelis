package acpserver

import (
	"context"
	"encoding/json"
	"io"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/config"
	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
	"github.com/OnslaughtSnail/caelis/internal/app/local"
	"github.com/OnslaughtSnail/caelis/internal/engine/approval"
	"github.com/OnslaughtSnail/caelis/protocol/acp/jsonrpc"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestServeStdioRunsPromptThroughCoreEngine(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stack, err := local.New(local.Config{
		Runtime: config.Runtime{
			AppName: "caelis",
			UserID:  "tester",
		},
		Provider: &testProvider{message: model.Message{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("pong")},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := stack.Services().Engine()

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, Config{
			Engine:  engine,
			AppName: "caelis",
			UserID:  "tester",
			Implementation: schema.Implementation{
				Name:    "caelis",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	updates := make(chan updateNotification, 8)
	go func() {
		_ = conn.Serve(ctx, nil, func(_ context.Context, msg jsonrpc.Message) {
			if msg.Method != schema.MethodSessionUpdate {
				return
			}
			var notification updateNotification
			if err := json.Unmarshal(msg.Params, &notification); err == nil {
				updates <- notification
			}
		})
	}()

	var initResp schema.InitializeResponse
	if err := conn.Call(ctx, schema.MethodInitialize, schema.InitializeRequest{
		ProtocolVersion:    schema.CurrentProtocolVersion,
		ClientCapabilities: map[string]any{},
	}, &initResp); err != nil {
		t.Fatalf("initialize call error = %v", err)
	}
	if initResp.ProtocolVersion != schema.CurrentProtocolVersion {
		t.Fatalf("protocol version = %d, want %d", initResp.ProtocolVersion, schema.CurrentProtocolVersion)
	}

	var newResp schema.NewSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: "/tmp/project"}, &newResp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}
	if newResp.SessionID == "" {
		t.Fatal("session/new returned empty session id")
	}

	var promptResp schema.PromptResponse
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "ping"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt call error = %v", err)
	}
	if promptResp.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want %q", promptResp.StopReason, schema.StopReasonEndTurn)
	}

	kinds := make([]string, 0, 2)
	for len(kinds) < 2 {
		select {
		case update := <-updates:
			if update.SessionID != newResp.SessionID {
				t.Fatalf("update session id = %q, want %q", update.SessionID, newResp.SessionID)
			}
			kinds = append(kinds, update.Update.SessionUpdate)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for updates, got %v", kinds)
		}
	}
	if kinds[0] != schema.UpdateUserMessage || kinds[1] != schema.UpdateAgentMessage {
		t.Fatalf("update kinds = %v, want user then agent message", kinds)
	}

	snapshot, err := stack.Services().Sessions().Load(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 2 {
		t.Fatalf("stored events = %d, want 2", len(snapshot.Events))
	}
	if got := session.EventText(snapshot.Events[1]); got != "pong" {
		t.Fatalf("stored assistant text = %q, want pong", got)
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

func TestServeStdioBridgesPermissionResponseIntoTurnSubmission(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-1",
					Name:  "ECHO",
					Input: json.RawMessage(`{"text":"hello"}`),
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("done")},
		},
	}}
	stack, err := local.New(local.Config{
		Runtime:  config.Runtime{AppName: "caelis", UserID: "tester"},
		Provider: provider,
		ToolList: []tool.Tool{tool.NamedTool{
			Def: tool.Definition{Name: "ECHO"},
			Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
				return tool.Result{
					ID:      call.ID,
					Name:    call.Name,
					Content: []model.Part{model.NewTextPart("approved output")},
				}, nil
			},
		}},
		Approval: approval.AskAll(),
	})
	if err != nil {
		t.Fatal(err)
	}

	clientToServerReader, clientToServerWriter := io.Pipe()
	serverToClientReader, serverToClientWriter := io.Pipe()
	defer clientToServerReader.Close()
	defer clientToServerWriter.Close()
	defer serverToClientReader.Close()
	defer serverToClientWriter.Close()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- ServeStdio(ctx, Config{
			Engine:  stack.Services().Engine(),
			AppName: "caelis",
			UserID:  "tester",
			Implementation: schema.Implementation{
				Name:    "caelis",
				Version: "test",
			},
		}, clientToServerReader, serverToClientWriter)
	}()

	conn := jsonrpc.New(serverToClientReader, clientToServerWriter)
	updates := make(chan updateNotification, 16)
	permissions := make(chan schema.RequestPermissionRequest, 2)
	go func() {
		_ = conn.Serve(ctx, func(_ context.Context, msg jsonrpc.Message) (any, *jsonrpc.RPCError) {
			if msg.Method != schema.MethodSessionReqPermission {
				return nil, &jsonrpc.RPCError{Code: -32601, Message: "method not found"}
			}
			var req schema.RequestPermissionRequest
			if err := json.Unmarshal(msg.Params, &req); err != nil {
				return nil, &jsonrpc.RPCError{Code: -32602, Message: err.Error()}
			}
			permissions <- req
			return schema.RequestPermissionResponse{
				Outcome: schema.PermissionOutcome{
					Outcome:  schema.PermAllowOnce,
					OptionID: schema.PermAllowOnce,
				},
			}, nil
		}, func(_ context.Context, msg jsonrpc.Message) {
			if msg.Method != schema.MethodSessionUpdate {
				return
			}
			var notification updateNotification
			if err := json.Unmarshal(msg.Params, &notification); err == nil {
				updates <- notification
			}
		})
	}()

	var initResp schema.InitializeResponse
	if err := conn.Call(ctx, schema.MethodInitialize, schema.InitializeRequest{
		ProtocolVersion:    schema.CurrentProtocolVersion,
		ClientCapabilities: map[string]any{},
	}, &initResp); err != nil {
		t.Fatalf("initialize call error = %v", err)
	}
	var newResp schema.NewSessionResponse
	if err := conn.Call(ctx, schema.MethodSessionNew, schema.NewSessionRequest{CWD: "/tmp/project"}, &newResp); err != nil {
		t.Fatalf("session/new call error = %v", err)
	}
	var promptResp schema.PromptResponse
	if err := conn.Call(ctx, schema.MethodSessionPrompt, schema.PromptRequest{
		SessionID: newResp.SessionID,
		Prompt: []json.RawMessage{
			jsonrpc.MustMarshalRaw(schema.TextContent{Type: "text", Text: "run echo"}),
		},
	}, &promptResp); err != nil {
		t.Fatalf("session/prompt call error = %v", err)
	}
	if promptResp.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("stop reason = %q, want %q", promptResp.StopReason, schema.StopReasonEndTurn)
	}
	select {
	case req := <-permissions:
		if req.SessionID != newResp.SessionID || req.ToolCall.ToolCallID != "call-1" {
			t.Fatalf("permission request = %#v, want call-1 for session", req)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for permission request")
	}

	var kinds []string
	for len(kinds) < 4 {
		select {
		case update := <-updates:
			kinds = append(kinds, update.Update.SessionUpdate)
		case <-ctx.Done():
			t.Fatalf("timed out waiting for updates, got %v", kinds)
		}
	}
	want := []string{
		schema.UpdateUserMessage,
		schema.UpdateToolCall,
		schema.UpdateToolCallInfo,
		schema.UpdateAgentMessage,
	}
	if len(kinds) < len(want) {
		t.Fatalf("update kinds = %v, want at least %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("update kinds = %v, want prefix %v", kinds, want)
		}
	}

	snapshot, err := stack.Services().Sessions().Load(ctx, session.Ref{SessionID: newResp.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 7 {
		t.Fatalf("stored events = %d, want approval-aware tool flow", len(snapshot.Events))
	}
	if snapshot.Events[3].Approval == nil || snapshot.Events[3].Approval.Status != session.ApprovalPending {
		t.Fatalf("stored pending approval = %#v", snapshot.Events[3])
	}
	if snapshot.Events[4].Approval == nil || snapshot.Events[4].Approval.Status != session.ApprovalApproved {
		t.Fatalf("stored approved approval = %#v", snapshot.Events[4])
	}

	cancel()
	_ = clientToServerWriter.Close()
	_ = clientToServerReader.Close()
	_ = serverToClientWriter.Close()
	_ = serverToClientReader.Close()
	select {
	case <-serverErr:
	case <-time.After(time.Second):
		t.Fatal("server did not stop")
	}
}

type updateNotification struct {
	SessionID string `json:"sessionId"`
	Update    struct {
		SessionUpdate string `json:"sessionUpdate"`
	} `json:"update"`
}

type testProvider struct {
	message model.Message
}

func (p *testProvider) ID() string {
	return "test"
}

func (p *testProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "test", Provider: "test"}}, nil
}

func (p *testProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: schemaStopEvent(),
		Response: &model.Response{
			Status:  model.ResponseCompleted,
			Message: model.CloneMessage(p.message),
		},
	}}}, nil
}

func schemaStopEvent() model.StreamEventType {
	return model.StreamTurnDone
}

type scriptedProvider struct {
	responses []model.Message
}

func (p *scriptedProvider) ID() string {
	return "scripted"
}

func (p *scriptedProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "scripted", Provider: "scripted"}}, nil
}

func (p *scriptedProvider) Stream(context.Context, model.Request) (model.Stream, error) {
	response := model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("default")},
	}
	if len(p.responses) > 0 {
		response = model.CloneMessage(p.responses[0])
		p.responses = p.responses[1:]
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: model.StreamTurnDone,
		Response: &model.Response{
			Status:  model.ResponseCompleted,
			Message: response,
		},
	}}}, nil
}
