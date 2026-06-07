package llmagent

import (
	"context"
	"fmt"
	"iter"
	"testing"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/tool"
)

// ─── Mock LLM ────────────────────────────────────────────────────────

type scriptedLLM struct {
	responses []scriptedResponse
	callCount int
	requests  []model.Request
}

type scriptedResponse struct {
	text      string
	toolCalls []model.ToolCallDelta
	err       error
}

func (m *scriptedLLM) Name() string { return "scripted" }

func (m *scriptedLLM) Generate(_ context.Context, req model.Request) iter.Seq2[model.ResponseEvent, error] {
	m.requests = append(m.requests, req)
	return func(yield func(model.ResponseEvent, error) bool) {
		if m.callCount >= len(m.responses) {
			return
		}
		resp := m.responses[m.callCount]
		m.callCount++
		if resp.err != nil {
			yield(model.ResponseEvent{}, resp.err)
			return
		}
		if resp.text != "" {
			yield(model.ResponseEvent{TextDelta: resp.text}, nil)
		}
		for _, tc := range resp.toolCalls {
			yield(model.ResponseEvent{ToolCall: &tc}, nil)
		}
		yield(model.ResponseEvent{FinishReason: "stop"}, nil)
	}
}

// ─── Mock Tool ────────────────────────────────────────────────────────

type echoTool struct{}

func (t *echoTool) Definition() tool.Definition {
	return tool.Definition{Name: "ECHO", Schema: tool.Schema{Type: "object"}}
}

func (t *echoTool) Run(_ tool.Context, call tool.Call) (tool.Result, error) {
	text, _ := call.Args["text"].(string)
	return tool.Result{Output: "echo:" + text}, nil
}

// ─── Mock InvocationContext ───────────────────────────────────────────

type mockInvCtx struct {
	context.Context
	priorMessages []model.Message
	userMessage   model.Message
	runConfig     *agent.RunConfig
	ended         bool
}

func (c *mockInvCtx) Agent() agent.Agent             { return nil }
func (c *mockInvCtx) Session() session.Session       { return session.Session{} }
func (c *mockInvCtx) InvocationID() string           { return "test-inv" }
func (c *mockInvCtx) Branch() string                 { return "" }
func (c *mockInvCtx) UserMessage() model.Message     { return c.userMessage }
func (c *mockInvCtx) PriorMessages() []model.Message { return c.priorMessages }
func (c *mockInvCtx) RunConfig() *agent.RunConfig {
	if c.runConfig != nil {
		return c.runConfig
	}
	return agent.DefaultRunConfig()
}
func (c *mockInvCtx) EndInvocation() { c.ended = true }
func (c *mockInvCtx) Ended() bool    { return c.ended }

// ─── Helpers ──────────────────────────────────────────────────────────

// prepare creates a new agent and prepares it with the given request.
func prepare(cfg Config, req agent.PrepareRequest) agent.Agent {
	a := New(cfg)
	return a.Prepare(req)
}

func userMsg(text string) model.Message {
	return model.Message{Role: model.RoleUser, Content: []model.Part{{Text: text}}}
}

func newInvCtx(msg model.Message) *mockInvCtx {
	return &mockInvCtx{Context: context.Background(), userMessage: msg}
}

// ─── Tests ────────────────────────────────────────────────────────────

func TestAgentNilLLM(t *testing.T) {
	a := New(Config{Name: "test", ModelRef: model.Ref{ModelID: "m"}})
	ctx := newInvCtx(userMsg("hi"))

	var gotErr bool
	for _, err := range a.Run(ctx) {
		if err != nil {
			gotErr = true
		}
	}
	if !gotErr {
		t.Error("expected error for nil LLM")
	}
}

func TestAgentTextResponse(t *testing.T) {
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM: &scriptedLLM{responses: []scriptedResponse{{text: "hello world"}}},
	})

	var events []session.Event
	for evt, err := range a.Run(newInvCtx(userMsg("hi"))) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		events = append(events, evt)
	}

	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Kind != session.EventKindAssistant {
		t.Errorf("kind: got %q", events[0].Kind)
	}
	if events[0].TextContent() != "hello world" {
		t.Errorf("text: got %q", events[0].TextContent())
	}
}

