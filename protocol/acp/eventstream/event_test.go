package eventstream

import (
	"encoding/json"
	"errors"
	"os"
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

func TestEnvelopeV1SessionUpdateGolden(t *testing.T) {
	t.Parallel()

	env := Envelope{
		Kind:       KindSessionUpdate,
		Cursor:     "turn-1:0001",
		SessionID:  "session-1",
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		OccurredAt: time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC),
		Scope:      ScopeMain,
		Final:      true,
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			Content:       schema.TextContent{Type: "text", Text: "hello"},
			MessageID:     "msg-1",
			Meta:          map[string]any{"vendor": map[string]any{"trace": "abc"}},
		},
		Meta: map[string]any{
			"caelis": map[string]any{
				"runtime": map[string]any{
					"tool": map[string]any{"status_detail": "completed"},
				},
			},
		},
	}
	assertGoldenJSON(t, "testdata/envelope_v1_session_update.golden.json", env)
}

func TestEnvelopeV1RequestPermissionGolden(t *testing.T) {
	t.Parallel()

	title := "RUN_COMMAND"
	kind := schema.ToolKindExecute
	status := schema.ToolStatusPending
	env := Envelope{
		Kind:       KindRequestPermission,
		Cursor:     "turn-1:0002",
		SessionID:  "session-1",
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		OccurredAt: time.Date(2026, 6, 27, 12, 0, 1, 0, time.UTC),
		Scope:      ScopeMain,
		Permission: &schema.RequestPermissionRequest{
			SessionID: "session-1",
			ToolCall: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Title:         &title,
				Kind:          &kind,
				Status:        &status,
				RawInput:      map[string]any{"command": "make test"},
			},
			Options: []schema.PermissionOption{
				{OptionID: schema.PermAllowOnce, Name: "Allow once", Kind: schema.PermAllowOnce},
				{OptionID: schema.PermRejectOnce, Name: "Reject", Kind: schema.PermRejectOnce},
			},
			Meta: map[string]any{
				"caelis": map[string]any{
					"approval": map[string]any{"mode": "manual"},
				},
			},
		},
	}
	assertGoldenJSON(t, "testdata/envelope_v1_request_permission.golden.json", env)
}

func TestCloneEnvelopePreservesContentChunkMetadata(t *testing.T) {
	t.Parallel()

	env := Envelope{
		Kind:      KindSessionUpdate,
		SessionID: "session-1",
		Update: schema.ContentChunk{
			SessionUpdate: schema.UpdateAgentMessage,
			MessageID:     "msg-1",
			Content:       schema.TextContent{Type: "text", Text: "hello"},
			Meta:          map[string]any{"vendor": map[string]any{"trace": "abc"}},
		},
	}
	cloned := CloneEnvelope(env)
	chunk, ok := cloned.Update.(schema.ContentChunk)
	if !ok {
		t.Fatalf("cloned update = %T, want ContentChunk", cloned.Update)
	}
	if chunk.MessageID != "msg-1" {
		t.Fatalf("message id = %q, want msg-1", chunk.MessageID)
	}
	vendor, _ := chunk.Meta["vendor"].(map[string]any)
	if vendor["trace"] != "abc" {
		t.Fatalf("chunk meta = %#v, want vendor trace", chunk.Meta)
	}
	chunk.Meta["vendor"].(map[string]any)["trace"] = "mutated"
	original := env.Update.(schema.ContentChunk)
	originalVendor, _ := original.Meta["vendor"].(map[string]any)
	if originalVendor["trace"] != "abc" {
		t.Fatalf("original meta mutated = %#v", original.Meta)
	}
}

func assertGoldenJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatalf("json.MarshalIndent() error = %v", err)
	}
	data = append(data, '\n')
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) error = %v", path, err)
	}
	if string(data) != string(want) {
		t.Fatalf("golden mismatch for %s\ngot:\n%s\nwant:\n%s", path, data, want)
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
