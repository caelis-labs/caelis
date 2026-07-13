package eventstream

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
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

func TestApprovalRequestIDStaysOnEnvelopeOutsideACPWirePayload(t *testing.T) {
	t.Parallel()

	env := Envelope{
		Kind:              KindRequestPermission,
		ApprovalRequestID: "approval-1",
		Permission: &schema.RequestPermissionRequest{
			SessionID: "session-1",
			ToolCall: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "call-1",
			},
		},
	}
	clone := CloneEnvelope(env)
	if clone.ApprovalRequestID != env.ApprovalRequestID {
		t.Fatalf("CloneEnvelope().ApprovalRequestID = %q, want %q", clone.ApprovalRequestID, env.ApprovalRequestID)
	}
	permissionJSON, err := json.Marshal(env.Permission)
	if err != nil {
		t.Fatalf("json.Marshal(permission) error = %v", err)
	}
	if strings.Contains(string(permissionJSON), "approval_request_id") {
		t.Fatalf("ACP permission wire payload = %s, must not contain Caelis request identity", permissionJSON)
	}
	envelopeJSON, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(envelope) error = %v", err)
	}
	if !strings.Contains(string(envelopeJSON), `"approval_request_id":"approval-1"`) {
		t.Fatalf("Envelope JSON = %s, want top-level approval request identity", envelopeJSON)
	}
}

func TestEnvelopeV1SessionUpdateGolden(t *testing.T) {
	t.Parallel()

	env := Envelope{
		Kind:         KindSessionUpdate,
		Cursor:       "acp-projection:ZXZlbnQtMQ:0",
		EventID:      "event-1",
		ProjectionID: "acp-projection:ZXZlbnQtMQ:0",
		SessionID:    "session-1",
		HandleID:     "handle-1",
		RunID:        "run-1",
		TurnID:       "turn-1",
		OccurredAt:   time.Date(2026, 6, 27, 12, 0, 0, 0, time.UTC),
		Scope:        ScopeMain,
		Final:        true,
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
		Kind:              KindRequestPermission,
		Cursor:            "turn-1:0002",
		SessionID:         "session-1",
		HandleID:          "handle-1",
		RunID:             "run-1",
		TurnID:            "turn-1",
		OccurredAt:        time.Date(2026, 6, 27, 12, 0, 1, 0, time.UTC),
		Scope:             ScopeMain,
		ApprovalRequestID: "approval-1",
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

func TestEnvelopeV1ToolCallGolden(t *testing.T) {
	t.Parallel()

	line := 7
	env := Envelope{
		Kind:       KindSessionUpdate,
		Cursor:     "turn-1:0003",
		SessionID:  "session-1",
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		OccurredAt: time.Date(2026, 6, 27, 12, 0, 2, 0, time.UTC),
		Scope:      ScopeMain,
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "call-1",
			Title:         "Read file",
			Kind:          schema.ToolKindRead,
			Status:        schema.ToolStatusPending,
			RawInput:      map[string]any{"path": "main.go"},
			Locations:     []schema.ToolCallLocation{{Path: "main.go", Line: &line}},
			Meta:          map[string]any{"vendor": map[string]any{"trace": "abc"}},
		},
	}
	assertGoldenJSON(t, "testdata/envelope_v1_tool_call.golden.json", env)
}

func TestEnvelopeV1ToolCallUpdateGolden(t *testing.T) {
	t.Parallel()

	title := "Run tests"
	kind := schema.ToolKindExecute
	status := schema.ToolStatusInProgress
	env := Envelope{
		Kind:       KindSessionUpdate,
		Cursor:     "turn-1:0004",
		SessionID:  "session-1",
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		OccurredAt: time.Date(2026, 6, 27, 12, 0, 3, 0, time.UTC),
		Scope:      ScopeMain,
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "call-1",
			Title:         &title,
			Kind:          &kind,
			Status:        &status,
			RawOutput:     map[string]any{"stdout": "ok\n"},
			Content: []schema.ToolCallContent{{
				Type:       "terminal",
				TerminalID: "term-1",
				Content:    schema.TextContent{Type: "text", Text: "ok\n"},
			}},
			Meta: map[string]any{
				"caelis": map[string]any{
					"runtime": map[string]any{
						"tool": map[string]any{"status_detail": "streaming"},
					},
				},
			},
		},
	}
	assertGoldenJSON(t, "testdata/envelope_v1_tool_call_update.golden.json", env)
}

