package runtime

import (
	"context"
	"iter"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestFreshRuntimesScopeProviderLocalToolCallIDsAndRebuildBothTurns(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	service := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	active, err := service.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: "provider-local-tool-ids",
	})
	if err != nil {
		t.Fatal(err)
	}
	allow := staticPolicyRegistry{mode: policy.NamedMode{
		ID: "allow",
		Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
			return policy.Decision{Action: policy.ActionAllow}, nil
		},
	}}
	target := tool.NamedTool{
		Def: tool.Definition{Name: "ECHO", InputSchema: map[string]any{"type": "object"}},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewJSONPart([]byte(`{"value":"ok"}`))}}, nil
		},
	}

	for turn := 1; turn <= 2; turn++ {
		freshService := sessionfile.NewService(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
		fresh, err := New(Config{
			Sessions: freshService, AgentFactory: chat.Factory{},
			PolicyRegistry: allow, DefaultPolicyMode: "allow",
		})
		if err != nil {
			t.Fatal(err)
		}
		run, err := fresh.Run(context.Background(), agent.RunRequest{
			SessionRef: active.SessionRef, Input: "turn",
			AgentSpec: agent.AgentSpec{Name: "chat", Model: &providerLocalToolIDModel{}, Tools: []tool.Tool{target}},
		})
		if err != nil {
			t.Fatalf("Run(turn %d) error = %v", turn, err)
		}
		if _, err := drainRunnerEvents(t, run.Handle); err != nil {
			t.Fatalf("runner(turn %d) error = %v", turn, err)
		}
	}

	loaded, err := service.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	callKeys := map[string]bool{}
	resultKeys := map[string]bool{}
	for _, event := range loaded.Events {
		if event == nil || event.Tool == nil || event.Tool.ID != "ollama-call-0" {
			continue
		}
		switch session.EventTypeOf(event) {
		case session.EventTypeToolCall:
			callKeys[event.IdempotencyKey] = true
		case session.EventTypeToolResult:
			resultKeys[event.IdempotencyKey] = true
		}
	}
	if len(callKeys) != 2 || len(resultKeys) != 2 {
		t.Fatalf("tool event identities: calls=%v results=%v, want two scoped facts each", callKeys, resultKeys)
	}

	probe := &capturingContextModel{messages: make(chan []model.Message, 1)}
	third, err := New(Config{Sessions: service, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	run, err := third.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef, Input: "verify", AgentSpec: agent.AgentSpec{Name: "probe", Model: probe},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainRunnerEvents(t, run.Handle); err != nil {
		t.Fatal(err)
	}
	messages := <-probe.messages
	toolCalls := 0
	toolResults := 0
	for _, message := range messages {
		for _, call := range message.ToolCalls() {
			if call.ID == "ollama-call-0" {
				toolCalls++
			}
		}
		for _, result := range message.ToolResults() {
			if result.ToolUseID == "ollama-call-0" {
				toolResults++
			}
		}
	}
	if toolCalls != 2 || toolResults != 2 {
		t.Fatalf("rebuilt model context has %d calls and %d results, want 2 paired turns", toolCalls, toolResults)
	}
}

func TestOneTurnScopesRepeatedProviderToolCallIDByStep(t *testing.T) {
	t.Parallel()

	service, active := newTestSessionService(t, "provider-local-step-ids")
	allow := staticPolicyRegistry{mode: policy.NamedMode{
		ID: "allow",
		Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
			return policy.Decision{Action: policy.ActionAllow}, nil
		},
	}}
	target := tool.NamedTool{
		Def: tool.Definition{Name: "ECHO", InputSchema: map[string]any{"type": "object"}},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewJSONPart([]byte(`{"value":"ok"}`))}}, nil
		},
	}
	core, err := New(Config{
		Sessions: service, AgentFactory: chat.Factory{},
		PolicyRegistry: allow, DefaultPolicyMode: "allow",
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := core.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef, Input: "two steps",
		AgentSpec: agent.AgentSpec{Name: "chat", Model: &repeatedProviderToolIDModel{}, Tools: []tool.Tool{target}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainRunnerEvents(t, run.Handle); err != nil {
		t.Fatal(err)
	}
	loaded, err := service.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	callKeys := map[string]bool{}
	resultKeys := map[string]bool{}
	stepIDs := map[string]bool{}
	for _, event := range loaded.Events {
		if event != nil && event.Tool != nil && event.Tool.ID == "ollama-call-0" {
			switch session.EventTypeOf(event) {
			case session.EventTypeToolCall:
				callKeys[event.IdempotencyKey] = true
			case session.EventTypeToolResult:
				resultKeys[event.IdempotencyKey] = true
			}
		}
		if event != nil && event.Journal != nil && event.Journal.ToolExecution != nil {
			stepIDs[event.Journal.ToolExecution.Key.StepID] = true
		}
	}
	if len(callKeys) != 2 || len(resultKeys) != 2 || len(stepIDs) != 2 {
		t.Fatalf("scoped identities: calls=%v results=%v steps=%v", callKeys, resultKeys, stepIDs)
	}
}

type providerLocalToolIDModel struct {
	calls int
}

type repeatedProviderToolIDModel struct {
	calls int
}

func (*repeatedProviderToolIDModel) Name() string { return "repeated-provider-tool-id" }

func (*repeatedProviderToolIDModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}

func (m *repeatedProviderToolIDModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	call := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		response := &model.Response{TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted}
		if call <= 2 {
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
				ID: "ollama-call-0", Name: "ECHO", Args: `{"value":"ok"}`,
			}}, "")
			response.FinishReason = model.FinishReasonToolCalls
		} else {
			response.Message = model.NewTextMessage(model.RoleAssistant, "done")
			response.FinishReason = model.FinishReasonStop
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: response}, nil)
	}
}

