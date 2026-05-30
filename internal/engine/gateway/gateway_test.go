package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
	"github.com/OnslaughtSnail/caelis/internal/engine/approval"
	"github.com/OnslaughtSnail/caelis/internal/engine/internal/teststore"
	"github.com/OnslaughtSnail/caelis/internal/engine/loop"
)

func TestGatewayBeginTurnPersistsAndReplaysCanonicalEvents(t *testing.T) {
	ctx := context.Background()
	store := teststore.New()
	provider := &scriptedProvider{
		responses: []model.Message{{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("pong")},
		}},
	}
	runner, err := loop.New(loop.Config{Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := New(Config{Store: store, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}

	active, err := gateway.StartSession(ctx, session.StartRequest{
		AppName: "caelis",
		UserID:  "tester",
		Workspace: session.Workspace{
			Key: "workspace",
			CWD: "/tmp/workspace",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := gateway.BeginTurn(ctx, coreruntime.TurnRequest{
		SessionRef: active.Ref,
		Input:      "ping",
		Surface:    "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	live := collectTurnEvents(t, turn)
	if got, want := eventTypes(live), []session.EventType{session.EventUser, session.EventAssistant}; !equalEventTypes(got, want) {
		t.Fatalf("live event types = %v, want %v", got, want)
	}
	if got := session.EventText(live[1]); got != "pong" {
		t.Fatalf("assistant text = %q, want pong", got)
	}

	snapshot, err := gateway.LoadSession(ctx, active.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(snapshot.Events); got != 2 {
		t.Fatalf("snapshot events = %d, want 2", got)
	}
	if snapshot.Events[0].Message == nil || snapshot.Events[0].Message.Role != model.RoleUser {
		t.Fatalf("first event is not canonical user message: %#v", snapshot.Events[0])
	}

	replay, err := gateway.Replay(ctx, coreruntime.ReplayRequest{SessionRef: active.Ref})
	if err != nil {
		t.Fatal(err)
	}
	replayed := collectReplayEvents(t, replay)
	if got, want := eventTypes(replayed), []session.EventType{session.EventUser, session.EventAssistant}; !equalEventTypes(got, want) {
		t.Fatalf("replay event types = %v, want %v", got, want)
	}

	requests := provider.Requests()
	if len(requests) != 1 {
		t.Fatalf("model requests = %d, want 1", len(requests))
	}
	if got := requests[0].Messages[len(requests[0].Messages)-1].TextContent(); got != "ping" {
		t.Fatalf("model saw user text = %q, want ping", got)
	}
}

func TestGatewayToolLoopPersistsToolAnchorsAndFinalAssistant(t *testing.T) {
	ctx := context.Background()
	store := teststore.New()
	provider := &scriptedProvider{
		responses: []model.Message{
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
		},
	}
	tools := staticTools{tool.NamedTool{
		Def: tool.Definition{Name: "ECHO"},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			var args struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(call.Input, &args); err != nil {
				return tool.Result{}, err
			}
			return tool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []model.Part{model.NewTextPart(strings.ToUpper(args.Text))},
			}, nil
		},
	}}
	runner, err := loop.New(loop.Config{Provider: provider, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := New(Config{Store: store, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	active, err := gateway.StartSession(ctx, session.StartRequest{AppName: "caelis", UserID: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := gateway.BeginTurn(ctx, coreruntime.TurnRequest{SessionRef: active.Ref, Input: "run echo"})
	if err != nil {
		t.Fatal(err)
	}
	live := collectTurnEvents(t, turn)
	want := []session.EventType{
		session.EventUser,
		session.EventAssistant,
		session.EventToolCall,
		session.EventToolResult,
		session.EventAssistant,
	}
	if got := eventTypes(live); !equalEventTypes(got, want) {
		t.Fatalf("live event types = %v, want %v", got, want)
	}
	if got := session.EventText(live[3]); got != "HELLO" {
		t.Fatalf("tool result text = %q, want HELLO", got)
	}
	if got := session.EventText(live[4]); got != "done" {
		t.Fatalf("final assistant text = %q, want done", got)
	}

	requests := provider.Requests()
	if len(requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(requests))
	}
	second := requests[1].Messages
	if second[len(second)-1].Role != model.RoleTool {
		t.Fatalf("last second-step message role = %q, want tool", second[len(second)-1].Role)
	}
}

func TestGatewayApprovalSubmissionResumesToolExecution(t *testing.T) {
	ctx := context.Background()
	store := teststore.New()
	provider := &scriptedProvider{
		responses: []model.Message{
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
		},
	}
	tools := staticTools{tool.NamedTool{
		Def: tool.Definition{Name: "ECHO"},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ID:      call.ID,
				Name:    call.Name,
				Content: []model.Part{model.NewTextPart("approved output")},
			}, nil
		},
	}}
	runner, err := loop.New(loop.Config{
		Provider: provider,
		Tools:    tools,
		Approval: approval.AskTools("echo"),
	})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := New(Config{Store: store, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	active, err := gateway.StartSession(ctx, session.StartRequest{AppName: "caelis", UserID: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := gateway.BeginTurn(ctx, coreruntime.TurnRequest{SessionRef: active.Ref, Input: "run echo"})
	if err != nil {
		t.Fatal(err)
	}
	live := collectTurnEventsWithApproval(t, turn, true)
	want := []session.EventType{
		session.EventUser,
		session.EventAssistant,
		session.EventToolCall,
		session.EventApproval,
		session.EventApproval,
		session.EventToolResult,
		session.EventAssistant,
	}
	if got := eventTypes(live); !equalEventTypes(got, want) {
		t.Fatalf("live event types = %v, want %v", got, want)
	}
	if live[3].Approval == nil || live[3].Approval.Status != session.ApprovalPending {
		t.Fatalf("pending approval event = %#v", live[3])
	}
	if live[4].Approval == nil || live[4].Approval.Status != session.ApprovalApproved {
		t.Fatalf("approved event = %#v", live[4])
	}
	if got := session.EventText(live[5]); got != "approved output" {
		t.Fatalf("tool result text = %q, want approved output", got)
	}

	snapshot, err := gateway.LoadSession(ctx, active.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if got := eventTypes(snapshot.Events); !equalEventTypes(got, want) {
		t.Fatalf("stored event types = %v, want %v", got, want)
	}
}

func TestGatewayRejectedApprovalReturnsToolError(t *testing.T) {
	ctx := context.Background()
	store := teststore.New()
	provider := &scriptedProvider{
		responses: []model.Message{
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
				Parts: []model.Part{model.NewTextPart("stopped")},
			},
		},
	}
	called := false
	tools := staticTools{tool.NamedTool{
		Def: tool.Definition{Name: "ECHO"},
		Invoke: func(context.Context, tool.Call) (tool.Result, error) {
			called = true
			return tool.Result{}, nil
		},
	}}
	runner, err := loop.New(loop.Config{
		Provider: provider,
		Tools:    tools,
		Approval: approval.AskAll(),
	})
	if err != nil {
		t.Fatal(err)
	}
	gateway, err := New(Config{Store: store, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	active, err := gateway.StartSession(ctx, session.StartRequest{AppName: "caelis", UserID: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := gateway.BeginTurn(ctx, coreruntime.TurnRequest{SessionRef: active.Ref, Input: "run echo"})
	if err != nil {
		t.Fatal(err)
	}
	live := collectTurnEventsWithApproval(t, turn, false)
	if called {
		t.Fatal("tool was executed after rejected approval")
	}
	if live[4].Approval == nil || live[4].Approval.Status != session.ApprovalRejected {
		t.Fatalf("rejected event = %#v", live[4])
	}
	if got := session.EventText(live[5]); !strings.Contains(got, "reject") {
		t.Fatalf("tool result text = %q, want rejection text", got)
	}
}

type scriptedProvider struct {
	mu        sync.Mutex
	requests  []model.Request
	responses []model.Message
}

func (p *scriptedProvider) ID() string {
	return "scripted"
}

func (p *scriptedProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "scripted", Provider: "scripted"}}, nil
}

func (p *scriptedProvider) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.requests = append(p.requests, cloneModelRequest(req))
	if len(p.responses) == 0 {
		return &model.StaticStream{Events: []model.StreamEvent{{
			Type: model.StreamTurnDone,
			Response: &model.Response{
				Status: model.ResponseCompleted,
				Message: model.Message{
					Role:  model.RoleAssistant,
					Parts: []model.Part{model.NewTextPart("default")},
				},
			},
		}}}, nil
	}
	response := model.CloneMessage(p.responses[0])
	p.responses = p.responses[1:]
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: model.StreamTurnDone,
		Response: &model.Response{
			Status:  model.ResponseCompleted,
			Message: response,
		},
	}}}, nil
}

func (p *scriptedProvider) Requests() []model.Request {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]model.Request, 0, len(p.requests))
	for _, req := range p.requests {
		out = append(out, cloneModelRequest(req))
	}
	return out
}

type staticTools []tool.Tool

func (s staticTools) List(context.Context) ([]tool.Tool, error) {
	return append([]tool.Tool(nil), s...), nil
}

func (s staticTools) Lookup(_ context.Context, name string) (tool.Tool, bool, error) {
	for _, item := range s {
		if item != nil && strings.EqualFold(item.Definition().Name, name) {
			return item, true, nil
		}
	}
	return nil, false, nil
}

func collectTurnEvents(t *testing.T, turn coreruntime.Turn) []session.Event {
	t.Helper()
	var out []session.Event
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatalf("turn event error: %s", env.Err)
		}
		out = append(out, session.CloneEvent(env.Event))
	}
	return out
}

func collectTurnEventsWithApproval(t *testing.T, turn coreruntime.Turn, approved bool) []session.Event {
	t.Helper()
	var out []session.Event
	for env := range turn.Events() {
		if env.Err != "" {
			t.Fatalf("turn event error: %s", env.Err)
		}
		event := session.CloneEvent(env.Event)
		out = append(out, event)
		if event.Approval != nil && event.Approval.Status == session.ApprovalPending {
			outcome := approval.OptionRejectOnce
			if approved {
				outcome = approval.OptionAllowOnce
			}
			if err := turn.Submit(context.Background(), coreruntime.Submission{
				Kind: coreruntime.SubmissionApproval,
				Approval: &coreruntime.ApprovalDecision{
					Outcome:  outcome,
					OptionID: outcome,
					Approved: approved,
					Reason:   outcome,
				},
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	return out
}

func collectReplayEvents(t *testing.T, replay <-chan coreruntime.EventEnvelope) []session.Event {
	t.Helper()
	var out []session.Event
	for env := range replay {
		if env.Err != "" {
			t.Fatalf("replay error: %s", env.Err)
		}
		out = append(out, session.CloneEvent(env.Event))
	}
	return out
}

func eventTypes(events []session.Event) []session.EventType {
	out := make([]session.EventType, 0, len(events))
	for _, event := range events {
		out = append(out, event.Type)
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

func cloneModelRequest(in model.Request) model.Request {
	out := in
	out.Messages = make([]model.Message, 0, len(in.Messages))
	for _, message := range in.Messages {
		out.Messages = append(out.Messages, model.CloneMessage(message))
	}
	out.Tools = append([]model.ToolSpec(nil), in.Tools...)
	return out
}
