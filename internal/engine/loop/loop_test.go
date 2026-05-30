package loop

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
	"github.com/OnslaughtSnail/caelis/internal/engine/approval"
)

func TestLoopPassesConfiguredInstructionsToProvider(t *testing.T) {
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("pong")},
	}}
	runner, err := New(Config{
		Provider:     provider,
		Instructions: []string{" system rule ", "", "workspace rule"},
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := runner.Run(context.Background(), Request{
		Session: session.Session{Ref: session.Ref{SessionID: "sess-1"}},
		Input:   "ping",
		TurnID:  "turn-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want user and assistant", len(events))
	}
	if len(provider.request.Instructions) != 2 || provider.request.Instructions[0] != "system rule" || provider.request.Instructions[1] != "workspace rule" {
		t.Fatalf("instructions = %#v, want trimmed configured instructions", provider.request.Instructions)
	}
}

func TestLoopPassesReasoningConfigToProvider(t *testing.T) {
	provider := &capturingProvider{message: model.Message{
		Role:  model.RoleAssistant,
		Parts: []model.Part{model.NewTextPart("pong")},
	}}
	runner, err := New(Config{Provider: provider})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runner.Run(context.Background(), Request{
		Session:   session.Session{Ref: session.Ref{SessionID: "sess-1"}},
		Input:     "ping",
		TurnID:    "turn-1",
		Reasoning: model.ReasoningConfig{Effort: "high"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if provider.request.Reasoning.Effort != "high" {
		t.Fatalf("reasoning = %#v, want high effort", provider.request.Reasoning)
	}
}

func TestLoopPassesSessionModeToApprovalPolicy(t *testing.T) {
	rawInput := json.RawMessage(`{"content":"review"}`)
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-plan",
					Name:  "update_plan",
					Input: rawInput,
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("done")},
		},
	}}
	var capturedMode string
	runner, err := New(Config{
		Provider: provider,
		Tools:    staticRegistry{tools: []tool.Tool{fakePlanTool{name: "update_plan"}}},
		Approval: approval.PolicyFunc(func(_ context.Context, req approval.Request) (approval.Decision, error) {
			capturedMode = req.Mode
			return approval.Decision{Verdict: approval.VerdictAllow}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(context.Background(), Request{
		Session: session.Session{Ref: session.Ref{SessionID: "sess-1"}},
		Input:   "plan",
		TurnID:  "turn-1",
		Mode:    approval.ModeManual,
	}); err != nil {
		t.Fatal(err)
	}
	if capturedMode != approval.ModeManual {
		t.Fatalf("approval mode = %q, want manual", capturedMode)
	}
}

func TestLoopRecordsPlanEventFromPlanToolResult(t *testing.T) {
	const planToolName = "update_plan"
	rawPlan, err := json.Marshal(map[string]any{
		"entries": []map[string]any{
			{"content": "Read code", "status": "completed"},
			{"content": "Implement fix", "status": "in_progress"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-plan",
					Name:  planToolName,
					Input: rawPlan,
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("done")},
		},
	}}
	tools := staticRegistry{tools: []tool.Tool{fakePlanTool{name: planToolName}}}
	runner, err := New(Config{Provider: provider, Tools: tools})
	if err != nil {
		t.Fatal(err)
	}
	events, err := runner.Run(context.Background(), Request{
		Session: session.Session{Ref: session.Ref{SessionID: "sess-1"}},
		Input:   "plan",
		TurnID:  "turn-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("events = %#v, want user, assistant, tool call, tool result, plan, assistant", eventTypes(events))
	}
	if events[4].Type != session.EventPlan || len(events[4].Plan) != 2 {
		t.Fatalf("plan event = %#v, want two plan entries", events[4])
	}
	if events[4].Plan[1].Content != "Implement fix" || events[4].Plan[1].Status != "in_progress" {
		t.Fatalf("plan entries = %#v, want normalized plan", events[4].Plan)
	}
}

func TestLoopProjectsJSONToolResultOutput(t *testing.T) {
	const toolName = "write_file"
	provider := &scriptedProvider{responses: []model.Message{
		{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:    "call-write",
					Name:  toolName,
					Input: json.RawMessage(`{"path":"demo.txt","content":"hello\n"}`),
				},
			}},
		},
		{
			Role:  model.RoleAssistant,
			Parts: []model.Part{model.NewTextPart("done")},
		},
	}}
	runner, err := New(Config{
		Provider: provider,
		Tools:    staticRegistry{tools: []tool.Tool{fakeJSONTool{name: toolName}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := runner.Run(context.Background(), Request{
		Session: session.Session{Ref: session.Ref{SessionID: "sess-1"}},
		Input:   "write",
		TurnID:  "turn-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	var result *session.ToolEvent
	for idx := range events {
		if events[idx].Type == session.EventToolResult {
			result = events[idx].Tool
			break
		}
	}
	if result == nil {
		t.Fatalf("events = %#v, want tool result event", eventTypes(events))
	}
	if result.Output["path"] != "demo.txt" || result.Output["changed"] != true {
		t.Fatalf("tool output = %#v, want decoded JSON payload", result.Output)
	}
	if result.Meta["source"] != "fake-json" {
		t.Fatalf("tool meta = %#v, want tool result metadata", result.Meta)
	}
}

type capturingProvider struct {
	request model.Request
	message model.Message
}

func (p *capturingProvider) ID() string {
	return "capturing"
}

func (p *capturingProvider) Models(context.Context) ([]model.ModelInfo, error) {
	return []model.ModelInfo{{ID: "capturing", Provider: "capturing"}}, nil
}

func (p *capturingProvider) Stream(_ context.Context, req model.Request) (model.Stream, error) {
	p.request = model.Request{
		Model:        req.Model,
		Messages:     cloneTestMessages(req.Messages),
		Tools:        req.Tools,
		Instructions: append([]string(nil), req.Instructions...),
		Reasoning:    req.Reasoning,
		Stream:       req.Stream,
	}
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: model.StreamTurnDone,
		Response: &model.Response{
			Status:  model.ResponseCompleted,
			Message: model.CloneMessage(p.message),
		},
	}}}, nil
}

func cloneTestMessages(in []model.Message) []model.Message {
	if len(in) == 0 {
		return nil
	}
	out := make([]model.Message, 0, len(in))
	for _, message := range in {
		out = append(out, model.CloneMessage(message))
	}
	return out
}

type staticRegistry struct {
	tools []tool.Tool
}

func (r staticRegistry) List(context.Context) ([]tool.Tool, error) {
	return append([]tool.Tool(nil), r.tools...), nil
}

func (r staticRegistry) Lookup(_ context.Context, name string) (tool.Tool, bool, error) {
	for _, item := range r.tools {
		if item.Definition().Name == name {
			return item, true, nil
		}
	}
	return nil, false, nil
}

type fakePlanTool struct {
	name string
}

func (t fakePlanTool) Definition() tool.Definition {
	return tool.Definition{Name: t.name}
}

func (t fakePlanTool) Call(_ context.Context, call tool.Call) (tool.Result, error) {
	return tool.Result{
		ID:   call.ID,
		Name: t.name,
		Content: []model.Part{{
			Kind: model.PartJSON,
			JSON: &model.JSONPart{Value: []byte(`{"updated":true}`)},
		}},
		Meta: map[string]any{
			"plan_entries": []session.PlanEntry{
				{Content: "Read code", Status: "completed"},
				{Content: "Implement fix", Status: "in_progress"},
			},
			"explanation": "test",
		},
	}, nil
}

type fakeJSONTool struct {
	name string
}

func (t fakeJSONTool) Definition() tool.Definition {
	return tool.Definition{Name: t.name}
}

func (t fakeJSONTool) Call(_ context.Context, call tool.Call) (tool.Result, error) {
	return tool.Result{
		ID:   call.ID,
		Name: t.name,
		Content: []model.Part{{
			Kind: model.PartJSON,
			JSON: &model.JSONPart{Value: []byte(`{"path":"demo.txt","changed":true}`)},
		}},
		Meta: map[string]any{"source": "fake-json"},
	}, nil
}

type scriptedProvider struct {
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
	p.requests = append(p.requests, model.Request{
		Model:        req.Model,
		Messages:     cloneTestMessages(req.Messages),
		Tools:        append([]model.ToolSpec(nil), req.Tools...),
		Instructions: append([]string(nil), req.Instructions...),
		Reasoning:    req.Reasoning,
		Stream:       req.Stream,
	})
	if len(p.responses) == 0 {
		return &model.StaticStream{}, nil
	}
	next := p.responses[0]
	p.responses = p.responses[1:]
	return &model.StaticStream{Events: []model.StreamEvent{{
		Type: model.StreamTurnDone,
		Response: &model.Response{
			Status:  model.ResponseCompleted,
			Message: model.CloneMessage(next),
		},
	}}}, nil
}

func eventTypes(events []session.Event) []session.EventType {
	out := make([]session.EventType, 0, len(events))
	for _, event := range events {
		out = append(out, event.Type)
	}
	return out
}
