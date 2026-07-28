package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func TestSubagentApplyResultKeepsFailureDiagnosticTerminalOnly(t *testing.T) {
	task := &subagentTask{result: map[string]any{"error": "previous failure"}}

	task.applyResult(delegation.Result{
		State:   delegation.StateRunning,
		Running: true,
		Error:   "must not leak while running",
	})
	if _, exists := task.result["error"]; exists {
		t.Fatalf("running result retained error: %#v", task.result)
	}

	task.applyResult(delegation.Result{
		State: delegation.StateFailed,
		Error: "current failure",
	})
	if got := taskStringValue(task.result["error"]); got != "current failure" {
		t.Fatalf("failed result error = %q, want current failure", got)
	}
	for _, key := range []string{"result", "final_message", "output_preview"} {
		if _, exists := task.result[key]; exists {
			t.Fatalf("failed result contains %q: %#v", key, task.result)
		}
	}

	task.applyResult(delegation.Result{
		State:         delegation.StateInterrupted,
		OutputPreview: "stale child activity",
		Result:        "must not become a final message",
	})
	if got := taskStringValue(task.result["error"]); got != "subagent interrupted" {
		t.Fatalf("interrupted fallback error = %q, want fixed diagnostic", got)
	}
	for _, key := range []string{"result", "final_message", "output_preview"} {
		if _, exists := task.result[key]; exists {
			t.Fatalf("interrupted result contains %q: %#v", key, task.result)
		}
	}

	task.applyResult(delegation.Result{
		State:  delegation.StateCompleted,
		Error:  "must not leak after completion",
		Result: "done",
	})
	if _, exists := task.result["error"]; exists {
		t.Fatalf("completed result retained stale error: %#v", task.result)
	}
	if got := taskStringValue(task.result["final_message"]); got != "done" {
		t.Fatalf("completed final_message = %q, want done", got)
	}
}

func TestNormalizeSubagentCancelledClearsStaleFailureAndOutputFields(t *testing.T) {
	result := map[string]any{
		"state":          string(task.StateFailed),
		"error":          "previous failure",
		"result":         "previous result",
		"final_message":  "previous final message",
		"output_preview": "previous activity",
		"handle":         "reviewer",
	}

	normalizeSubagentResultForState(&result, task.StateCancelled, "")

	if got := taskStringValue(result["state"]); got != string(task.StateCancelled) {
		t.Fatalf("state = %q, want cancelled", got)
	}
	for _, key := range []string{"error", "result", "final_message", "output_preview"} {
		if _, exists := result[key]; exists {
			t.Fatalf("cancelled result retained %q: %#v", key, result)
		}
	}
	if got := taskStringValue(result["handle"]); got != "reviewer" {
		t.Fatalf("unrelated result metadata = %q, want reviewer", got)
	}
}

func TestSubagentTerminalStreamStateUsesFailureDiagnosticContract(t *testing.T) {
	tests := []struct {
		name       string
		state      task.State
		diagnostic string
	}{
		{name: "failed", state: task.StateFailed, diagnostic: "subagent failed"},
		{name: "interrupted", state: task.StateInterrupted, diagnostic: "subagent interrupted"},
		{name: "unknown outcome", state: task.StateUnknownOutcome, diagnostic: "subagent outcome could not be confirmed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			subtask := &subagentTask{
				ref:     task.Ref{TaskID: "task-stream-state"},
				state:   task.StateRunning,
				running: true,
				result: map[string]any{
					"result":         "previous completed turn",
					"final_message":  "previous completed turn",
					"output_preview": "stale activity",
				},
			}
			subtask.applyStreamFrames([]stream.Frame{{
				State:   string(test.state),
				Running: false,
				Closed:  true,
			}})
			snapshot := subtask.snapshot()
			if snapshot.State != test.state || snapshot.Running {
				t.Fatalf("snapshot = state %q running %v, want %q false", snapshot.State, snapshot.Running, test.state)
			}
			if got := taskStringValue(snapshot.Result["error"]); got != test.diagnostic {
				t.Fatalf("snapshot error = %q, want %q", got, test.diagnostic)
			}
			for _, key := range []string{"result", "final_message", "output_preview"} {
				if _, exists := snapshot.Result[key]; exists {
					t.Fatalf("terminal stream state retained %q: %#v", key, snapshot.Result)
				}
			}
		})
	}
}