func TestEnvelopeV1SpawnChildSemanticGolden(t *testing.T) {
	t.Parallel()

	env := Envelope{
		Kind:       KindSessionUpdate,
		Cursor:     "stream:child-1",
		SessionID:  "session-1",
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		OccurredAt: time.Date(2026, 7, 12, 10, 0, 0, 0, time.UTC),
		Scope:      ScopeSubagent,
		ScopeID:    "task-1",
		Actor:      "reviewer",
		ParentTool: &ParentToolRelation{
			ToolCallID: "spawn-call-1",
			ToolName:   "Spawn",
		},
		Delivery: &Delivery{
			Transient:           true,
			HasParentToolMirror: true,
		},
		Update: schema.ToolCall{
			SessionUpdate: schema.UpdateToolCall,
			ToolCallID:    "child-read-1",
			Title:         "Read README.md",
			Kind:          schema.ToolKindRead,
			Status:        schema.ToolStatusCompleted,
			RawInput:      map[string]any{"path": "README.md"},
		},
	}
	assertGoldenJSON(t, "testdata/envelope_v1_spawn_child_semantic.golden.json", env)
	assertEnvelopeRelationOutsideACPUpdate(t, env)
}

func TestEnvelopeV1SpawnParentToolMirrorGolden(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusCompleted
	env := Envelope{
		Kind:       KindSessionUpdate,
		Cursor:     "stream:spawn-mirror-1",
		SessionID:  "session-1",
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		OccurredAt: time.Date(2026, 7, 12, 10, 0, 1, 0, time.UTC),
		Scope:      ScopeMain,
		Delivery: &Delivery{
			Transient:          true,
			IsParentToolMirror: true,
		},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "spawn-call-1",
			Status:        &status,
			Content: []schema.ToolCallContent{{
				Type:       "terminal",
				TerminalID: "spawn-call-1",
			}},
			Meta: metautil.WithTerminalOutput(nil, "spawn-call-1", "child result\n"),
		},
	}
	assertGoldenJSON(t, "testdata/envelope_v1_spawn_parent_tool_mirror.golden.json", env)
	assertEnvelopeRelationOutsideACPUpdate(t, env)
}

func TestEnvelopeV1RunCommandTransientGolden(t *testing.T) {
	t.Parallel()

	status := schema.ToolStatusInProgress
	env := Envelope{
		Kind:       KindSessionUpdate,
		Cursor:     "stream:command-1",
		SessionID:  "session-1",
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		OccurredAt: time.Date(2026, 7, 12, 10, 0, 2, 0, time.UTC),
		Scope:      ScopeMain,
		Delivery:   &Delivery{Transient: true},
		Update: schema.ToolCallUpdate{
			SessionUpdate: schema.UpdateToolCallInfo,
			ToolCallID:    "command-call-1",
			Status:        &status,
			Content: []schema.ToolCallContent{{
				Type:       "terminal",
				TerminalID: "command-call-1",
			}},
		},
	}
	assertGoldenJSON(t, "testdata/envelope_v1_run_command_transient.golden.json", env)
	assertEnvelopeRelationOutsideACPUpdate(t, env)
}

func TestEnvelopeV1PlanGolden(t *testing.T) {
	t.Parallel()

	env := Envelope{
		Kind:       KindSessionUpdate,
		Cursor:     "turn-1:0005",
		SessionID:  "session-1",
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		OccurredAt: time.Date(2026, 6, 27, 12, 0, 4, 0, time.UTC),
		Scope:      ScopeMain,
		Update: schema.PlanUpdate{
			SessionUpdate: schema.UpdatePlan,
			Entries: []schema.PlanEntry{{
				Content:  "Inspect replay",
				Status:   "completed",
				Priority: "high",
			}},
		},
	}
	assertGoldenJSON(t, "testdata/envelope_v1_plan.golden.json", env)
}

