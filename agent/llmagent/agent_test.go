package llmagent

import (
	"context"
	"fmt"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/tool"
	"github.com/OnslaughtSnail/caelis/trace"
)

// ─── Mock LLM ────────────────────────────────────────────────────────

type scriptedLLM struct {
	responses []scriptedResponse
	callCount int
	requests  []model.Request
}

type scriptedResponse struct {
	text      string
	reasoning string
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
		if resp.reasoning != "" {
			yield(model.ResponseEvent{ReasoningDelta: resp.reasoning}, nil)
		}
		for _, tc := range resp.toolCalls {
			yield(model.ResponseEvent{ToolCall: &tc}, nil)
		}
		yield(model.ResponseEvent{FinishReason: "stop"}, nil)
	}
}

type streamingDeltaLLM struct {
	events []model.ResponseEvent
}

func (m *streamingDeltaLLM) Name() string { return "streaming-delta" }

func (m *streamingDeltaLLM) Generate(_ context.Context, _ model.Request) iter.Seq2[model.ResponseEvent, error] {
	return func(yield func(model.ResponseEvent, error) bool) {
		for _, evt := range m.events {
			if !yield(evt, nil) {
				return
			}
		}
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

type panicTool struct{}

func (t *panicTool) Definition() tool.Definition {
	return tool.Definition{Name: "ECHO", Schema: tool.Schema{Type: "object"}}
}

func (t *panicTool) Run(tool.Context, tool.Call) (tool.Result, error) {
	panic("llmagent must not run tools directly")
}

type recordingExecutor struct {
	calls []tool.Call
}

func (e *recordingExecutor) Execute(_ context.Context, call tool.Call) (tool.Result, error) {
	e.calls = append(e.calls, tool.CloneCall(call))
	text, _ := call.Args["text"].(string)
	return tool.Result{Output: "executor:" + text}, nil
}

type concurrentExecutor struct {
	mu        sync.Mutex
	active    int
	maxActive int
	calls     []string
}

func (e *concurrentExecutor) Execute(ctx context.Context, call tool.Call) (tool.Result, error) {
	e.mu.Lock()
	e.active++
	if e.active > e.maxActive {
		e.maxActive = e.active
	}
	e.calls = append(e.calls, call.CallID)
	e.mu.Unlock()

	switch call.CallID {
	case "c1":
		select {
		case <-time.After(40 * time.Millisecond):
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	case "c2":
		select {
		case <-time.After(5 * time.Millisecond):
		case <-ctx.Done():
			return tool.Result{}, ctx.Err()
		}
	}

	e.mu.Lock()
	e.active--
	e.mu.Unlock()
	return tool.Result{Output: "result:" + call.CallID}, nil
}

func (e *concurrentExecutor) MaxActive() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.maxActive
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
func (c *mockInvCtx) Hooks() []agent.Hook  { return nil }
func (c *mockInvCtx) Tracer() trace.Tracer { return nil }
func (c *mockInvCtx) EndInvocation()       { c.ended = true }
func (c *mockInvCtx) Ended() bool          { return c.ended }

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

func TestAgentPreservesReasoningPartsWithinInvocationHistory(t *testing.T) {
	llm := &scriptedLLM{responses: []scriptedResponse{
		{
			reasoning: "need tool",
			toolCalls: []model.ToolCallDelta{{
				CallID: "call-1",
				Name:   "ECHO",
				Args:   map[string]any{"text": "x"},
			}},
		},
		{text: "done"},
	}}
	exec := &recordingExecutor{}
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM:          llm,
		ToolExecutor: exec,
	})

	for _, err := range a.Run(newInvCtx(userMsg("hi"))) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
	}

	if len(llm.requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(llm.requests))
	}
	var got *model.Reasoning
	for _, msg := range llm.requests[1].Messages {
		for _, part := range msg.Content {
			if part.Reasoning != nil {
				got = part.Reasoning
			}
		}
	}
	if got == nil || got.Text != "need tool" {
		t.Fatalf("second request reasoning = %#v, want need tool", got)
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

	if len(events) != 2 {
		t.Fatalf("got %d events, want transient delta plus canonical assistant", len(events))
	}
	if events[0].Kind != session.EventKindAssistant || events[0].Visibility != session.VisibilityUIOnly {
		t.Errorf("delta event: got %#v, want ui-only assistant", events[0])
	}
	if events[1].Kind != session.EventKindAssistant || events[1].Visibility != session.VisibilityCanonical {
		t.Errorf("canonical event: got %#v, want canonical assistant", events[1])
	}
	if events[1].TextContent() != "hello world" {
		t.Errorf("text: got %q", events[1].TextContent())
	}
}