func TestFailedSubagentDiagnosticSurvivesCanonicalTaskSyncAndRehydrate(t *testing.T) {
	ctx := context.Background()
	const failure = "subagent prompt failed"
	runner := &recordingSubagentRunner{
		spawnResult: delegation.Result{
			State:   delegation.StateRunning,
			Running: true,
		},
		waitResult: delegation.Result{
			State:         delegation.StateFailed,
			OutputPreview: "child exited before first update",
			Error:         failure,
		},
	}
	runtime, activeSession := newSubagentTaskTestRuntime(t, runner)

	started, err := runtime.tasks.StartSubagent(ctx, activeSession, activeSession.SessionRef, runner, task.SubagentStartRequest{
		Agent:  "helper",
		Prompt: "review",
		Source: "agent_spawn",
	})
	if err != nil {
		t.Fatalf("StartSubagent() error = %v", err)
	}
	snapshot, err := runtime.tasks.Wait(ctx, activeSession.SessionRef, task.ControlRequest{
		TaskID:    started.Ref.TaskID,
		Principal: session.ActorKindTool,
	})
	if err != nil {
		t.Fatalf("Wait() error = %v", err)
	}
	if snapshot.State != task.StateFailed || snapshot.Running {
		t.Fatalf("snapshot = state %q running %v, want terminal failed", snapshot.State, snapshot.Running)
	}
	payload := taskToolPayload(snapshot)
	if got := taskStringValue(payload["error"]); got != failure {
		t.Fatalf("Task payload error = %q, want durable failure", got)
	}
	if _, exists := payload["final_message"]; exists {
		t.Fatalf("Task payload treated failure as final assistant output: %#v", payload)
	}
	err = runtime.tasks.syncCanonicalToolResult(ctx, activeSession.SessionRef, &session.Event{
		Type: session.EventTypeToolResult,
		Meta: taskToolMeta(snapshot),
		Tool: &session.EventTool{
			Name:   "TASK",
			Status: "completed",
			Output: payload,
		},
	})
	if err != nil {
		t.Fatalf("syncCanonicalToolResult() error = %v", err)
	}
	entry, err := runtime.tasks.store.Get(ctx, snapshot.Ref.TaskID)
	if err != nil {
		t.Fatalf("task store Get(after sync) error = %v", err)
	}
	if got := taskStringValue(entry.Result["error"]); got != failure {
		t.Fatalf("stored error = %q, want durable failure", got)
	}
	for _, key := range []string{"result", "final_message", "output_preview"} {
		if _, exists := entry.Result[key]; exists {
			t.Fatalf("stored failure contains %q: %#v", key, entry.Result)
		}
	}

	rehydrated := runtime.tasks.rehydrateSubagentTask(entry).snapshot()
	if got := taskStringValue(rehydrated.Result["error"]); got != failure {
		t.Fatalf("rehydrated error = %q, want durable failure", got)
	}
	if _, exists := rehydrated.Result["final_message"]; exists {
		t.Fatalf("rehydrated failure became final assistant output: %#v", rehydrated.Result)
	}

	// Canonical synchronization remains a pure replacement. Compatibility
	// producers that omit a typed Runtime diagnostic receive the fixed fallback
	// instead of inheriting a previous error through the shared apply path.
	compatibility := task.CloneEntry(entry)
	compatibility.FailureDiagnostic = ""
	applyCanonicalTaskEntry(compatibility, map[string]any{
		"handle": taskPublicHandle(snapshot),
		"state":  string(task.StateFailed),
	}, "completed", time.Now())
	if got := taskStringValue(compatibility.Result["error"]); got != "subagent failed" {
		t.Fatalf("canonical fallback error = %q, want subagent failed", got)
	}
	if compatibility.FailureDiagnostic != "subagent failed" {
		t.Fatalf("canonical typed diagnostic = %q, want subagent failed", compatibility.FailureDiagnostic)
	}
}

