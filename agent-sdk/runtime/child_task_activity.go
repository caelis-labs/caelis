package runtime

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
)

// childTaskActivity binds a possible follow-up to its first producer evidence.
// Admission alone changes no Task state. The latch retains only identity;
// output payloads go directly to the application observer.
type childTaskActivity struct {
	runtime     *taskRuntime
	ctx         context.Context
	ref         session.SessionRef
	taskID      string
	activityID  string
	turnSeq     int64
	observer    output.Observer
	started     sync.Once
	observed    atomic.Bool
	ready       chan struct{}
	readyOnce   sync.Once
	persistDone chan struct{}
	settled     atomic.Bool
}

func (a *childTaskActivity) ObserveTaskOutput(ctx context.Context, event output.Event) error {
	// Recipient-visible input is admission evidence, not child execution.
	if !session.IsAgentCommunicationProtocol(event.Event) &&
		(event.Event != nil || event.Text != "" || event.Running || event.Closed || event.State != "") {
		a.started.Do(func() {
			a.observed.Store(true)
			// Activity persistence owns its existing operation claim and CAS path,
			// but disk latency must not run under the producer's endpoint lock.
			go a.persistWhenAvailable()
		})
	}
	if a.observer != nil {
		return a.observer.ObserveTaskOutput(ctx, event)
	}
	return nil
}

func (a *childTaskActivity) persistWhenAvailable() {
	defer close(a.persistDone)
	defer a.signalReady()
	for {
		release, err := a.runtime.waitForTaskOperationClaim(a.ctx, a.ref, a.taskID)
		if err != nil {
			return
		}
		err = a.persist()
		release()
		if err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (a *childTaskActivity) signalReady() { a.readyOnce.Do(func() { close(a.ready) }) }

// awaitObservedStart lets readers cross the first-output registration barrier
// without making the producer wait for Task persistence. Admission alone does
// not wait or change the previous activity's state.
func (a *childTaskActivity) awaitObservedStart(ctx context.Context) error {
	if a == nil || !a.observed.Load() {
		return nil
	}
	select {
	case <-a.ready:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// awaitPersistence joins the first-output writer after terminal commit. It
// runs outside the Task operation claim and never delays output callbacks.
func (a *childTaskActivity) awaitPersistence(ctx context.Context) error {
	if a == nil || !a.observed.Load() {
		return nil
	}
	select {
	case <-a.persistDone:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// openLocked runs under the Task operation claim and task.mu. Only this exact
// next activity may advance the generation, including a cancellation journal
// that selected it before its first output arrived.
func (a *childTaskActivity) openLocked(task *subagentTask) bool {
	if task.turnSeq == a.turnSeq {
		return task.activityID == a.activityID
	}
	if task.turnSeq+1 != a.turnSeq {
		return false
	}
	cancelTurn, cancelScoped := subagentCancelTurnSeq(task.metadata)
	if task.running && (!cancelScoped || cancelTurn != a.turnSeq) {
		return false
	}
	task.turnSeq = a.turnSeq
	task.activityID = a.activityID
	task.activityGeneration = a.turnSeq
	task.stdout, task.stderr = "", ""
	task.stdoutCursor, task.stderrCursor = 0, 0
	task.contextUsage = nil
	task.applyResult(delegation.Result{TaskID: a.taskID, State: delegation.StateRunning, Running: true})
	task.metadata[subagentActivityIDMeta] = a.activityID
	task.metadata[subagentActivityGenerationMeta] = a.turnSeq
	if !cancelScoped || cancelTurn != a.turnSeq {
		delete(task.metadata, subagentCancelPhaseKey)
		delete(task.metadata, subagentCancelTurnSeqKey)
	}
	delete(task.metadata, "final_event_persisted")
	return true
}

// persist holds the operation claim, but releases all Task memory locks before
// store I/O. CAS retries rebase lifecycle state without replaying child input.
func (a *childTaskActivity) persist() error {
	task, err := a.runtime.lookupSubagent(a.ctx, a.ref, a.taskID)
	if err != nil {
		return err
	}
	task.activityApplyMu.Lock()
	task.mu.Lock()
	if !a.openLocked(task) || !task.running {
		task.mu.Unlock()
		task.activityApplyMu.Unlock()
		return nil
	}
	entry := task.entrySnapshot(a.runtime.runtime.now())
	task.mu.Unlock()
	task.activityApplyMu.Unlock()
	// Readers need the observed generation, not a disk acknowledgement. The
	// existing operation claim still fences the asynchronous durable write.
	a.signalReady()
	for range 4 {
		err = a.runtime.persistTaskEntryWithConflictInvalidation(a.ctx, entry, false)
		var conflict *taskapi.RevisionConflictError
		if !errors.As(err, &conflict) || a.runtime.store == nil {
			return err
		}
		current, loadErr := a.runtime.store.Get(a.ctx, a.taskID)
		if loadErr != nil || current == nil {
			return errors.Join(err, loadErr)
		}
		durable := a.runtime.rehydrateSubagentTask(current)
		if taskTurnSeqFromSpec(current.Spec) != a.turnSeq-1 || !a.openLocked(durable) {
			// An activity already committed at this generation (including a
			// terminal one) is authoritative. Never rebase local running state
			// over it, or over another activity that won the same generation.
			a.runtime.mu.Lock()
			a.runtime.subagents[a.taskID] = durable
			a.runtime.mu.Unlock()
			return nil
		}
		// Reopen from the latest predecessor so a cancellation journal written
		// by another CAS owner is preserved when it targets this activity.
		task = durable
		entry = a.runtime.rebaseObservedSubagentTask(task, current)
		a.runtime.mu.Lock()
		a.runtime.subagents[a.taskID] = task
		a.runtime.mu.Unlock()
	}
	return err
}

// ReplaceTaskHistory forwards recovery observations without opening an activity.
func (a *childTaskActivity) ReplaceTaskHistory(ctx context.Context, events []*session.Event) error {
	if observer, ok := a.observer.(output.HistoryObserver); ok {
		return observer.ReplaceTaskHistory(ctx, events)
	}
	return nil
}
