package runtime

import (
	"context"
	"errors"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/toolsearch"
)

type auxiliarySearchProbeModel struct {
	mainCalls     int
	selectorCalls int
	firstUsage    int
}

func (*auxiliarySearchProbeModel) Name() string             { return "gpt-5.6-sol" }
func (*auxiliarySearchProbeModel) ProviderName() string     { return "openai-codex" }
func (*auxiliarySearchProbeModel) ContextWindowTokens() int { return 200000 }
func (*auxiliarySearchProbeModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (m *auxiliarySearchProbeModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		selector := len(req.Tools) == 1 && req.Tools[0].Function != nil && req.Tools[0].Function.Name == "InspectToolSchema"
		var message model.Message
		usage := 12
		if selector {
			m.selectorCalls++
			if len(req.Messages) == 0 || strings.Contains(req.Messages[0].TextContent(), "parent-private-history") {
				yield(nil, errors.New("selector inherited parent history"))
				return
			}
			if m.selectorCalls == 1 {
				message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "inspect", Name: "InspectToolSchema", Args: `{"name":"docs__read"}`}}, "")
			} else {
				message = model.NewTextMessage(model.RoleAssistant, `{"tools":["docs__read"]}`)
			}
		} else {
			m.mainCalls++
			if m.mainCalls == 1 {
				message = model.MessageFromToolCalls(model.RoleAssistant, []model.ToolCall{{ID: "search", Name: tool.ToolSearchToolName, Args: `{"query":"read docs"}`}}, "")
				if m.firstUsage > 0 {
					usage = m.firstUsage
				}
			} else {
				message = model.NewTextMessage(model.RoleAssistant, "done")
			}
		}
		yield(model.StreamEventFromResponse(&model.Response{
			Message: message, Provider: m.ProviderName(), Model: m.Name(),
			Usage:        model.Usage{PromptTokens: usage - 4, CompletionTokens: 4, TotalTokens: usage, Reported: true},
			TurnComplete: true, StepComplete: true, Status: model.ResponseStatusCompleted,
		}), nil)
	}
}

func auxiliarySearchRun(t *testing.T, llm *auxiliarySearchProbeModel, compactor compact.Engine, admissions *atomic.Int32) ([]*session.Event, error) {
	t.Helper()
	sessions, active := newJournalTestSession(t, "auxiliary-search")
	candidate := tool.NamedTool{Def: tool.Definition{
		Name: "docs__read", Description: "Read documents", InputSchema: map[string]any{"type": "object"},
		Metadata: map[string]any{tool.MetadataToolKind: tool.MetadataToolKindMCP},
	}}
	source := &runtimeDeferredSource{ready: []tool.Tool{candidate}}
	registry := staticPolicyRegistry{mode: policy.NamedMode{ID: "test", Decide: func(context.Context, policy.ToolContext) (policy.Decision, error) {
		return policy.Decision{Action: policy.ActionAllow}, nil
	}}}
	rt, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}, PolicyRegistry: registry, DefaultPolicyMode: "test", Compaction: CompactionConfig{
		Enabled: compactor != nil, WatermarkRatio: 0.9, ForceWatermarkRatio: 0.9,
		DefaultContextWindowTokens: 200000, ReserveOutputTokens: 1024,
	}, Compactor: compactor})
	if err != nil {
		t.Fatal(err)
	}
	spec := agent.AgentSpec{
		Name: "chat", Model: llm, Tools: []tool.Tool{toolsearch.NewSource(source)}, DeferredTools: source,
	}
	if admissions != nil {
		spec.ResolveModelRequest = func(context.Context) (agent.ModelRequestSnapshot, error) {
			return agent.ModelRequestSnapshot{Model: llm, Tools: spec.Tools, DeferredTools: source, Admit: func(_ context.Context, admission agent.ModelRequestAdmission) error {
				if admission.RequestID == "" || admission.TurnID == "" {
					return errors.New("missing model attempt identity")
				}
				admissions.Add(1)
				return nil
			}}, nil
		}
	}
	run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "read docs", AgentSpec: spec})
	if err != nil {
		t.Fatal(err)
	}
	_, runErr := drainRunnerEvents(t, run.Handle)
	loaded, err := sessions.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil {
		t.Fatal(err)
	}
	return loaded.Events, runErr
}

func TestToolSearchAuxiliaryCallsPersistEveryProviderAttempt(t *testing.T) {
	llm := &auxiliarySearchProbeModel{}
	var admissions atomic.Int32
	events, err := auxiliarySearchRun(t, llm, nil, &admissions)
	if err != nil {
		t.Fatal(err)
	}
	if llm.mainCalls != 2 || llm.selectorCalls != 2 {
		t.Fatalf("main=%d selector=%d, want 2 each", llm.mainCalls, llm.selectorCalls)
	}
	if admissions.Load() != 4 {
		t.Fatalf("provider attempt admissions=%d, want 4", admissions.Load())
	}
	receipts, tokens, toolReceipts := 0, 0, 0
	for _, event := range session.InvocationAccountingEvents(events) {
		if !session.IsModelInvocationReceipt(event) {
			continue
		}
		receipts++
		if usage := session.UsageSnapshotFromSessionEvent(event); usage != nil {
			tokens += usage.TotalTokens
		}
		if session.IsMainInvocationVisibleEvent(event) || session.IsClientReplayEvent(event) {
			t.Fatal("private invocation entered main context or replay")
		}
		if event.Meta["caelis"].(map[string]any)["sdk"].(map[string]any)["invocation_kind"] == "tool" {
			toolReceipts++
		}
	}
	if receipts != 4 || toolReceipts != 2 || tokens != 48 {
		t.Fatalf("receipts=%d toolReceipts=%d tokens=%d, want 4/2/48", receipts, toolReceipts, tokens)
	}
}

type rejectAfterSearchCompactor struct{}

func (rejectAfterSearchCompactor) Prepare(context.Context, compact.Request) (compact.Result, error) {
	return compact.Result{}, nil
}
func (rejectAfterSearchCompactor) CompactOnOverflow(context.Context, compact.Request, error) (compact.Result, error) {
	return compact.Result{}, nil
}
func (rejectAfterSearchCompactor) Force(context.Context, compact.Request, string) (compact.Result, error) {
	return compact.Result{}, errors.New("stop after search")
}

func TestToolSearchAuxiliaryIgnoresParentCompactionWatermark(t *testing.T) {
	llm := &auxiliarySearchProbeModel{firstUsage: 185000}
	events, runErr := auxiliarySearchRun(t, llm, rejectAfterSearchCompactor{}, nil)
	if runErr == nil || !strings.Contains(runErr.Error(), "stop after search") {
		t.Fatalf("parent compaction gate did not run after search: %v", runErr)
	}
	if llm.selectorCalls != 2 {
		t.Fatalf("selector provider calls=%d, want 2 despite parent watermark", llm.selectorCalls)
	}
	selected := false
	for _, event := range events {
		if event.Tool != nil && event.Tool.Name == tool.ToolSearchToolName && event.Type == session.EventTypeToolResult {
			selected = len(tool.ParseToolSearchOutput(event.Tool.Output).DiscoveredToolNames()) == 1
		}
	}
	if !selected {
		t.Fatal("ToolSearch did not publish the selection before the parent compaction gate")
	}
}