func TestSubagentTaskPayloadOwnsFailureDiagnostic(t *testing.T) {
	const rawLegacyError = "Bearer direct-secret api_key=direct-key /Users/alice/private"
	result := map[string]any{
		"output_preview": "stale activity",
		"final_message":  "previous completed turn",
		"error":          rawLegacyError,
	}
	normalizeSubagentResultForState(&result, task.StateUnknownOutcome, "")
	payload := taskToolPayload(task.Snapshot{
		Handle:  "reviewer",
		Kind:    task.KindSubagent,
		State:   task.StateUnknownOutcome,
		Running: true,
		Result:  result,
	})
	if got := taskStringValue(payload["error"]); got != "subagent outcome could not be confirmed" {
		t.Fatalf("payload error = %q, want fixed unknown-outcome diagnostic", got)
	}
	for _, key := range []string{"output_preview", "final_message"} {
		if _, exists := payload[key]; exists {
			t.Fatalf("failure payload contains %q: %#v", key, payload)
		}
	}
	if exposed := fmt.Sprint(payload); strings.Contains(exposed, "direct-secret") ||
		strings.Contains(exposed, "direct-key") || strings.Contains(exposed, "/Users/alice") {
		t.Fatalf("failure payload exposed unmarked legacy error: %s", exposed)
	}
}

func TestLegacySubagentFailureErrorIsNotPromotedAcrossRehydrate(t *testing.T) {
	tests := []struct {
		name       string
		state      task.State
		rawError   string
		secrets    []string
		diagnostic string
	}{
		{
			name:       "failed bearer",
			state:      task.StateFailed,
			rawError:   "request failed: Authorization: Bearer legacy-token at /Users/alice/work",
			secrets:    []string{"legacy-token", "/Users/alice"},
			diagnostic: "subagent failed",
		},
		{
			name:       "interrupted api key",
			state:      task.StateInterrupted,
			rawError:   "transport interrupted: api_key=legacy-api-key at /private/tmp/child.sock",
			secrets:    []string{"legacy-api-key", "/private/tmp/child.sock"},
			diagnostic: "subagent interrupted",
		},
		{
			name:       "unknown outcome path",
			state:      task.StateUnknownOutcome,
			rawError:   "unknown effect: sk-legacy-secret in /home/service/.config",
			secrets:    []string{"sk-legacy-secret", "/home/service/.config"},
			diagnostic: "subagent outcome could not be confirmed",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			runtime, activeSession := newSubagentTaskTestRuntime(t, &recordingSubagentRunner{})
			taskID := "legacy-" + strings.ReplaceAll(test.name, " ", "-")
			entry := &task.Entry{
				TaskID:  taskID,
				Handle:  taskID,
				Kind:    task.KindSubagent,
				Session: activeSession.SessionRef,
				State:   test.state,
				Spec: map[string]any{
					"agent":      "helper",
					"handle":     taskID,
					"session_id": "child-" + taskID,
					"agent_id":   "agent-" + taskID,
				},
				Result: map[string]any{
					"state":         string(test.state),
					"error":         test.rawError,
					"result":        test.rawError,
					"final_message": test.rawError,
				},
			}
			if err := runtime.tasks.store.Upsert(ctx, entry); err != nil {
				t.Fatalf("store Upsert() error = %v", err)
			}
			stored, err := runtime.tasks.store.Get(ctx, taskID)
			if err != nil || taskStringValue(stored.Result["error"]) != test.rawError {
				t.Fatalf("legacy store round-trip = %#v, %v; want raw fixture retained before trust boundary", stored, err)
			}

			subtask, err := runtime.tasks.lookupSubagent(ctx, activeSession.SessionRef, taskID)
			if err != nil {
				t.Fatalf("lookupSubagent() error = %v", err)
			}
			snapshot := subtask.snapshot()
			payload := taskToolPayload(snapshot)
			if got := taskStringValue(snapshot.Result["error"]); got != test.diagnostic {
				t.Fatalf("rehydrated error = %q, want %q", got, test.diagnostic)
			}
			if got := taskStringValue(payload["error"]); got != test.diagnostic {
				t.Fatalf("Task payload error = %q, want %q", got, test.diagnostic)
			}
			for _, key := range []string{"result", "final_message", "output_preview"} {
				if _, exists := snapshot.Result[key]; exists {
					t.Fatalf("rehydrated failure retained %q: %#v", key, snapshot.Result)
				}
				if _, exists := payload[key]; exists {
					t.Fatalf("Task payload retained %q: %#v", key, payload)
				}
			}
			exposed := fmt.Sprint(snapshot.Result, payload)
			for _, secret := range test.secrets {
				if strings.Contains(exposed, secret) {
					t.Fatalf("legacy secret %q crossed rehydrate boundary: %s", secret, exposed)
				}
			}
		})
	}
}

