package runtime

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/compact"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

type listFailingTaskStore struct {
	taskapi.Store
	err error
}

func (s *listFailingTaskStore) ListSession(ctx context.Context, ref session.SessionRef) ([]*taskapi.Entry, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.Store.ListSession(ctx, ref)
}

type recoveryProbeCompactor struct {
	prepareCalls int
	forceCalls   int
}

func (c *recoveryProbeCompactor) Prepare(context.Context, compact.Request) (compact.Result, error) {
	c.prepareCalls++
	return compact.Result{}, nil
}

func (*recoveryProbeCompactor) CompactOnOverflow(context.Context, compact.Request, error) (compact.Result, error) {
	return compact.Result{}, nil
}

func (c *recoveryProbeCompactor) Force(context.Context, compact.Request, string) (compact.Result, error) {
	c.forceCalls++
	return compact.Result{}, nil
}

func TestRuntimeRecoveryReturnsDurableListFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ref := session.SessionRef{SessionID: "command-recovery-list-failure"}
	base := newSagaTaskStore()
	entry, err := base.Put(ctx, taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: "task-list-failure", Kind: taskapi.KindCommand, Session: ref,
		State: taskapi.StateRunning, Running: true,
		Metadata: map[string]any{
			"command_phase": commandPhaseEffectClaimed,
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("forced durable Task list failure")
	store := &listFailingTaskStore{Store: base, err: cause}
	runtime := &Runtime{}
	runtime.tasks = newTaskRuntime(runtime, store)

	err = runtime.recoverRuntimeState(ctx, ref)
	if !errors.Is(err, cause) {
		t.Fatalf("recoverRuntimeState() error = %v, want durable list failure", err)
	}
	unchanged, getErr := base.Get(ctx, entry.TaskID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if unchanged.Revision != entry.Revision || unchanged.State != taskapi.StateRunning || !unchanged.Running {
		t.Fatalf("entry after list failure = %#v, want unchanged running revision %d", unchanged, entry.Revision)
	}

	store.err = nil
	if err := runtime.recoverRuntimeState(ctx, ref); err != nil {
		t.Fatalf("recoverRuntimeState() after store recovery error = %v", err)
	}
	recovered, getErr := base.Get(ctx, entry.TaskID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	assertRecoveredCommandEntry(
		t, recovered, taskapi.StateUnknownOutcome, commandPhaseUnknown,
		"command effect outcome is unavailable after process restart", false,
	)
	stableRevision := recovered.Revision
	if err := runtime.recoverRuntimeState(ctx, ref); err != nil {
		t.Fatalf("second recoverRuntimeState() after store recovery error = %v", err)
	}
	stable, getErr := base.Get(ctx, entry.TaskID)
	if getErr != nil {
		t.Fatal(getErr)
	}
	if stable.Revision != stableRevision {
		t.Fatalf("second recovery revision = %d, want stable %d", stable.Revision, stableRevision)
	}
}

func TestRuntimeRecoveryEmptyDurableListDoesNotUseLiveIndex(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ref := session.SessionRef{SessionID: "command-recovery-empty-durable"}
	store := newSagaTaskStore()
	runtime := &Runtime{}
	runtime.tasks = newTaskRuntime(runtime, store)
	live := &commandTask{
		ref: taskapi.Ref{TaskID: "live-only-task"}, sessionRef: ref,
		state: taskapi.StateRunning, running: true,
		metadata: map[string]any{"command_phase": commandPhaseEffectClaimed},
	}
	runtime.tasks.tasks[live.ref.TaskID] = live
	runtime.tasks.order[ref.SessionID] = []string{live.ref.TaskID}

	if err := runtime.recoverRuntimeState(ctx, ref); err != nil {
		t.Fatalf("recoverRuntimeState() error = %v", err)
	}
	live.mu.Lock()
	gotState, gotRunning := live.state, live.running
	live.mu.Unlock()
	if gotState != taskapi.StateRunning || !gotRunning {
		t.Fatalf("live-only task = state %q running %v, want unchanged live index", gotState, gotRunning)
	}
	listed, err := store.ListSession(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 0 {
		t.Fatalf("durable entries = %#v, want empty", listed)
	}
}

func TestRuntimeRecoveryListFailureBlocksRunAndCompact(t *testing.T) {
	t.Parallel()

	cause := errors.New("forced durable Task list failure")
	store := &listFailingTaskStore{Store: newSagaTaskStore(), err: cause}
	sessions, active := newTestSessionService(t, "recovery-list-gates")
	factory := &attemptFactory{}
	compactor := &recoveryProbeCompactor{}
	runtime, err := New(Config{
		Sessions: sessions, AgentFactory: factory, TaskStore: store, Compactor: compactor,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef,
		Input:      "must not reach model",
		AgentSpec: agent.AgentSpec{
			Name: "chat", Model: staticModel{text: "unexpected"},
		},
	})
	if err != nil {
		t.Fatalf("Run() start error = %v", err)
	}
	_, runErr := drainRunnerEvents(t, run.Handle)
	if !errors.Is(runErr, cause) {
		t.Fatalf("Run() terminal error = %v, want durable list failure", runErr)
	}
	if factory.Calls() != 0 || compactor.prepareCalls != 0 {
		t.Fatalf("Run() downstream calls = factory %d prepare %d, want zero", factory.Calls(), compactor.prepareCalls)
	}

	_, err = runtime.Compact(context.Background(), CompactRequest{
		SessionRef: active.SessionRef,
		Model:      staticModel{text: "unexpected"},
		Trigger:    "manual",
	})
	if !errors.Is(err, cause) {
		t.Fatalf("Compact() error = %v, want durable list failure", err)
	}
	if compactor.forceCalls != 0 {
		t.Fatalf("Compact() Force calls = %d, want zero", compactor.forceCalls)
	}
}

func TestRuntimeRecoveryConvergesInterruptedCommand(t *testing.T) {
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
					"state":         string(taskapi.StateRunning),
					"running":       true,
					"command_phase": tt.phase,
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
			wantError := "task interrupted during resume"
			if tt.wantState == taskapi.StateUnknownOutcome {
				wantError = "command effect outcome is unavailable after process restart"
			}
			assertRecoveredCommandEntry(t, got, tt.wantState, tt.wantPhase, wantError, tt.wantResult)

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
		"command effect outcome is unavailable after process restart", false,
	)
}

func TestRuntimeRecoveryReturnsCommandRepairPersistenceFailure(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	ref := session.SessionRef{SessionID: "command-recovery-persist-failure"}
	store := newSagaTaskStore()
	entry, err := store.Put(ctx, taskapi.PutRequest{Entry: &taskapi.Entry{
		TaskID: "task-persist-failure", Kind: taskapi.KindCommand, Session: ref,
		State: taskapi.StateRunning, Running: true,
		Metadata: map[string]any{"command_phase": commandPhaseRunning},
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
		return
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

func TestTaskInt64ValueSaturatesJSONFloatExtremes(t *testing.T) {
	t.Parallel()

	got, ok := taskInt64Value(float64(math.MaxInt64))
	if !ok || got != math.MaxInt64 {
		t.Fatalf("taskInt64Value(float64(MaxInt64)) = (%d, %v), want (%d, true)", got, ok, math.MaxInt64)
	}
	got, ok = taskInt64Value(float64(math.MinInt64))
	if !ok || got != math.MinInt64 {
		t.Fatalf("taskInt64Value(float64(MinInt64)) = (%d, %v), want (%d, true)", got, ok, math.MinInt64)
	}
}