func TestEnvelopeV1ACPUsageUpdateGolden(t *testing.T) {
	t.Parallel()

	env := Envelope{
		Kind:       KindSessionUpdate,
		Cursor:     "turn-1:0007",
		SessionID:  "session-1",
		HandleID:   "handle-1",
		RunID:      "run-1",
		TurnID:     "turn-1",
		OccurredAt: time.Date(2026, 6, 27, 12, 0, 6, 0, time.UTC),
		Scope:      ScopeMain,
		Update: schema.UsageUpdate{
			SessionUpdate: schema.UpdateUsage,
			Size:          200000,
			Used:          42000,
			Cost:          &schema.UsageCost{Total: 0.47, Currency: "USD"},
			Meta:          map[string]any{"vendor": map[string]any{"trace": "abc"}},
		},
	}
	assertGoldenJSON(t, "testdata/envelope_v1_acp_usage_update.golden.json", env)
}

func TestUsageUpdateFromSnapshotStoresBreakdownInMeta(t *testing.T) {
	t.Parallel()

	update := UsageUpdateFromSnapshot(UsageSnapshot{
		PromptTokens:        12,
		CachedInputTokens:   3,
		CompletionTokens:    5,
		ReasoningTokens:     2,
		TotalTokens:         17,
		ContextWindowTokens: 200000,
	}, map[string]any{"caelis": map[string]any{"invocation": map[string]any{"model": "m"}}})
	if update.SessionUpdate != schema.UpdateUsage {
		t.Fatalf("SessionUpdate = %q, want usage_update", update.SessionUpdate)
	}
	if update.Used != 17 || update.Size != 200000 {
		t.Fatalf("usage size/used = %d/%d, want context window size and used total", update.Size, update.Used)
	}
	roundTripped := UsageSnapshotFromUpdate(update)
	if roundTripped == nil || roundTripped.PromptTokens != 12 || roundTripped.CachedInputTokens != 3 || roundTripped.CompletionTokens != 5 || roundTripped.ReasoningTokens != 2 || roundTripped.TotalTokens != 17 || roundTripped.ContextWindowTokens != 200000 {
		t.Fatalf("UsageSnapshotFromUpdate() = %#v", roundTripped)
	}
	caelis, _ := update.Meta["caelis"].(map[string]any)
	if caelis["version"] != 1 {
		t.Fatalf("meta.caelis.version = %#v, want 1", caelis["version"])
	}
	invocation, _ := caelis["invocation"].(map[string]any)
	if invocation["model"] != "m" {
		t.Fatalf("meta.caelis.invocation = %#v, want preserved model", invocation)
	}
}

func TestUsageUpdateFromSnapshotDefaultsUnknownSizeToUsed(t *testing.T) {
	t.Parallel()

	update := UsageUpdateFromSnapshot(UsageSnapshot{TotalTokens: 17}, nil)
	if update.Used != 17 || update.Size != 17 {
		t.Fatalf("usage size/used = %d/%d, want required size fallback to used total", update.Size, update.Used)
	}
}