func TestLegacyNonFailureSubagentErrorIsDroppedFromRehydratePayloadAndStream(t *testing.T) {
	tests := []struct {
		name    string
		state   task.State
		running bool
		result  map[string]any
	}{
		{
			name:    "running",
			state:   task.StateRunning,
			running: true,
			result:  map[string]any{"output_preview": "working"},
		},
		{
			name:   "completed",
			state:  task.StateCompleted,
			result: map[string]any{"result": "completed answer", "final_message": "completed answer"},
		},
		{
			name:   "cancelled",
			state:  task.StateCancelled,
			result: map[string]any{"result": "cancelled", "final_message": "cancelled"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			runtime, activeSession := newSubagentTaskTestRuntime(t, &recordingSubagentRunner{})
			taskID := "legacy-nonfailure-" + test.name
			const rawError = "Authorization: Bearer nonfailure-secret api_key=nonfailure-key at /Users/alice/private"
			result := session.CloneState(test.result)
			result["state"] = string(test.state)
			result["error"] = rawError

			directPayload := taskToolPayload(task.Snapshot{
				Handle: taskID, Kind: task.KindSubagent, State: test.state, Running: test.running, Result: result,
			})
			if exposed := fmt.Sprint(directPayload); strings.Contains(exposed, "nonfailure-secret") ||
				strings.Contains(exposed, "nonfailure-key") || strings.Contains(exposed, "/Users/alice") {
				t.Fatalf("direct Task payload exposed non-failure error: %s", exposed)
			}
			if _, exists := directPayload["error"]; exists {
				t.Fatalf("direct Task payload retained non-failure error: %#v", directPayload)
			}

			if err := runtime.tasks.store.Upsert(ctx, &task.Entry{
				TaskID:  taskID,
				Handle:  taskID,
				Kind:    task.KindSubagent,
				Session: activeSession.SessionRef,
				State:   test.state,
				Running: test.running,
				Spec: map[string]any{
					"agent":      "helper",
					"handle":     taskID,
					"session_id": "child-" + taskID,
					"agent_id":   "agent-" + taskID,
				},
				Result: result,
			}); err != nil {
				t.Fatalf("store Upsert() error = %v", err)
			}
			subtask, err := runtime.tasks.lookupSubagent(ctx, activeSession.SessionRef, taskID)
			if err != nil {
				t.Fatalf("lookupSubagent() error = %v", err)
			}
			snapshot := subtask.snapshot()
			if _, exists := snapshot.Result["error"]; exists {
				t.Fatalf("rehydrated non-failure retained error: %#v", snapshot.Result)
			}
			payload := taskToolPayload(snapshot)
			streamSnapshot, err := runtime.Streams().Read(ctx, stream.ReadRequest{
				Ref: stream.Ref{SessionID: activeSession.SessionID, TaskID: taskID},
			})
			if err != nil {
				t.Fatalf("Streams().Read() error = %v", err)
			}
			exposed := fmt.Sprint(snapshot.Result, payload, streamSnapshot.FinalText)
			for _, secret := range []string{"nonfailure-secret", "nonfailure-key", "/Users/alice"} {
				if strings.Contains(exposed, secret) {
					t.Fatalf("legacy non-failure secret %q crossed read boundary: %s", secret, exposed)
				}
			}
			if _, exists := payload["error"]; exists {
				t.Fatalf("Task payload retained non-failure error: %#v", payload)
			}
		})
	}
}

