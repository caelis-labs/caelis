package eventstream

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestEnvelopeMarshalIncludesACPUpdate(t *testing.T) {
	env := Envelope{
		Kind:      KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "hello"},
		},
	}

	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(Envelope) error = %v", err)
	}
	text := string(data)
	for _, want := range []string{
		`"kind":"session/update"`,
		`"update":`,
		`"sessionUpdate":"agent_message_chunk"`,
		`"text":"hello"`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("json = %s, want %s", text, want)
		}
	}
}

func TestTurnLifecycleConstructors(t *testing.T) {
	at := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)

	completed := TurnCompleted(" handle ", " run ", " turn ", at)
	if completed.Kind != KindLifecycle || completed.HandleID != "handle" || completed.RunID != "run" || completed.TurnID != "turn" {
		t.Fatalf("completed envelope = %#v", completed)
	}
	if completed.Lifecycle == nil || completed.Lifecycle.State != LifecycleStateCompleted || completed.Lifecycle.StopReason != schema.StopReasonEndTurn {
		t.Fatalf("completed lifecycle = %#v", completed.Lifecycle)
	}
	if !completed.OccurredAt.Equal(at) || completed.Scope != ScopeMain {
		t.Fatalf("completed timing/scope = (%v, %q)", completed.OccurredAt, completed.Scope)
	}

	failed := TurnFailed("h", "r", "t", " provider failed ", at)
	if failed.Lifecycle == nil || failed.Lifecycle.State != LifecycleStateFailed || failed.Lifecycle.Reason != "provider failed" || failed.Lifecycle.StopReason != "" {
		t.Fatalf("failed lifecycle = %#v", failed.Lifecycle)
	}

	cancelled := TurnCancelled("h", "r", "t", " context canceled ", at)
	if cancelled.Lifecycle == nil || cancelled.Lifecycle.State != LifecycleStateCancelled || cancelled.Lifecycle.Reason != "context canceled" || cancelled.Lifecycle.StopReason != schema.StopReasonCancelled {
		t.Fatalf("cancelled lifecycle = %#v", cancelled.Lifecycle)
	}
}

func TestIsTerminalLifecycle(t *testing.T) {
	for _, state := range []string{
		LifecycleStateCompleted,
		LifecycleStateFailed,
		LifecycleStateInterrupted,
		LifecycleStateCancelled,
		"canceled",
		"terminated",
	} {
		if !IsTerminalLifecycle(Envelope{Kind: KindLifecycle, Lifecycle: &Lifecycle{State: state}}) {
			t.Fatalf("state %q should be terminal", state)
		}
	}
	for _, state := range []string{"", LifecycleStateRunning, "queued"} {
		if IsTerminalLifecycle(Envelope{Kind: KindLifecycle, Lifecycle: &Lifecycle{State: state}}) {
			t.Fatalf("state %q should not be terminal", state)
		}
	}
	if IsTerminalLifecycle(Envelope{Kind: KindError, Lifecycle: &Lifecycle{State: LifecycleStateCompleted}}) {
		t.Fatal("non-lifecycle envelope should not be terminal")
	}
}

func TestEnsureTerminalLifecycleSynthesizesCompletedForEmptyStream(t *testing.T) {
	out := collectLifecycleTestEnvelopes(EnsureTerminalLifecycle(nil, "h", "r", "t"))
	if len(out) != 1 {
		t.Fatalf("events = %#v, want synthesized completion only", out)
	}
	assertLifecycleState(t, out[0], LifecycleStateCompleted)
	if out[0].HandleID != "h" || out[0].RunID != "r" || out[0].TurnID != "t" {
		t.Fatalf("ids = (%q, %q, %q), want h/r/t", out[0].HandleID, out[0].RunID, out[0].TurnID)
	}
}

func TestEnsureTerminalLifecycleSynthesizesFailedAfterError(t *testing.T) {
	src := make(chan Envelope, 1)
	src <- Error(errors.New("provider failed"))
	close(src)

	out := collectLifecycleTestEnvelopes(EnsureTerminalLifecycle(src, "h", "r", "t"))
	if len(out) != 2 {
		t.Fatalf("events = %#v, want error plus failed lifecycle", out)
	}
	if out[0].Kind != KindError {
		t.Fatalf("first event = %#v, want error", out[0])
	}
	assertLifecycleState(t, out[1], LifecycleStateFailed)
	if out[1].Lifecycle.Reason != "provider failed" {
		t.Fatalf("failure reason = %q", out[1].Lifecycle.Reason)
	}
}

func TestEnsureTerminalLifecycleSynthesizesCancelledAfterCancelError(t *testing.T) {
	src := make(chan Envelope, 1)
	src <- Error(errors.New("providers: context canceled"))
	close(src)

	out := collectLifecycleTestEnvelopes(EnsureTerminalLifecycle(src, "h", "r", "t"))
	if len(out) != 2 {
		t.Fatalf("events = %#v, want error plus cancelled lifecycle", out)
	}
	assertLifecycleState(t, out[1], LifecycleStateCancelled)
	if out[1].Lifecycle.StopReason != schema.StopReasonCancelled {
		t.Fatalf("stopReason = %q, want cancelled", out[1].Lifecycle.StopReason)
	}
}

func TestEnsureTerminalLifecycleForwardsExplicitTerminalOnce(t *testing.T) {
	src := make(chan Envelope, 4)
	src <- Envelope{Kind: KindNotice, Notice: "hello"}
	src <- TurnCompleted("h", "r", "t", time.Time{})
	src <- TurnFailed("h", "r", "t", "late", time.Time{})
	src <- Envelope{Kind: KindNotice, Notice: "late"}
	close(src)

	out := collectLifecycleTestEnvelopes(EnsureTerminalLifecycle(src, "h", "r", "t"))
	if len(out) != 2 {
		t.Fatalf("events = %#v, want pre-terminal notice plus first terminal", out)
	}
	if out[0].Kind != KindNotice {
		t.Fatalf("first event = %#v, want notice", out[0])
	}
	assertLifecycleState(t, out[1], LifecycleStateCompleted)
}

func TestIsCancelledReason(t *testing.T) {
	for _, reason := range []string{"context canceled", "providers: context canceled", "cancelled by user", "canceled"} {
		if !IsCancelledReason(reason) {
			t.Fatalf("IsCancelledReason(%q) = false", reason)
		}
	}
	for _, reason := range []string{"provider failed", "", "deadline exceeded"} {
		if IsCancelledReason(reason) {
			t.Fatalf("IsCancelledReason(%q) = true", reason)
		}
	}
}

func collectLifecycleTestEnvelopes(events <-chan Envelope) []Envelope {
	var out []Envelope
	for env := range events {
		out = append(out, env)
	}
	return out
}

func assertLifecycleState(t *testing.T, env Envelope, state string) {
	t.Helper()
	if !IsTerminalLifecycle(env) {
		t.Fatalf("env = %#v, want terminal lifecycle", env)
	}
	if env.Lifecycle == nil || env.Lifecycle.State != state {
		t.Fatalf("lifecycle = %#v, want state %q", env.Lifecycle, state)
	}
}
