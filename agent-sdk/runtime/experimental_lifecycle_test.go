package runtime

import (
	"context"
	"fmt"
	"iter"
	"path/filepath"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
)

func TestExperimentalAsyncPreservesTurnStartBudget(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			h := newAsyncHarness(t, "")
			h.rt.compaction.Async.Enabled = enabled
			if _, err := h.store.AppendEvent(h.ctx, session.AppendEventRequest{SessionRef: h.active.SessionRef, MutationGuard: session.RuntimeMutationGuard(h.ctx), Event: experimentalAssistantEvent(strings.Repeat("previous work with concrete evidence; ", 4000))}); err != nil {
				t.Fatal(err)
			}
			h.llm.summaries <- asyncTextResponse(asyncFixtureSummary, 1000)
			h.llm.responses <- asyncTextResponse("answer", 1000)
			run, err := h.rt.Run(h.ctx, agent.RunRequest{SessionRef: h.active.SessionRef, Input: "preserve this current user input", AgentSpec: agent.AgentSpec{Model: h.llm}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := drainRunnerEvents(t, run.Handle); err != nil {
				t.Fatal(err)
			}
			req := receiveSnapshotValue(t, h.llm.foreground).request
			text := asyncMessagesText(req.Messages)
			if !strings.Contains(text, "CONTEXT CHECKPOINT") || !strings.Contains(text, "preserve this current user input") {
				t.Fatal("first request bypassed compaction or lost new user input")
			}
		})
	}
}

func TestExperimentalFailedMCPDoesNotBlockUnrelatedCompact(t *testing.T) {
	for bits := 0; bits < 2; bits++ {
		t.Run(fmt.Sprint(bits), func(t *testing.T) {
			manager, err := mcp.NewManager(t.Context(), []mcp.ServerSpec{{PluginID: "fixture", Name: "missing", Command: filepath.Join(t.TempDir(), "nonexistent")}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer manager.Close()
			<-manager.Initialized()
			store, active := newTestSessionService(t, "failed-mcp")
			rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}, Compaction: CompactionConfig{Enabled: true, Async: AsyncCompactionConfig{Enabled: bits&1 != 0}}})
			if err != nil {
				t.Fatal(err)
			}
			for attempt := 0; attempt < 2; attempt++ {
				appendTestEvent(t, store, active.SessionRef, userTextEvent("independent history"))
				appendTestEvent(t, store, active.SessionRef, experimentalAssistantEvent("independent answer"))
				result, err := rt.Compact(t.Context(), CompactRequest{SessionRef: active.SessionRef, Model: &compactionRequestCaptureModel{}})
				if err != nil || !result.Compacted {
					t.Fatalf("attempt %d: compact=%v err=%v", attempt, result.Compacted, err)
				}
			}
		})
	}
}

func experimentalAssistantEvent(text string) *session.Event {
	message := model.NewTextMessage(model.RoleAssistant, text)
	return &session.Event{Type: session.EventTypeAssistant, Message: &message}
}

// A low watermark creates distinct healthy recovery cycles without a live model.
type progressingCompactModel struct{ work, summaries int }

func (*progressingCompactModel) Name() string             { return "progressing" }
func (*progressingCompactModel) ProviderName() string     { return "fixture" }
func (*progressingCompactModel) ContextWindowTokens() int { return 10000 }
func (*progressingCompactModel) Capabilities() model.Capabilities {
	return runtimeTestModelCapabilities()
}
func (m *progressingCompactModel) Generate(_ context.Context, req *model.Request) iter.Seq2[*model.StreamEvent, error] {
	return func(yield func(*model.StreamEvent, error) bool) {
		text := "finished"
		var calls []model.ToolCall
		if isRuntimeCompactionRequest(req) {
			m.summaries++
			text = asyncFixtureSummary
		} else if m.work < 8 {
			m.work++
			calls = []model.ToolCall{{ID: fmt.Sprint(m.work), Name: "work", Args: `{}`}}
		}
		message := model.NewTextMessage(model.RoleAssistant, text)
		if len(calls) > 0 {
			message = model.MessageFromToolCalls(model.RoleAssistant, calls, "")
		}
		yield(model.StreamEventFromResponse(&model.Response{Message: message, TurnComplete: true, StepComplete: true}), nil)
	}
}
func TestExperimentalAsyncAllowsHealthyRepeatedRecovery(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			store, active := newTestSessionService(t, "progress")
			llm := &progressingCompactModel{}
			cfg := Config{Sessions: store, AgentFactory: chat.Factory{}, Compaction: CompactionConfig{Enabled: true, WatermarkRatio: .15, ForceWatermarkRatio: .2, ReserveOutputTokens: 10, SafetyMarginTokens: 10, Async: AsyncCompactionConfig{Enabled: enabled}}}
			rt, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			work := tool.NamedTool{Def: tool.Definition{Name: "work", EffectClass: tool.EffectReadOnly, InputSchema: map[string]any{"type": "object"}}, Invoke: func(_ context.Context, c tool.Call) (tool.Result, error) {
				return tool.Result{ID: c.ID, Name: c.Name, Content: []model.Part{model.NewTextPart(strings.Repeat("new independent evidence ", 500))}}, nil
			}}
			run, err := rt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "perform eight independent steps", AgentSpec: agent.AgentSpec{Model: llm, Tools: []tool.Tool{work}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := drainRunnerEvents(t, run.Handle); err != nil {
				t.Fatal(err)
			}
			if llm.work != 8 || llm.summaries < 3 {
				t.Fatalf("work=%d summaries=%d", llm.work, llm.summaries)
			}
		})
	}
}