func TestCanonicalSubagentFailureDoesNotPromoteUntypedDiagnostic(t *testing.T) {
	for _, test := range []struct {
		name     string
		backfill bool
	}{
		{name: "sync"},
		{name: "backfill", backfill: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			runtime, activeSession := newSubagentTaskTestRuntime(t, &recordingSubagentRunner{})
			taskID := "compat-failed-" + test.name
			const rawError = "Authorization: Bearer canonical-secret api_key=canonical-key at /Users/alice/private"
			if err := runtime.tasks.store.Upsert(ctx, &task.Entry{
				TaskID:    taskID,
				Handle:    taskID,
				Kind:      task.KindSubagent,
				Session:   activeSession.SessionRef,
				State:     task.StateFailed,
				UpdatedAt: time.Now().Add(-time.Minute),
				Result:    map[string]any{"state": string(task.StateFailed), "error": rawError},
			}); err != nil {
				t.Fatalf("store Upsert() error = %v", err)
			}
			event := &session.Event{
				Type: session.EventTypeToolResult,
				Tool: &session.EventTool{
					Name:   "TASK",
					Status: "completed",
					Output: map[string]any{
						"task_id": taskID,
						"handle":  taskID,
						"state":   string(task.StateFailed),
						"error":   rawError,
					},
				},
			}
			if test.backfill {
				if _, err := runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{
					SessionRef: activeSession.SessionRef,
					Event:      event,
				}); err != nil {
					t.Fatalf("AppendEvent() error = %v", err)
				}
				if _, err := runtime.tasks.lookupSubagent(ctx, activeSession.SessionRef, taskID); err != nil {
					t.Fatalf("lookupSubagent() error = %v", err)
				}
			} else if err := runtime.tasks.syncCanonicalToolResult(ctx, activeSession.SessionRef, event); err != nil {
				t.Fatalf("syncCanonicalToolResult() error = %v", err)
			}
			entry, err := runtime.tasks.store.Get(ctx, taskID)
			if err != nil {
				t.Fatalf("store Get() error = %v", err)
			}
			if got := taskStringValue(entry.Result["error"]); got != "subagent failed" {
				t.Fatalf("canonical error = %q, want fixed fallback", got)
			}
			if entry.FailureDiagnostic != "subagent failed" {
				t.Fatalf("typed diagnostic = %q, want fixed fallback", entry.FailureDiagnostic)
			}
			exposed := fmt.Sprint(entry.Result, entry.FailureDiagnostic)
			for _, secret := range []string{"canonical-secret", "canonical-key", "/Users/alice"} {
				if strings.Contains(exposed, secret) {
					t.Fatalf("canonical %s promoted untyped secret %q: %s", test.name, secret, exposed)
				}
			}
		})
	}
}

