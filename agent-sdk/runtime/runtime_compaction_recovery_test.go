package runtime

import (
	"context"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

func TestRuntimeAllowsRepeatedCompactionRecoveriesInOneRun(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-repeated-compact-recoveries")
	testModel := &repeatedWatermarkModel{
		t:                    t,
		toolCallsBeforeFinal: 5,
	}
	targetTool := repeatedCompactionEchoTool()

	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		Compaction: repeatedCompactionConfig(),
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "Use ECHO repeatedly and then finish.",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
			Tools: []tool.Tool{targetTool},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	var finalText string
	compactNotices := 0
	for event, seqErr := range result.Handle.Events() {
		if seqErr != nil {
			t.Fatalf("runner error = %v", seqErr)
		}
		if event != nil && event.Type == session.EventTypeAssistant {
			finalText = strings.TrimSpace(session.EventText(event))
		}
		if notice, ok := session.NoticeOf(event); ok && notice.Text == compact.CompactNoticeLabel {
			compactNotices++
		}
	}
	if finalText != "finished after repeated compactions" {
		t.Fatalf("finalText = %q, want repeated compaction completion", finalText)
	}
	if testModel.compactionCalls != 5 {
		t.Fatalf("compactionCalls = %d, want 5", testModel.compactionCalls)
	}
	if testModel.normalCalls != 6 {
		t.Fatalf("normalCalls = %d, want 6", testModel.normalCalls)
	}
	if testModel.checkpointRequests != 5 {
		t.Fatalf("checkpointRequests = %d, want 5", testModel.checkpointRequests)
	}
	if compactNotices != 5 {
		t.Fatalf("compact notices = %d, want 5", compactNotices)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	compactEvents := 0
	for _, event := range loaded.Events {
		if event != nil && event.Type == session.EventTypeCompact {
			compactEvents++
		}
	}
	if compactEvents != 5 {
		t.Fatalf("durable compact events = %d, want 5", compactEvents)
	}
}

func TestRuntimeStopsCompactionRecoveryWithoutSourceProgress(t *testing.T) {
	t.Parallel()

	sessions, activeSession := newTestSessionService(t, "sess-compact-recovery-no-progress")
	testModel := &stepWatermarkModel{t: t}
	runtime, err := New(Config{
		Sessions: sessions,
		AgentFactory: chat.Factory{
			SystemPrompt: "Use tools when necessary.",
		},
		Compaction: repeatedCompactionConfig(),
		Compactor:  noProgressRecoveryCompactor{},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: activeSession.SessionRef,
		Input:      "Use ECHO and then finish.",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: testModel,
			Tools: []tool.Tool{repeatedCompactionEchoTool()},
		},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	_, seqErr := drainRunnerEvents(t, result.Handle)
	if seqErr == nil {
		t.Fatal("runner error = nil, want no durable compact progress")
	}
	if !strings.Contains(seqErr.Error(), "made no durable compact progress") {
		t.Fatalf("runner error = %v, want no-progress compact recovery", seqErr)
	}

	loaded, err := sessions.LoadSession(context.Background(), session.LoadSessionRequest{
		SessionRef: activeSession.SessionRef,
	})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	for _, event := range loaded.Events {
		if event != nil && event.Type == session.EventTypeCompact {
			t.Fatalf("no-progress compact must not be persisted: %#v", event)
		}
	}
}

func repeatedCompactionConfig() CompactionConfig {
	return CompactionConfig{
		Enabled:                    true,
		WatermarkRatio:             0.80,
		ForceWatermarkRatio:        0.90,
		DefaultContextWindowTokens: 256,
		ReserveOutputTokens:        32,
		SafetyMarginTokens:         16,
		SegmentTokenBudget:         80,
	}
}

func repeatedCompactionEchoTool() tool.Tool {
	return tool.NamedTool{
		Def: tool.Definition{
			Name:        "ECHO",
			Description: "echo input",
			InputSchema: map[string]any{"type": "object"},
		},
		Invoke: func(_ context.Context, call tool.Call) (tool.Result, error) {
			return tool.Result{
				ID:   call.ID,
				Name: call.Name,
				Content: []model.Part{
					model.NewJSONPart([]byte(`{"value":"pong","detail":"repeated compaction tool result"}`)),
				},
			}, nil
		},
	}
}

type noProgressRecoveryCompactor struct{}

func (noProgressRecoveryCompactor) Prepare(_ context.Context, req compact.Request) (compact.Result, error) {
	promptEvents := compact.PromptEventsFromLatestCompact(req.Events)
	return compact.Result{
		PromptEvents: promptEvents,
		Usage: compact.UsageSnapshot{
			TotalTokens:          1,
			ContextWindowTokens:  256,
			EffectiveInputBudget: 208,
		},
	}, nil
}

func (noProgressRecoveryCompactor) CompactOnOverflow(context.Context, compact.Request, error) (compact.Result, error) {
	return compact.Result{}, nil
}

func (noProgressRecoveryCompactor) Force(_ context.Context, req compact.Request, trigger string) (compact.Result, error) {
	event := buildCompactEvent(req.Session, "CONTEXT CHECKPOINT\n\nNo source progress.", compact.CompactEventData{
		ContractVersion: compact.CompactContractVersion,
		Generator:       "test_no_progress",
		Trigger:         strings.TrimSpace(trigger),
	})
	return compact.Result{
		Compacted:    true,
		CompactText:  session.EventText(event),
		CompactEvent: event,
		PromptEvents: compact.PromptEventsFromLatestCompact([]*session.Event{event}),
	}, nil
}