func TestUsageUpdateFromSnapshotDoesNotPreserveStaleUsageMeta(t *testing.T) {
	t.Parallel()

	update := UsageUpdateFromSnapshot(UsageSnapshot{
		PromptTokens: 12,
		TotalTokens:  17,
	}, map[string]any{
		"caelis": map[string]any{
			"invocation": map[string]any{"model": "m"},
			"usage": map[string]any{
				"completion_tokens": 99,
				"reasoning_tokens":  88,
			},
		},
	})
	usage := UsageSnapshotFromUpdate(update)
	if usage == nil || usage.PromptTokens != 12 || usage.TotalTokens != 17 {
		t.Fatalf("UsageSnapshotFromUpdate() = %#v, want current prompt/total tokens", usage)
	}
	if usage.CompletionTokens != 0 || usage.ReasoningTokens != 0 {
		t.Fatalf("UsageSnapshotFromUpdate() = %#v, want stale usage fields removed", usage)
	}
	caelis, _ := update.Meta["caelis"].(map[string]any)
	if _, ok := caelis["invocation"].(map[string]any); !ok {
		t.Fatalf("meta.caelis = %#v, want invocation sibling preserved", caelis)
	}
	usageMeta, _ := caelis["usage"].(map[string]any)
	if usageMeta["completion_tokens"] != nil || usageMeta["reasoning_tokens"] != nil {
		t.Fatalf("meta.caelis.usage = %#v, want stale fields removed", usageMeta)
	}
}

func TestEnvelopeV1ParticipantGolden(t *testing.T) {
	t.Parallel()

	env := Envelope{
		Kind:          KindParticipant,
		Cursor:        "turn-1:0008",
		SessionID:     "session-1",
		TurnID:        "turn-1",
		OccurredAt:    time.Date(2026, 6, 27, 12, 0, 7, 0, time.UTC),
		Scope:         ScopeParticipant,
		ScopeID:       "participant-1",
		Actor:         "@reviewer",
		ParticipantID: "participant-1",
		Participant:   &Participant{State: "attached"},
	}
	assertGoldenJSON(t, "testdata/envelope_v1_participant.golden.json", env)
}

func TestEnvelopeV1LifecycleGolden(t *testing.T) {
	t.Parallel()

	env := TurnCompleted("handle-1", "run-1", "turn-1", time.Date(2026, 6, 27, 12, 0, 8, 0, time.UTC))
	env.Cursor = "turn-1:0009"
	env.SessionID = "session-1"
	assertGoldenJSON(t, "testdata/envelope_v1_lifecycle.golden.json", env)
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

func TestCloneEnvelopeDeepCopiesRelationAndDelivery(t *testing.T) {
	t.Parallel()

	env := Envelope{
		ParentTool: &ParentToolRelation{ToolCallID: "spawn-call-1", ToolName: "Spawn"},
		Delivery: &Delivery{
			Transient:           true,
			HasParentToolMirror: true,
		},
	}
	cloned := CloneEnvelope(env)
	cloned.ParentTool.ToolCallID = "changed-call"
	cloned.Delivery.Transient = false
	cloned.Delivery.HasParentToolMirror = false
	cloned.Delivery.IsParentToolMirror = true

	if env.ParentTool.ToolCallID != "spawn-call-1" || env.ParentTool.ToolName != "Spawn" {
		t.Fatalf("original parent relation mutated = %#v", env.ParentTool)
	}
	if !env.Delivery.Transient || !env.Delivery.HasParentToolMirror || env.Delivery.IsParentToolMirror {
		t.Fatalf("original delivery mutated = %#v", env.Delivery)
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

func assertEnvelopeRelationOutsideACPUpdate(t *testing.T, env Envelope) {
	t.Helper()
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("json.Marshal(Envelope) error = %v", err)
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("json.Unmarshal(Envelope) error = %v", err)
	}
	if _, ok := root["parent_tool"]; env.ParentTool != nil && !ok {
		t.Fatalf("envelope json = %s, want parent_tool at envelope root", data)
	}
	if _, ok := root["delivery"]; env.Delivery != nil && !ok {
		t.Fatalf("envelope json = %s, want delivery at envelope root", data)
	}
	var update map[string]json.RawMessage
	if raw := root["update"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &update); err != nil {
			t.Fatalf("json.Unmarshal(update) error = %v", err)
		}
	}
	for _, field := range []string{"parent_tool", "delivery"} {
		if _, ok := update[field]; ok {
			t.Fatalf("ACP update unexpectedly contains %q: %s", field, root["update"])
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