func TestAgentStreamsTransientTextAndReasoningBeforeCanonicalAssistant(t *testing.T) {
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM: &streamingDeltaLLM{events: []model.ResponseEvent{
			{ReasoningDelta: "think-"},
			{TextDelta: "hel"},
			{ReasoningDelta: "ing"},
			{TextDelta: "lo"},
			{FinishReason: "stop"},
		}},
	})

	var events []session.Event
	for evt, err := range a.Run(newInvCtx(userMsg("hi"))) {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		events = append(events, evt)
	}

	if len(events) != 5 {
		t.Fatalf("events = %#v, want four transient deltas plus canonical assistant", events)
	}
	for i := 0; i < 4; i++ {
		if events[i].Kind != session.EventKindAssistant || events[i].Visibility != session.VisibilityUIOnly {
			t.Fatalf("event[%d] = %#v, want ui-only assistant delta", i, events[i])
		}
		if events[i].AssistantPayload == nil || len(events[i].AssistantPayload.Parts) != 1 {
			t.Fatalf("event[%d] payload = %#v, want one delta part", i, events[i].AssistantPayload)
		}
	}
	if events[0].AssistantPayload.Parts[0].Kind != session.PartKindReasoning || events[0].TextContent() != "think-" {
		t.Fatalf("event[0] = %#v, want reasoning delta think-", events[0])
	}
	if events[1].AssistantPayload.Parts[0].Kind != session.PartKindText || events[1].TextContent() != "hel" {
		t.Fatalf("event[1] = %#v, want text delta hel", events[1])
	}
	final := events[4]
	if final.Kind != session.EventKindAssistant || final.Visibility != session.VisibilityCanonical {
		t.Fatalf("final = %#v, want canonical assistant", final)
	}
	if final.TextContent() != "think-inghello" {
		t.Fatalf("final text content = %q, want reasoning+text content", final.TextContent())
	}
	if len(final.AssistantPayload.Parts) != 2 ||
		final.AssistantPayload.Parts[0].Kind != session.PartKindReasoning ||
		final.AssistantPayload.Parts[0].Text != "think-ing" ||
		final.AssistantPayload.Parts[1].Kind != session.PartKindText ||
		final.AssistantPayload.Parts[1].Text != "hello" {
		t.Fatalf("final parts = %#v, want accumulated reasoning and text", final.AssistantPayload.Parts)
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
		Tools:        []tool.Tool{&panicTool{}},
		ToolExecutor: &recordingExecutor{},
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
			if e.Visibility == session.VisibilityCanonical {
				assistants++
			}
		}
	}
	if toolCalls != 1 || toolResults != 1 || assistants != 1 {
		t.Errorf("got %d/%d/%d tool_call/tool_result/assistant, want 1/1/1",
			toolCalls, toolResults, assistants)
	}
}

func TestAgentBuildsToolSpecsFromCatalog(t *testing.T) {
	llm := &scriptedLLM{responses: []scriptedResponse{
		{toolCalls: []model.ToolCallDelta{
			{CallID: "call-1", Name: "ECHO", Args: map[string]any{"text": "hi"}},
		}},
		{text: "done"},
	}}
	catalog := tool.NewMemoryRegistry()
	catalog.Register(&panicTool{})
	executor := &recordingExecutor{}
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM:          llm,
		ToolCatalog:  catalog,
		ToolExecutor: executor,
	})

	for _, err := range a.Run(newInvCtx(userMsg("use tool"))) {
		if err != nil {
			t.Fatalf("Run error: %v", err)
		}
	}
	if len(llm.requests) == 0 || len(llm.requests[0].Tools) != 1 || llm.requests[0].Tools[0].Name != "ECHO" {
		t.Fatalf("model tools = %#v, want ECHO from catalog", llm.requests[0].Tools)
	}
	if len(executor.calls) != 1 || executor.calls[0].Name != "ECHO" {
		t.Fatalf("executor calls = %#v, want ECHO", executor.calls)
	}
}

func TestAgentToolCallUsesPreparedExecutor(t *testing.T) {
	executor := &recordingExecutor{}
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM: &scriptedLLM{responses: []scriptedResponse{
			{toolCalls: []model.ToolCallDelta{
				{CallID: "c1", Name: "ECHO", Args: map[string]any{"text": "hello"}},
			}},
			{text: "done"},
		}},
		Tools:        []tool.Tool{&panicTool{}},
		ToolExecutor: executor,
	})

	var resultText string
	for evt, err := range a.Run(newInvCtx(userMsg("echo"))) {
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if evt.Kind == session.EventKindToolResult && evt.ToolResultPayload != nil {
			for _, part := range evt.Content {
				if part.Kind == session.PartKindText {
					resultText += part.Text
				}
			}
		}
	}

	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(executor.calls))
	}
	if executor.calls[0].Name != "ECHO" || executor.calls[0].CallID != "c1" {
		t.Fatalf("executor call = %#v, want ECHO/c1", executor.calls[0])
	}
	if resultText != "executor:hello" {
		t.Fatalf("tool result text = %q, want executor output", resultText)
	}
}

