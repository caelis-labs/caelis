package runtime

import (
	"context"
	"errors"
	"sync"
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
	runtime    *taskRuntime
	ctx        context.Context
	ref        session.SessionRef
	taskID     string
	activityID string
	turnSeq    int64
	observer   output.Observer
	started    sync.Once
}

func (a *childTaskActivity) ObserveTaskOutput(ctx context.Context, event output.Event) error {
	// Recipient-visible input is admission evidence, not child execution.
	if !session.IsAgentCommunicationProtocol(event.Event) &&
		(event.Event != nil || event.Text != "" || event.Running || event.Closed || event.State != "") {
		a.started.Do(func() {
			// A producer may hold its endpoint lock while calling us. Waiting for
			// a Task control owner here would deadlock concurrent cancellation.
			if release, claimed := a.runtime.tryClaimSubagentOperation(a.ref, a.taskID); claimed {
				err := a.persist()
				release()
				if err == nil {
					return
				}
			}
			go a.persistWhenAvailable()
		})
	}
	if a.observer != nil {
		return a.observer.ObserveTaskOutput(ctx, event)
	}
	return nil
}

func (a *childTaskActivity) persistWhenAvailable() {
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
