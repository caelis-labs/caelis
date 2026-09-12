package runtime

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/agent-sdk/task/subagent"
)

func TestChildUsagePersistsLiveInitialAndFollowup(t *testing.T) {
	for _, initial := range []bool{true, false} {
		t.Run(map[bool]string{true: "initial", false: "followup"}[initial], func(t *testing.T) {
			r, task, activity, completion := newUsageTestActivity(t, initial)
			for _, used := range []uint64{42000, 8000, 0} {
				if err := activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, used, "gpt-test")); err != nil {
					t.Fatal(err)
				}
				awaitChildUsagePersistence(t, activity)
				entry := assertChildContextUsage(t, r, task, 200000, used, "gpt-test")
				if !entry.Running || entry.State != taskapi.StateRunning || taskTurnSeqFromSpec(entry.Spec) != activity.turnSeq {
					t.Fatalf("live usage changed lifecycle: %#v", entry)
				}
			}
			finishChildActivity(t, completion, "done")
			entry := assertChildContextUsage(t, r, task, 200000, 0, "gpt-test")
			if entry.Running || entry.State != taskapi.StateCompleted {
				t.Fatalf("terminal usage reopened Task: %#v", entry)
			}
		})
	}
}

func TestChildUsageReplacesGaugeDuringBlockedWrite(t *testing.T) {
	r, task, activity, completion := newUsageTestActivity(t, true)
	base := r.tasks.store
	started, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	r.tasks.store = &usageTestStore{Store: base, beforePut: func(ctx context.Context, _ taskapi.PutRequest) error {
		if first.CompareAndSwap(false, true) {
			close(started)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}}
	_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 42000, "gpt-test"))
	waitUsageSignal(t, started)
	returned := make(chan struct{})
	go func() {
		_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 8000, "gpt-test"))
		close(returned)
	}()
	waitUsageSignal(t, returned)
	releaseOnce.Do(func() { close(release) })
	awaitChildUsagePersistence(t, activity)
	assertChildContextUsage(t, r, task, 200000, 8000, "gpt-test")
	// A producer arriving after worker quiescence must start a new writer.
	_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 0, "gpt-test"))
	awaitChildUsagePersistence(t, activity)
	assertChildContextUsage(t, r, task, 200000, 0, "gpt-test")
	finishChildActivity(t, completion, "done")
}

func TestChildUsageRetainsGaugeAcrossWriteFailure(t *testing.T) {
	for _, conflict := range []bool{false, true} {
		t.Run(map[bool]string{false: "io_failure", true: "cas_conflict"}[conflict], func(t *testing.T) {
			r, task, activity, completion := newUsageTestActivity(t, true)
			base := r.tasks.store
			var first atomic.Bool
			r.tasks.store = &usageTestStore{Store: base, beforePut: func(ctx context.Context, req taskapi.PutRequest) error {
				if !first.CompareAndSwap(false, true) {
					return nil
				}
				if !conflict {
					return errors.New("injected usage persistence failure")
				}
				return bumpUsageTestTask(base, req.Entry.TaskID, func(entry *taskapi.Entry) {
					entry.Metadata["concurrent_writer"] = "preserved"
					entry.Spec["concurrent_spec"] = "preserved"
				})
			}}
			_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 8000, "gpt-test"))
			awaitChildUsagePersistence(t, activity)
			entry := assertChildContextUsage(t, r, task, 200000, 8000, "gpt-test")
			if conflict && (entry.Metadata["concurrent_writer"] != "preserved" || entry.Spec["concurrent_spec"] != "preserved") {
				t.Fatalf("usage retry lost canonical fields: %#v", entry)
			}
			finishChildActivity(t, completion, "done")
		})
	}
}