func TestAgentExecutesToolCallsConcurrentlyAndYieldsResultsInCallOrder(t *testing.T) {
	executor := &concurrentExecutor{}
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM: &scriptedLLM{responses: []scriptedResponse{
			{toolCalls: []model.ToolCallDelta{
				{CallID: "c1", Name: "ECHO", Args: map[string]any{"text": "one"}},
				{CallID: "c2", Name: "ECHO", Args: map[string]any{"text": "two"}},
			}},
			{text: "done"},
		}},
		Tools:        []tool.Tool{&echoTool{}},
		ToolExecutor: executor,
	})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	invCtx := newInvCtx(userMsg("echo"))
	invCtx.Context = ctx

	var resultIDs []string
	var resultTexts []string
	for evt, err := range a.Run(invCtx) {
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if evt.Kind == session.EventKindToolResult && evt.ToolResultPayload != nil {
			resultIDs = append(resultIDs, evt.ToolResultPayload.CallID)
			if len(evt.Content) > 0 {
				resultTexts = append(resultTexts, evt.Content[0].Text)
			}
		}
	}

	if executor.MaxActive() < 2 {
		t.Fatalf("max active tool calls = %d, want concurrent execution", executor.MaxActive())
	}
	if got, want := fmt.Sprint(resultIDs), "[c1 c2]"; got != want {
		t.Fatalf("result order = %s, want %s", got, want)
	}
	if got, want := fmt.Sprint(resultTexts), "[result:c1 result:c2]"; got != want {
		t.Fatalf("result text order = %s, want %s", got, want)
	}
}

func TestAgentRepairsMissingToolCallID(t *testing.T) {
	executor := &recordingExecutor{}
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM: &scriptedLLM{responses: []scriptedResponse{
			{toolCalls: []model.ToolCallDelta{
				{Name: "ECHO", Args: map[string]any{"text": "hello"}},
			}},
			{text: "done"},
		}},
		Tools:        []tool.Tool{&echoTool{}},
		ToolExecutor: executor,
	})

	var toolCallID string
	for evt, err := range a.Run(newInvCtx(userMsg("echo"))) {
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if evt.Kind == session.EventKindToolCall && evt.ToolCallPayload != nil {
			toolCallID = evt.ToolCallPayload.CallID
		}
	}

	if toolCallID == "" {
		t.Fatal("expected repaired tool call id")
	}
	if len(executor.calls) != 1 || executor.calls[0].CallID != toolCallID {
		t.Fatalf("executor calls = %#v, want repaired call id %q", executor.calls, toolCallID)
	}
}

func TestAgentTurnsMissingToolNameIntoCanonicalToolError(t *testing.T) {
	executor := &recordingExecutor{}
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM: &scriptedLLM{responses: []scriptedResponse{
			{toolCalls: []model.ToolCallDelta{
				{CallID: "bad-1", Args: map[string]any{"text": "hello"}},
			}},
			{text: "done"},
		}},
		Tools:        []tool.Tool{&echoTool{}},
		ToolExecutor: executor,
	})

	var callName string
	var resultText string
	var resultIsError bool
	for evt, err := range a.Run(newInvCtx(userMsg("echo"))) {
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if evt.Kind == session.EventKindToolCall && evt.ToolCallPayload != nil {
			callName = evt.ToolCallPayload.Name
		}
		if evt.Kind == session.EventKindToolResult && evt.ToolResultPayload != nil {
			resultIsError = evt.IsError
			if len(evt.Content) > 0 {
				resultText = evt.Content[0].Text
			}
		}
	}

	if callName != "INVALID_TOOL_CALL" {
		t.Fatalf("tool call name = %q, want INVALID_TOOL_CALL", callName)
	}
	if !resultIsError || !strings.Contains(resultText, "missing tool name") {
		t.Fatalf("tool result = isError:%v text:%q, want missing-name error", resultIsError, resultText)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("executor calls = %#v, want none for invalid tool name", executor.calls)
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
			if !e.IsError {
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
		Tools:        []tool.Tool{&echoTool{}},
		ToolExecutor: &recordingExecutor{},
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

func TestAgentMaxToolCalls(t *testing.T) {
	executor := &recordingExecutor{}
	a := prepare(Config{Name: "test"}, agent.PrepareRequest{
		LLM: &scriptedLLM{responses: []scriptedResponse{
			{toolCalls: []model.ToolCallDelta{
				{CallID: "c1", Name: "ECHO", Args: map[string]any{"text": "one"}},
				{CallID: "c2", Name: "ECHO", Args: map[string]any{"text": "two"}},
			}},
		}},
		Tools:        []tool.Tool{&echoTool{}},
		ToolExecutor: executor,
	})

	ctx := &mockInvCtx{
		Context:     context.Background(),
		userMessage: userMsg("loop"),
		runConfig: &agent.RunConfig{
			MaxModelCalls: 100,
			MaxToolCalls:  1,
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
		t.Error("expected error for exceeding max tool calls")
	}
	if len(executor.calls) != 1 {
		t.Fatalf("executor calls = %d, want only first tool call executed", len(executor.calls))
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
