package evalharness

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	localruntime "github.com/caelis-labs/caelis/agent-sdk/runtime"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	inmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

// ContextCompactionOutcome selects the deterministic Runtime terminal path.
type ContextCompactionOutcome string

const (
	ContextCompactionSuccess ContextCompactionOutcome = "success"
	ContextCompactionFailure ContextCompactionOutcome = "failure"
)

// ContextCompactionRun contains the actual Runtime observation stream and the
// durable Session history written by the same run.
type ContextCompactionRun struct {
	LiveEvents    []*session.Event
	DurableEvents []*session.Event
}

// RunContextCompaction executes a real Runtime turn with deterministic model,
// Session, clock, and compaction inputs. It is the semantic source for Surface
// product scenarios; callers must still use the maintained ACP projector.
func RunContextCompaction(ctx context.Context, outcome ContextCompactionOutcome) (ContextCompactionRun, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	const sessionID = "product-context-compaction"
	var sequence atomic.Uint64
	nextID := func(prefix string) string {
		return fmt.Sprintf("%s-%d", prefix, sequence.Add(1))
	}
	baseTime := time.Unix(120, 0)
	clock := func() time.Time {
		return baseTime.Add(time.Duration(sequence.Add(1)) * time.Millisecond)
	}
	store := inmemory.NewStore(inmemory.Config{
		SessionIDGenerator: func() string { return sessionID },
		EventIDGenerator:   func() string { return nextID("event") },
		Clock:              clock,
	})
	active, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName:            "caelis",
		UserID:             "product-acceptance",
		PreferredSessionID: sessionID,
		Workspace: session.WorkspaceRef{
			Key: "product-acceptance",
			CWD: "/tmp/product-scenario",
		},
	})
	if err != nil {
		return ContextCompactionRun{}, fmt.Errorf("start scenario Session: %w", err)
	}
	for _, event := range contextCompactionHistory() {
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: active.SessionRef,
			Event:      event,
		}); err != nil {
			return ContextCompactionRun{}, fmt.Errorf("seed scenario Session: %w", err)
		}
	}

	var steps []ScriptStep
	switch outcome {
	case ContextCompactionSuccess:
		steps = []ScriptStep{
			TextStep("CONTEXT CHECKPOINT\n\n## Current Objective\n- verify Runtime to TUI compaction\n\n## Next Actions\n1. continue"),
			TextStep("Compaction path is healthy."),
		}
	case ContextCompactionFailure:
		for range 8 {
			steps = append(steps, ScriptStep{Err: errors.New("provider unavailable")})
		}
	default:
		return ContextCompactionRun{}, fmt.Errorf("unknown context compaction outcome %q", outcome)
	}
	scripted := NewScriptedModel("product-context-compaction", steps...)
	runtime, err := localruntime.New(localruntime.Config{
		Sessions:       store,
		AgentFactory:   chat.Factory{SystemPrompt: "Be terse."},
		RunIDGenerator: func() string { return "run-product-compaction" },
		Clock:          clock,
		Compaction: localruntime.CompactionConfig{
			Enabled:                    true,
			WatermarkRatio:             0.7,
			ForceWatermarkRatio:        0.85,
			DefaultContextWindowTokens: 64,
			ReserveOutputTokens:        16,
			SafetyMarginTokens:         8,
			SegmentTokenBudget:         80,
		},
	})
	if err != nil {
		return ContextCompactionRun{}, fmt.Errorf("build scenario Runtime: %w", err)
	}
	result, err := runtime.Run(ctx, agent.RunRequest{
		SessionRef: active.SessionRef,
		Input:      "continue",
		AgentSpec: agent.AgentSpec{
			Name:  "chat",
			Model: scripted,
		},
	})
	if err != nil {
		return ContextCompactionRun{}, fmt.Errorf("start scenario Runtime: %w", err)
	}
	liveEvents, runErr := collectRunnerEvents(result.Handle)
	switch outcome {
	case ContextCompactionSuccess:
		if runErr != nil {
			return ContextCompactionRun{LiveEvents: liveEvents}, fmt.Errorf("run successful compaction scenario: %w", runErr)
		}
	case ContextCompactionFailure:
		if runErr == nil {
			return ContextCompactionRun{LiveEvents: liveEvents}, errors.New("failed compaction error is nil, want provider unavailable")
		}
		if !strings.Contains(runErr.Error(), "provider unavailable") {
			return ContextCompactionRun{LiveEvents: liveEvents}, fmt.Errorf("failed compaction error does not contain provider unavailable: %w", runErr)
		}
	}
	loaded, err := store.LoadSession(ctx, session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		return ContextCompactionRun{LiveEvents: liveEvents}, fmt.Errorf("load scenario Session: %w", err)
	}
	return ContextCompactionRun{
		LiveEvents:    session.CloneEvents(liveEvents),
		DurableEvents: session.CloneEvents(loaded.Events),
	}, nil
}

func contextCompactionHistory() []*session.Event {
	texts := []struct {
		role model.Role
		text string
	}{
		{role: model.RoleUser, text: "Project objective: verify Runtime to TUI context compaction."},
		{role: model.RoleAssistant, text: "Acknowledged objective."},
		{role: model.RoleUser, text: "Constraint: preserve typed lifecycle and notice semantics."},
		{role: model.RoleAssistant, text: "Acknowledged constraint."},
		{role: model.RoleUser, text: "Next action: run deterministic product acceptance."},
		{role: model.RoleAssistant, text: "Ready."},
	}
	events := make([]*session.Event, 0, len(texts))
	for _, item := range texts {
		message := model.NewTextMessage(item.role, item.text)
		eventType := session.EventTypeAssistant
		actorKind := session.ActorKindController
		if item.role == model.RoleUser {
			eventType = session.EventTypeUser
			actorKind = session.ActorKindUser
		}
		events = append(events, session.CanonicalizeEvent(&session.Event{
			Type:    eventType,
			Actor:   session.ActorRef{Kind: actorKind},
			Message: &message,
		}))
	}
	return events
}

func collectRunnerEvents(handle agent.Runner) ([]*session.Event, error) {
	if handle == nil {
		return nil, nil
	}
	var events []*session.Event
	for event, err := range handle.Events() {
		if err != nil {
			if _, ok := agent.AsEventStreamGap(err); ok {
				continue
			}
			return events, err
		}
		if event != nil {
			events = append(events, session.CloneEvent(event))
		}
	}
	return events, nil
}