func TestChildUsageTerminalJoinsBlockedWriter(t *testing.T) {
	r, task, activity, completion := newUsageTestActivity(t, true)
	base := r.tasks.store
	started, release := make(chan struct{}), make(chan struct{})
	var first atomic.Bool
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	r.tasks.store = &usageTestStore{Store: base, beforePut: func(context.Context, taskapi.PutRequest) error {
		if first.CompareAndSwap(false, true) {
			close(started)
			// Model a store operation that does not stop on cancellation.
			<-release
		}
		return nil
	}}
	_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 42000, "gpt-test"))
	waitUsageSignal(t, started)
	_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 8000, "gpt-test"))
	done := make(chan struct{})
	go func() {
		completion.PublishSubagentCompletion(delegation.Result{State: delegation.StateCompleted, Result: "done"})
		close(done)
	}()
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		<-done
	})
	waitUsageSignal(t, activity.usageCtx.Done())
	select {
	case <-done:
		t.Fatal("terminal acknowledged while usage held the Task claim")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	waitUsageSignal(t, done)
	activity.usageMu.Lock()
	worker := activity.usageDone
	activity.usageMu.Unlock()
	if worker != nil {
		t.Fatal("terminal publication returned before joining its usage writer")
	}
	entry := assertChildContextUsage(t, r, task, 200000, 8000, "gpt-test")
	if entry.Running || entry.State != taskapi.StateCompleted {
		t.Fatalf("terminal failed to converge after writer release: %#v", entry)
	}
	_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 99000, "late"))
	after, err := r.tasks.store.Get(t.Context(), task.ref.TaskID)
	if err != nil || !reflect.DeepEqual(entry, after) {
		t.Fatalf("usage wrote after terminal publication returned: %#v, %v", after, err)
	}
}

func TestChildCompletionRetainsUsageAcrossCASConflict(t *testing.T) {
	for _, initial := range []bool{true, false} {
		for _, terminal := range []bool{false, true} {
			name := map[bool]string{true: "initial", false: "followup"}[initial] + "/" + map[bool]string{true: "terminal", false: "running"}[terminal]
			t.Run(name, func(t *testing.T) {
				r, task, activity, completion := newUsageTestActivity(t, initial)
				base := r.tasks.store
				var first atomic.Bool
				r.tasks.store = &usageTestStore{Store: base, beforePut: func(ctx context.Context, req taskapi.PutRequest) error {
					if !first.CompareAndSwap(false, true) {
						return nil
					}
					return bumpUsageTestTask(base, req.Entry.TaskID, func(entry *taskapi.Entry) {
						entry.Metadata["concurrent_writer"] = "preserved"
						if terminal {
							entry.State, entry.Running = taskapi.StateCompleted, false
						}
					})
				}}
				release, claimed := r.tasks.tryClaimSubagentOperation(task.sessionRef, task.ref.TaskID)
				if !claimed {
					t.Fatal("Task operation unavailable")
				}
				_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 42000, "gpt-test"))
				_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 8000, "gpt-test"))
				// Enqueue synchronously so terminal owns the release edge before
				// the live worker can acquire the held operation claim.
				done := completion.enqueue(delegation.Result{State: delegation.StateCompleted, Result: "done"})
				release()
				waitUsageSignal(t, done)
				awaitChildUsagePersistence(t, activity)
				entry := assertChildContextUsage(t, r, task, 200000, 8000, "gpt-test")
				if entry.Running || entry.State != taskapi.StateCompleted || entry.Metadata["concurrent_writer"] != "preserved" {
					t.Fatalf("terminal CAS did not converge: %#v", entry)
				}
				_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 99000, "late"))
				awaitChildUsagePersistence(t, activity)
				after := assertChildContextUsage(t, r, task, 200000, 8000, "gpt-test")
				if after.Revision != entry.Revision {
					t.Fatal("usage writer outlived terminal acknowledgement")
				}
			})
		}
	}
}