func TestSharedToolStepSequenceSurvivesOverflowStyleRebind(t *testing.T) {
	t.Parallel()

	// Overflow recovery re-resolves the Agent (and rewraps tools) while the same
	// run/turn continues. A fresh journal counter would reissue tool-step-1 for
	// a reused provider-local call ID; the shared sequence must keep advancing.
	service, active := newTestSessionService(t, "shared-tool-step-sequence")
	core, err := New(Config{Sessions: service, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	sequence := &atomic.Uint64{}
	base := tool.NamedTool{
		Def: tool.Definition{Name: "ECHO", InputSchema: map[string]any{"type": "object"}},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{ID: call.ID, Name: call.Name, Content: []model.Part{model.NewJSONPart([]byte(`{"value":"ok"}`))}}, nil
		},
	}
	firstWrap := core.wrapToolsForExecutionJournal(active.SessionRef, "run-1", "turn-1", sequence, []tool.Tool{base})
	secondWrap := core.wrapToolsForExecutionJournal(active.SessionRef, "run-1", "turn-1", sequence, []tool.Tool{base})
	if len(firstWrap) != 1 || len(secondWrap) != 1 {
		t.Fatalf("wrapped tools = %d/%d, want 1 each", len(firstWrap), len(secondWrap))
	}
	if _, err := firstWrap[0].Call(context.Background(), tool.Call{ID: "ollama-call-0", Name: "ECHO", Input: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := secondWrap[0].Call(context.Background(), tool.Call{ID: "ollama-call-0", Name: "ECHO", Input: []byte(`{}`)}); err != nil {
		t.Fatal(err)
	}
	events, err := service.Events(context.Background(), session.EventsRequest{
		SessionRef: active.SessionRef, IncludeTransient: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	stepIDs := map[string]bool{}
	for _, event := range events {
		if event == nil || event.Journal == nil || event.Journal.ToolExecution == nil {
			continue
		}
		if event.Journal.ToolExecution.Key.ToolCallID != "ollama-call-0" {
			continue
		}
		stepIDs[event.Journal.ToolExecution.Key.StepID] = true
	}
	if !stepIDs["tool-step-1:ollama-call-0"] || !stepIDs["tool-step-2:ollama-call-0"] {
		t.Fatalf("tool execution step identities = %v, want tool-step-1 and tool-step-2 for ollama-call-0", stepIDs)
	}
}

func (*providerLocalToolIDModel) Name() string { return "provider-local-tool-id" }

func (*providerLocalToolIDModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}

func (m *providerLocalToolIDModel) Generate(context.Context, *model.Request) iter.Seq2[*model.StreamEvent, error] {
	m.calls++
	call := m.calls
	return func(yield func(*model.StreamEvent, error) bool) {
		response := &model.Response{
			TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted,
		}
		if call == 1 {
			response.Message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{
				ID: "ollama-call-0", Name: "ECHO", Args: `{"value":"ok"}`,
			}}, "")
			response.FinishReason = model.FinishReasonToolCalls
		} else {
			response.Message = model.NewTextMessage(model.RoleAssistant, "done")
			response.FinishReason = model.FinishReasonStop
		}
		yield(&model.StreamEvent{Type: model.StreamEventTurnDone, Response: response}, nil)
	}
}
