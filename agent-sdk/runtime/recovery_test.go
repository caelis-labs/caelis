package runtime

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	taskstream "github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func TestRuntimeRecoveryConvergesCommandRehydrateFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		phase      string
		wantState  taskapi.State
		wantPhase  string
		wantResult bool
	}{
		{
			name: "running becomes interrupted", phase: commandPhaseRunning,
			wantState: taskapi.StateInterrupted, wantPhase: commandPhaseRunning, wantResult: true,
		},
		{
			name: "claimed effect remains unknown", phase: commandPhaseEffectClaimed,
			wantState: taskapi.StateUnknownOutcome, wantPhase: commandPhaseUnknown,
		},
		{
			name: "claimed cancellation remains unknown", phase: commandPhaseCancelClaimed,
			wantState: taskapi.StateUnknownOutcome, wantPhase: commandPhaseCancelUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			ref := session.SessionRef{SessionID: "command-recovery-" + strings.ReplaceAll(tt.name, " ", "-")}
			store := newSagaTaskStore()
			entry, err := store.Put(ctx, taskapi.PutRequest{Entry: &taskapi.Entry{
				TaskID: "task-" + strings.ReplaceAll(tt.name, " ", "-"),
				Kind:   taskapi.KindCommand, Session: ref, State: taskapi.StateRunning, Running: true,
				Result: map[string]any{"state": string(taskapi.StateRunning)},
				Metadata: map[string]any{
					"state":                      string(taskapi.StateRunning),
					"running":                    true,
					"command_phase":              tt.phase,
					commandStreamEventCursorMeta: int64(math.MaxInt64),
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			runtime := &Runtime{}
			runtime.tasks = newTaskRuntime(runtime, store)

			if err := runtime.recoverRuntimeState(ctx, ref); err != nil {
				t.Fatalf("recoverRuntimeState() error = %v", err)
			}
			got, err := store.Get(ctx, entry.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			assertRecoveredCommandEntry(
				t, got, tt.wantState, tt.wantPhase,
				"command stream event cursor is exhausted", tt.wantResult,
			)

			beforeRevision := got.Revision
			if err := runtime.recoverRuntimeState(ctx, ref); err != nil {
				t.Fatalf("second recoverRuntimeState() error = %v", err)
			}
			again, err := store.Get(ctx, entry.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if again.Revision != beforeRevision {
				t.Fatalf("second recovery revision = %d, want stable %d", again.Revision, beforeRevision)
			}
		})
	}
}

func TestRuntimeRecoveryConvergesUnattachedCommandOutcome(t *testing.T) {
	t.Parallel()

	for _, phase := range []string{
		commandPhaseEffectClaimed,
		commandPhaseUnknown,
		commandPhaseCancelClaimed,
		commandPhaseCancelUnknown,
		commandPhaseCancelApplied,
	} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			ref := session.SessionRef{SessionID: "command-recovery-" + phase}
			store := newSagaTaskStore()
			entry, err := store.Put(ctx, taskapi.PutRequest{Entry: &taskapi.Entry{
				TaskID: "task-" + phase, Kind: taskapi.KindCommand, Session: ref,
				State: taskapi.StateRunning, Running: true,
				Result: map[string]any{"state": string(taskapi.StateRunning)},
				Metadata: map[string]any{
					"state":         string(taskapi.StateRunning),
					"running":       true,
					"command_phase": phase,
				},
			}})
			if err != nil {
				t.Fatal(err)
			}
			runtime := &Runtime{}
			runtime.tasks = newTaskRuntime(runtime, store)

			if err := runtime.recoverRuntimeState(ctx, ref); err != nil {
				t.Fatalf("recoverRuntimeState() error = %v", err)
			}
			got, err := store.Get(ctx, entry.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			wantPhase := commandPhaseUnknown
			if commandCancelPhase(phase) {
				wantPhase = commandPhaseCancelUnknown
			}
			assertRecoveredCommandEntry(
				t, got, taskapi.StateUnknownOutcome, wantPhase,
				"command effect outcome is unavailable after process restart", false,
			)
		})
	}
}

func TestRuntimeRecoveryConvergesInstalledUnattachedCommand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ref := session.SessionRef{SessionID: "command-recovery-installed-unattached"}
	store := newSagaTaskStore()
	entry, err := store.Put(ctx, taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: "task-installed-unattached", Kind: taskapi.KindCommand, Session: ref,
		State: taskapi.StateRunning, Running: true,
		Terminal: sandbox.TerminalRef{
			Backend: sandbox.BackendHost, SessionID: "lost-sandbox-session", TerminalID: "lost-terminal",
		},
		Result: map[string]any{"state": string(taskapi.StateRunning)},
		Metadata: map[string]any{
			"state":         string(taskapi.StateRunning),
			"running":       true,
			"command_phase": commandPhaseEffectClaimed,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{}
	runtime.tasks = newTaskRuntime(runtime, store)
	unattached, err := runtime.tasks.rehydrateCommandTask(entry)
	if err != nil {
		t.Fatalf("rehydrateCommandTask() error = %v", err)
	}
	if unattached == nil || !unattached.running || unattached.session != nil {
		t.Fatalf("rehydrated command = %#v, want running task without live session", unattached)
	}
	runtime.tasks.installCommandTask(unattached)

	if err := runtime.recoverRuntimeState(ctx, ref); err != nil {
		t.Fatalf("recoverRuntimeState() error = %v", err)
	}
	got, err := store.Get(ctx, entry.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	assertRecoveredCommandEntry(
		t, got, taskapi.StateUnknownOutcome, commandPhaseUnknown,
		"command effect outcome is unavailable after process restart", false,
	)
	unattached.mu.Lock()
	cachedState, cachedRunning := unattached.state, unattached.running
	cachedPhase := taskStringValue(unattached.metadata["command_phase"])
	unattached.mu.Unlock()
	if cachedState != taskapi.StateUnknownOutcome || cachedRunning || cachedPhase != commandPhaseUnknown {
		t.Fatalf("cached command after recovery = state %q running %v phase %q, want terminal unknown outcome", cachedState, cachedRunning, cachedPhase)
	}
	streamed, err := newStreamService(runtime.tasks).Read(ctx, taskstream.ReadRequest{
		Ref: taskstream.Ref{SessionID: ref.SessionID, TaskID: entry.TaskID},
	})
	if err != nil {
		t.Fatalf("stream Read() after cached recovery error = %v", err)
	}
	if streamed.State != string(taskapi.StateUnknownOutcome) || streamed.Running || !streamed.TerminalFramed {
		t.Fatalf("stream snapshot after cached recovery = %#v, want terminal unknown outcome", streamed)
	}
}

func TestRuntimeRecoveryDoesNotRewriteActiveCommand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ref := session.SessionRef{SessionID: "command-recovery-active"}
	store := newSagaTaskStore()
	entry, err := store.Put(ctx, taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: "task-active", Kind: taskapi.KindCommand, Session: ref,
		State: taskapi.StateRunning, Running: true,
		Result: map[string]any{"state": string(taskapi.StateRunning)},
		Metadata: map[string]any{
			"state":                      string(taskapi.StateRunning),
			"running":                    true,
			"command_phase":              commandPhaseEffectClaimed,
			commandStreamEventCursorMeta: int64(math.MaxInt64),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{}
	runtime.tasks = newTaskRuntime(runtime, store)
	runtime.tasks.tasks[entry.TaskID] = &commandTask{
		ref: taskapi.Ref{TaskID: entry.TaskID}, sessionRef: ref,
		session: newYieldProbeSandboxSession(),
		state:   taskapi.StateRunning, running: true,
	}

	if err := runtime.recoverRuntimeState(ctx, ref); err != nil {
		t.Fatalf("recoverRuntimeState() error = %v", err)
	}
	got, err := store.Get(ctx, entry.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != taskapi.StateRunning || !got.Running || got.Revision != entry.Revision {
		t.Fatalf("active command entry = %#v, want unchanged running revision %d", got, entry.Revision)
	}
}

func TestRuntimeRecoveryDoesNotRewriteClaimedCommandOperation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ref := session.SessionRef{SessionID: "command-recovery-claimed-operation"}
	store := newSagaTaskStore()
	entry, err := store.Put(ctx, taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: "task-claimed-operation", Kind: taskapi.KindCommand, Session: ref,
		State: taskapi.StateRunning, Running: true,
		Result: map[string]any{"state": string(taskapi.StateRunning)},
		Metadata: map[string]any{
			"state":                      string(taskapi.StateRunning),
			"running":                    true,
			"command_phase":              commandPhaseEffectClaimed,
			commandStreamEventCursorMeta: int64(math.MaxInt64),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{}
	runtime.tasks = newTaskRuntime(runtime, store)
	runtime.tasks.tasks[entry.TaskID] = &commandTask{
		ref: taskapi.Ref{TaskID: entry.TaskID}, sessionRef: ref,
		state: taskapi.StateRunning, running: true,
	}
	release, claimed := runtime.tasks.tryClaimSubagentOperation(ref, entry.TaskID)
	if !claimed {
		t.Fatal("claim command operation = false, want true")
	}
	defer release()

	if err := runtime.recoverRuntimeState(ctx, ref); err != nil {
		t.Fatalf("recoverRuntimeState() error = %v", err)
	}
	got, err := store.Get(ctx, entry.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != taskapi.StateRunning || !got.Running || got.Revision != entry.Revision {
		t.Fatalf("claimed command entry = %#v, want unchanged running revision %d", got, entry.Revision)
	}
}

func TestRuntimeRecoveryCommandRepairFileStoreRoundTrip(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	ref := session.SessionRef{SessionID: "command-recovery-file-roundtrip"}
	store := sessionfile.NewTaskStore(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	entry, err := store.Put(ctx, taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: "task-file-roundtrip", Kind: taskapi.KindCommand, Session: ref,
		State: taskapi.StateRunning, Running: true,
		Result: map[string]any{"state": string(taskapi.StateRunning)},
		Metadata: map[string]any{
			"state":                      string(taskapi.StateRunning),
			"running":                    true,
			"command_phase":              commandPhaseEffectClaimed,
			commandStreamEventCursorMeta: int64(math.MaxInt64),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime := &Runtime{}
	runtime.tasks = newTaskRuntime(runtime, store)
	if err := runtime.recoverRuntimeState(ctx, ref); err != nil {
		t.Fatalf("recoverRuntimeState() error = %v", err)
	}

	reopened := sessionfile.NewTaskStore(sessionfile.NewStore(sessionfile.Config{RootDir: root}))
	got, err := reopened.Get(ctx, entry.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	assertRecoveredCommandEntry(
		t, got, taskapi.StateUnknownOutcome, commandPhaseUnknown,
		"command stream event cursor is exhausted", false,
	)

	reopenedRuntime := &Runtime{}
	reopenedRuntime.tasks = newTaskRuntime(reopenedRuntime, reopened)
	streamed, err := newStreamService(reopenedRuntime.tasks).Read(ctx, taskstream.ReadRequest{
		Ref: taskstream.Ref{SessionID: ref.SessionID, TaskID: entry.TaskID},
	})
	if err != nil {
		t.Fatalf("stream Read() after repair round trip error = %v", err)
	}
	if streamed.State != string(taskapi.StateUnknownOutcome) || streamed.Running ||
		streamed.Cursor.Events != math.MaxInt64 || !streamed.TerminalFramed {
		t.Fatalf("stream snapshot after repair round trip = %#v, want terminal unknown outcome at exhausted frontier", streamed)
	}
	frames := taskstream.FramesForSnapshot(streamed)
	if len(frames) != 1 || !frames[0].Closed || frames[0].State != string(taskapi.StateUnknownOutcome) ||
		frames[0].Cursor.Events != math.MaxInt64 {
		t.Fatalf("stream frames after repair round trip = %#v, want one closed unknown-outcome frame", frames)
	}
	acknowledged, err := newStreamService(reopenedRuntime.tasks).Read(ctx, taskstream.ReadRequest{
		Ref:    taskstream.Ref{SessionID: ref.SessionID, TaskID: entry.TaskID},
		Cursor: streamed.Cursor,
	})
	if err != nil {
		t.Fatalf("stream Read() at repaired terminal frontier error = %v", err)
	}
	if acknowledged.State != string(taskapi.StateUnknownOutcome) || !acknowledged.TerminalFramed ||
		len(taskstream.FramesForSnapshot(acknowledged)) != 0 {
		t.Fatalf("acknowledged repaired stream snapshot = %#v, want idempotent terminal snapshot without repeated frames", acknowledged)
	}
}

func TestRuntimeRecoveryReturnsCommandRepairPersistenceFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ref := session.SessionRef{SessionID: "command-recovery-persist-failure"}
	store := newSagaTaskStore()
	entry, err := store.Put(ctx, taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: "task-persist-failure", Kind: taskapi.KindCommand, Session: ref,
		State: taskapi.StateRunning, Running: true,
		Metadata: map[string]any{
			"command_phase":              commandPhaseRunning,
			commandStreamEventCursorMeta: int64(math.MaxInt64),
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	store.failOnPut = 2
	runtime := &Runtime{}
	runtime.tasks = newTaskRuntime(runtime, store)

	err = runtime.recoverRuntimeState(ctx, ref)
	if err == nil || !strings.Contains(err.Error(), "forced task persistence failure") {
		t.Fatalf("recoverRuntimeState() error = %v, want repair persistence failure", err)
	}
	got, getErr := store.Get(ctx, entry.TaskID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if got.State != taskapi.StateRunning || !got.Running || got.Revision != entry.Revision {
		t.Fatalf("entry after failed repair = %#v, want unchanged running revision %d", got, entry.Revision)
	}
}

func assertRecoveredCommandEntry(
	t *testing.T,
	got *taskapi.Entry,
	wantState taskapi.State,
	wantPhase string,
	wantError string,
	wantResult bool,
) {
	t.Helper()
	if got == nil {
		t.Fatal("recovered command entry = nil")
	}
	metadataRunning, _ := got.Metadata["running"].(bool)
	gotResult := strings.TrimSpace(taskStringValue(got.Result["result"]))
	if got.State != wantState || got.Running ||
		taskStringValue(got.Result["state"]) != string(wantState) ||
		taskStringValue(got.Metadata["state"]) != string(wantState) || metadataRunning ||
		taskStringValue(got.Metadata["command_phase"]) != wantPhase ||
		!strings.Contains(taskStringValue(got.Result["error"]), wantError) ||
		(gotResult != "") != wantResult {
		t.Fatalf("recovered command entry = %#v, want terminal %q phase %q error containing %q with result=%v", got, wantState, wantPhase, wantError, wantResult)
	}
}