func TestChildUsageCannotOverwriteReplacementActivity(t *testing.T) {
	for _, nextGeneration := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_generation", true: "next_generation"}[nextGeneration], func(t *testing.T) {
			r, task, activity, completion := newUsageTestActivity(t, true)
			base := r.tasks.store
			var first atomic.Bool
			var replacement *taskapi.Entry
			r.tasks.store = &usageTestStore{Store: base, beforePut: func(ctx context.Context, req taskapi.PutRequest) error {
				if !first.CompareAndSwap(false, true) {
					return nil
				}
				err := bumpUsageTestTask(base, req.Entry.TaskID, func(entry *taskapi.Entry) {
					entry.Metadata[subagentActivityIDMeta] = "replacement"
					if nextGeneration {
						entry.Spec["turn_seq"] = activity.turnSeq + 1
						entry.Metadata[subagentActivityGenerationMeta] = activity.turnSeq + 1
					}
					entry.ContextUsage = contextUsageRecordFromOutput(childUsageOutput(200000, 123, "replacement"))
				})
				if err == nil {
					replacement, err = base.Get(ctx, req.Entry.TaskID)
				}
				return err
			}}
			_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 8000, "gpt-test"))
			awaitChildUsagePersistence(t, activity)
			finishChildActivity(t, completion, "stale final")
			entry := assertChildContextUsage(t, r, task, 200000, 123, "replacement")
			if !reflect.DeepEqual(entry, replacement) {
				t.Fatalf("stale producer changed replacement: got %#v, want %#v", entry, replacement)
			}
		})
	}
}

func TestChildUsageDiscardJoinsUninstalledWriter(t *testing.T) {
	r, task, _, _ := newUsageTestActivity(t, true)
	before, err := r.tasks.store.Get(t.Context(), task.ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	activity := newChildTaskActivity(r.tasks, t.Context(), task.sessionRef, task.ref.TaskID, task.activityID, task.turnSeq, output.Nop(), false, true)
	_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 8000, "gpt-test"))
	activity.discard()
	awaitChildUsagePersistence(t, activity)
	_ = activity.ObserveTaskOutput(t.Context(), childUsageOutput(200000, 99000, "late"))
	awaitChildUsagePersistence(t, activity)
	after, err := r.tasks.store.Get(t.Context(), task.ref.TaskID)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("uninstalled writer changed Task: %#v, %v", after, err)
	}
}

func TestChildUsageWaitsForSpawnInstallation(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "installed", true: "failed"}[fail], func(t *testing.T) {
			release := make(chan struct{})
			var releaseOnce sync.Once
			base := &recordingSubagentRunner{
				spawnResult:    delegation.Result{State: delegation.StateRunning, Running: true},
				publishOnSpawn: true, spawnStreamEvent: childUsageOutput(200000, 42000, "gpt-test").Event,
			}
			runner := &usageSpawnTestRunner{Runner: base, entered: make(chan struct{}), release: release, fail: fail}
			r, active := newSubagentTaskTestRuntime(t, runner)
			result := make(chan error, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				_, err := r.tasks.StartSubagent(t.Context(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
					Agent: "helper", Prompt: "inspect", Role: session.ParticipantRoleDelegated,
				})
				result <- err
			}()
			t.Cleanup(func() {
				releaseOnce.Do(func() { close(release) })
				<-finished
				if activity, ok := base.spawnContext.Output.(*childTaskActivity); ok {
					activity.discard()
					if err := activity.awaitPersistence(context.Background()); err != nil {
						t.Error(err)
					}
				}
			})
			waitUsageSignal(t, runner.entered)
			activity := base.spawnContext.Output.(*childTaskActivity)
			before, err := r.tasks.store.Get(t.Context(), base.spawnContext.TaskID)
			if err != nil || spawnPhaseOf(before) != spawnPhaseExternalPending || before.ContextUsage != nil {
				t.Fatalf("usage crossed Spawn installation gate: %#v, %v", before, err)
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-result:
				if (err != nil) != fail {
					t.Fatalf("Spawn returned %v; failure expected: %v", err, fail)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("Spawn did not finish")
			}
			awaitChildUsagePersistence(t, activity)
			entry, err := r.tasks.store.Get(t.Context(), before.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if fail {
				if entry.ContextUsage != nil || spawnPhaseOf(entry) != spawnPhaseUnknownOutcome {
					t.Fatalf("failed Spawn retained usage writer effects: %#v", entry)
				}
			} else {
				if entry.ContextUsage == nil || entry.ContextUsage.Snapshot.Used != 42000 {
					t.Fatalf("installed Spawn lost synchronous usage: %#v", entry)
				}
				finishChildActivity(t, base.spawnContext.Completion, "done")
			}
		})
	}
}