func TestCanonicalNonFailureSubagentDropsLegacyError(t *testing.T) {
	tests := []struct {
		name     string
		state    task.State
		backfill bool
	}{
		{name: "sync completed", state: task.StateCompleted},
		{name: "sync cancelled", state: task.StateCancelled},
		{name: "backfill completed", state: task.StateCompleted, backfill: true},
		{name: "backfill cancelled", state: task.StateCancelled, backfill: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			runtime, activeSession := newSubagentTaskTestRuntime(t, &recordingSubagentRunner{})
			taskID := "legacy-canonical-" + strings.ReplaceAll(test.name, " ", "-")
			const (
				rawError    = "Authorization: Bearer canonical-secret api_key=canonical-key at /Users/alice/private"
				finalOutput = "safe canonical output"
			)
			if err := runtime.tasks.store.Upsert(ctx, &task.Entry{
				TaskID:  taskID,
				Handle:  taskID,
				Kind:    task.KindSubagent,
				Session: activeSession.SessionRef,
				State:   test.state,
				Spec: map[string]any{
					"agent":      "helper",
					"handle":     taskID,
					"session_id": "child-" + taskID,
					"agent_id":   "agent-" + taskID,
				},
				Result: map[string]any{
					"state": string(test.state),
					"error": rawError,
				},
				UpdatedAt: time.Now().Add(-time.Minute),
			}); err != nil {
				t.Fatalf("store Upsert() error = %v", err)
			}

			event := &session.Event{
				Type: session.EventTypeToolResult,
				Tool: &session.EventTool{
					Name:   "TASK",
					Status: "completed",
					Output: map[string]any{
						"task_id":       taskID,
						"handle":        taskID,
						"state":         string(test.state),
						"error":         rawError,
						"final_message": finalOutput,
					},
				},
			}
			if test.backfill {
				if _, err := runtime.sessions.AppendEvent(ctx, session.AppendEventRequest{
					SessionRef: activeSession.SessionRef,
					Event:      event,
				}); err != nil {
					t.Fatalf("AppendEvent() error = %v", err)
				}
			} else if err := runtime.tasks.syncCanonicalToolResult(ctx, activeSession.SessionRef, event); err != nil {
				t.Fatalf("syncCanonicalToolResult() error = %v", err)
			}

			subtask, err := runtime.tasks.lookupSubagent(ctx, activeSession.SessionRef, taskID)
			if err != nil {
				t.Fatalf("lookupSubagent() error = %v", err)
			}
			stored, err := runtime.tasks.store.Get(ctx, taskID)
			if err != nil {
				t.Fatalf("store Get() error = %v", err)
			}
			snapshot := subtask.snapshot()
			payload := taskToolPayload(snapshot)
			for label, result := range map[string]map[string]any{
				"stored":   stored.Result,
				"snapshot": snapshot.Result,
				"payload":  payload,
			} {
				if _, exists := result["error"]; exists {
					t.Fatalf("%s retained legacy non-failure error: %#v", label, result)
				}
			}
			exposed := fmt.Sprint(stored.Result, snapshot.Result, payload)
			for _, secret := range []string{"canonical-secret", "canonical-key", "/Users/alice"} {
				if strings.Contains(exposed, secret) {
					t.Fatalf("canonical %s exposed %q: %s", test.name, secret, exposed)
				}
			}
		})
	}
}

func TestCanonicalSubagentBatchNormalizesEachFailureIndependently(t *testing.T) {
	failedResult := map[string]any{}
	unknownResult := map[string]any{}
	normalizeSubagentResultForState(&failedResult, task.StateFailed, "subagent prompt failed")
	normalizeSubagentResultForState(
		&unknownResult,
		task.StateUnknownOutcome,
		subagentContinueUnknownDiagnostic,
	)
	items := []taskBatchControlItem{
		{
			Handle: "failed-child",
			OK:     true,
			Snapshot: task.Snapshot{
				Handle: "failed-child", Kind: task.KindSubagent,
				State: task.StateFailed, Result: failedResult,
			},
		},
		{
			Handle: "unknown-child",
			OK:     true,
			Snapshot: task.Snapshot{
				Handle: "unknown-child", Kind: task.KindSubagent,
				State: task.StateUnknownOutcome, Result: unknownResult,
			},
		},
	}
	payload := taskBatchControlPayload(items, "wait", 0)
	outputs, ok := canonicalTaskBatchOutputs(payload["tasks"])
	if !ok || len(outputs) != 2 {
		t.Fatalf("canonical batch outputs = %#v, want two items", outputs)
	}
	got := []string{
		taskStringValue(canonicalSubagentTaskOutput(
			outputs[0], "completed", task.StateFailed, "subagent prompt failed",
		)["error"]),
		taskStringValue(canonicalSubagentTaskOutput(
			outputs[1], "completed", task.StateUnknownOutcome, subagentContinueUnknownDiagnostic,
		)["error"]),
	}
	want := []string{"subagent prompt failed", subagentContinueUnknownDiagnostic}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("canonical batch diagnostic[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