func TestAgentToolCall(t *testing.T) {
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM: &scriptedLLM{responses: []scriptedResponse{
			{toolCalls: []model.ToolCallDelta{
				{CallID: "c1", Name: "ECHO", Args: map[string]any{"text": "hello"}},
			}},
			{text: "done"},
		}},
		Tools: []tool.Tool{&echoTool{}},
	})

	var events []session.Event
	for evt, err := range a.Run(newInvCtx(userMsg("echo"))) {
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		events = append(events, evt)
	}

	var toolCalls, toolResults, assistants int
	for _, e := range events {
		switch e.Kind {
		case session.EventKindToolCall:
			toolCalls++
		case session.EventKindToolResult:
			toolResults++
		case session.EventKindAssistant:
			assistants++
		}
	}
	if toolCalls != 1 || toolResults != 1 || assistants != 1 {
		t.Errorf("got %d/%d/%d tool_call/tool_result/assistant, want 1/1/1",
			toolCalls, toolResults, assistants)
	}
}

func TestAgentToolNotFound(t *testing.T) {
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM: &scriptedLLM{responses: []scriptedResponse{
			{toolCalls: []model.ToolCallDelta{
				{CallID: "c1", Name: "MISSING", Args: map[string]any{}},
			}},
			{text: "done"},
		}},
	})

	var events []session.Event
	for evt, err := range a.Run(newInvCtx(userMsg("go"))) {
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		events = append(events, evt)
	}

	for _, e := range events {
		if e.Kind == session.EventKindToolResult && e.ToolResultPayload != nil {
			if !e.ToolResultPayload.IsError {
				t.Error("expected error for missing tool")
			}
			return
		}
	}
	t.Error("expected tool result event")
}

func TestAgentModelError(t *testing.T) {
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM: &scriptedLLM{responses: []scriptedResponse{
			{err: fmt.Errorf("model overloaded")},
		}},
	})

	var gotErr bool
	for _, err := range a.Run(newInvCtx(userMsg("hi"))) {
		if err != nil {
			gotErr = true
		}
	}
	if !gotErr {
		t.Error("expected error for model failure")
	}
}

func TestAgentMaxModelCalls(t *testing.T) {
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM: &scriptedLLM{responses: []scriptedResponse{
			{toolCalls: []model.ToolCallDelta{{CallID: "c1", Name: "ECHO", Args: map[string]any{}}}},
			{toolCalls: []model.ToolCallDelta{{CallID: "c2", Name: "ECHO", Args: map[string]any{}}}},
			{toolCalls: []model.ToolCallDelta{{CallID: "c3", Name: "ECHO", Args: map[string]any{}}}},
		}},
		Tools: []tool.Tool{&echoTool{}},
	})

	ctx := &mockInvCtx{
		Context:     context.Background(),
		userMessage: userMsg("loop"),
		runConfig: &agent.RunConfig{
			MaxModelCalls: 2,
			MaxToolCalls:  100,
			Metadata:      make(map[string]any),
		},
	}

	var gotErr bool
	for _, err := range a.Run(ctx) {
		if err != nil {
			gotErr = true
		}
	}
	if !gotErr {
		t.Error("expected error for exceeding max model calls")
	}
}

func TestAgentWithPriorMessages(t *testing.T) {
	ml := &scriptedLLM{responses: []scriptedResponse{{text: "continued"}}}
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{LLM: ml})

	prior := []model.Message{
		{Role: model.RoleUser, Content: []model.Part{{Text: "previous"}}},
		{Role: model.RoleAssistant, Content: []model.Part{{Text: "reply"}}},
	}
	ctx := &mockInvCtx{
		Context:       context.Background(),
		priorMessages: prior,
		userMessage:   userMsg("new"),
	}

	for range a.Run(ctx) {
	}

	if len(ml.requests) != 1 {
		t.Fatalf("got %d requests, want 1", len(ml.requests))
	}
	req := ml.requests[0]
	if len(req.Messages) < 3 {
		t.Fatalf("got %d messages, want >= 3", len(req.Messages))
	}
	// First message should be prior user.
	if req.Messages[0].Content[0].Text != "previous" {
		t.Errorf("msg 0: got %q", req.Messages[0].Content[0].Text)
	}
}

func TestAgentPrepare(t *testing.T) {
	a := New(Config{Name: "test", ModelRef: model.Ref{ModelID: "m"}})
	prepared := a.Prepare(agent.PrepareRequest{LLM: &scriptedLLM{}})
	if prepared == nil {
		t.Fatal("expected non-nil")
	}
	if a.llm != nil {
		t.Error("original should not be mutated")
	}
}

func TestAgentSubAgents(t *testing.T) {
	child := New(Config{Name: "child"})
	parent := New(Config{Name: "parent", SubAgents: []agent.Agent{child}})

	if parent.Name() != "parent" {
		t.Errorf("name: %q", parent.Name())
	}
	if len(parent.SubAgents()) != 1 {
		t.Fatalf("subagents: got %d", len(parent.SubAgents()))
	}
	if parent.FindAgent("child") == nil {
		t.Error("expected to find child")
	}
	if parent.FindAgent("missing") != nil {
		t.Error("expected nil for missing")
	}
}