type usageSpawnTestRunner struct {
	subagent.Runner
	entered chan struct{}
	release <-chan struct{}
	fail    bool
}

func (r *usageSpawnTestRunner) Spawn(ctx context.Context, spawn subagent.SpawnContext, req delegation.Request) (delegation.Anchor, delegation.Result, error) {
	anchor, result, err := r.Runner.Spawn(ctx, spawn, req)
	close(r.entered)
	select {
	case <-r.release:
	case <-ctx.Done():
		return anchor, result, ctx.Err()
	}
	if r.fail {
		return anchor, result, errors.New("injected unknown Spawn outcome")
	}
	return anchor, result, err
}

type usageTestStore struct {
	taskapi.Store
	beforePut func(context.Context, taskapi.PutRequest) error
}

func (s *usageTestStore) Put(ctx context.Context, req taskapi.PutRequest) (*taskapi.Entry, error) {
	if req.Entry.ContextUsage != nil {
		if err := s.beforePut(ctx, req); err != nil {
			return nil, err
		}
	}
	return s.Store.(taskapi.CASStore).Put(ctx, req)
}

func bumpUsageTestTask(store taskapi.Store, taskID string, mutate func(*taskapi.Entry)) error {
	ctx := context.Background()
	entry, err := store.Get(ctx, taskID)
	if err != nil {
		return err
	}
	mutate(entry)
	_, err = store.(taskapi.CASStore).Put(ctx, taskapi.PutRequest{Entry: entry, ExpectedRevision: entry.Revision})
	return err
}

func newUsageTestActivity(t *testing.T, initial bool) (*Runtime, *subagentTask, *childTaskActivity, subagentCompletionSink) {
	t.Helper()
	var r *Runtime
	var task *subagentTask
	var activity *childTaskActivity
	var completion subagentCompletionSink
	if initial {
		runner := &recordingSubagentRunner{spawnResult: delegation.Result{State: delegation.StateRunning, Running: true}}
		var active session.Session
		r, active = newSubagentTaskTestRuntime(t, runner)
		snapshot, err := r.tasks.StartSubagent(t.Context(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{
			Agent: "helper", Prompt: "inspect", Role: session.ParticipantRoleDelegated,
		})
		if err != nil {
			t.Fatal(err)
		}
		task, err = r.tasks.lookupSubagent(t.Context(), active.SessionRef, snapshot.Ref.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		activity = runner.spawnContext.Output.(*childTaskActivity)
		completion = runner.spawnContext.Completion.(subagentCompletionSink)
	} else {
		r, task, _ = newIdleChildActivityTask(t)
		_, observer, sink, err := r.prepareChildTaskOutput(t.Context(), task)
		if err != nil {
			t.Fatal(err)
		}
		activity = observer.(*childTaskActivity)
		completion = sink.(subagentCompletionSink)
		_ = observer.ObserveTaskOutput(t.Context(), output.Event{State: "running", Running: true})
		awaitChildUsagePersistence(t, activity)
	}
	t.Cleanup(func() {
		activity.discard()
		if err := activity.awaitPersistence(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return r, task, activity, completion
}

func awaitChildUsagePersistence(t *testing.T, activity *childTaskActivity) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := activity.awaitPersistence(ctx); err != nil {
		t.Fatal(err)
	}
}

func waitUsageSignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("usage persistence barrier did not finish")
	}
}
