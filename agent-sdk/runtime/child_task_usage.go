package runtime

import (
	"context"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
)

func (a *childTaskActivity) markInstalled() {
	if a == nil {
		return
	}
	a.installedOnce.Do(func() { close(a.installed) })
}

func (a *childTaskActivity) discard() {
	if a == nil {
		return
	}
	a.sealUsage()
	a.settled.Store(true)
	a.usageMu.Lock()
	a.latestUsage = nil
	a.usageMu.Unlock()
	a.markInstalled()
}

// sealUsage closes producer admission before terminal persistence. The final
// snapshot remains available across CAS retries; only the live writer stops.
func (a *childTaskActivity) sealUsage() {
	if a == nil {
		return
	}
	a.usageMu.Lock()
	a.usageClosed = true
	a.stopUsage()
	a.usageMu.Unlock()
}

func (a *childTaskActivity) peekUsage() *taskapi.ContextUsageRecord {
	if a == nil {
		return nil
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	return taskapi.CloneContextUsageRecord(a.latestUsage)
}

func (a *childTaskActivity) noteUsage(record *taskapi.ContextUsageRecord) {
	if a == nil || record == nil {
		return
	}
	a.usageMu.Lock()
	defer a.usageMu.Unlock()
	if a.usageClosed {
		return
	}
	a.latestUsage = record
	if a.usageDone == nil {
		a.usageDone = make(chan struct{})
		go a.persistUsageLoop()
	}
}

// finishUsageLocked releases worker ownership atomically with the idle check.
// A later producer either belongs to this worker or starts its successor.
func (a *childTaskActivity) finishUsageLocked() {
	close(a.usageDone)
	a.usageDone = nil
}

func (a *childTaskActivity) persistUsageLoop() {
	select {
	case <-a.installed:
	case <-a.usageCtx.Done():
	}
	if err := a.awaitOpenPersistence(a.usageCtx); err != nil {
		a.usageMu.Lock()
		a.finishUsageLocked()
		a.usageMu.Unlock()
		return
	}
	for {
		a.usageMu.Lock()
		if a.usageClosed || a.latestUsage == a.persistedUsage {
			a.finishUsageLocked()
			a.usageMu.Unlock()
			return
		}
		a.usageMu.Unlock()
		release, err := a.runtime.waitForTaskOperationClaim(a.usageCtx, a.ref, a.taskID)
		if err == nil {
			// Read after claim admission, never restore a pre-claim snapshot.
			a.usageMu.Lock()
			record, closed := a.latestUsage, a.usageClosed
			a.usageMu.Unlock()
			if !closed {
				var current bool
				current, err = a.writeUsage(a.usageCtx, record)
				if err == nil {
					if !current {
						a.discard()
					} else {
						a.usageMu.Lock()
						a.persistedUsage = record
						a.usageMu.Unlock()
					}
				}
			}
			release()
		}
		if err != nil {
			// Keep the pending gauge across failures. Terminal admission cancels
			// this wait and folds that same gauge into its own transaction.
			timer := time.NewTimer(50 * time.Millisecond)
			select {
			case <-timer.C:
			case <-a.usageCtx.Done():
			}
			timer.Stop()
		}
	}
}

// writeUsage runs under the Task operation claim. The canonical entry owns all
// lifecycle fields; a gauge update may replace only usage and its timestamp.
func (a *childTaskActivity) writeUsage(ctx context.Context, record *taskapi.ContextUsageRecord) (bool, error) {
	entry, err := a.runtime.store.Get(ctx, a.taskID)
	if err != nil {
		return false, err
	}
	if entry == nil || entry.Kind != taskapi.KindSubagent || session.NormalizeSessionRef(entry.Session) != session.NormalizeSessionRef(a.ref) ||
		taskStringValue(entry.Metadata[subagentActivityIDMeta]) != a.activityID || taskTurnSeqFromSpec(entry.Spec) != a.turnSeq {
		return false, nil
	}
	// Refresh the live lifecycle before the successful CAS advances its revision.
	// An intervening external write conflicts and retries from canonical state.
	task := a.runtime.adoptCanonicalSubagentEntry(entry)
	entry = taskapi.CloneEntry(entry)
	entry.ContextUsage = taskapi.CloneContextUsageRecord(record)
	entry.UpdatedAt = a.runtime.runtime.now()
	if err := a.runtime.persistTaskEntryWithConflictInvalidation(ctx, entry, false); err != nil {
		return false, err
	}
	task.mu.Lock()
	if task.activityID == a.activityID && task.turnSeq == a.turnSeq {
		task.contextUsage = taskapi.CloneContextUsageRecord(record)
	}
	task.mu.Unlock()
	return true, nil
}

func contextUsageRecordFromOutput(event output.Event) *taskapi.ContextUsageRecord {
	if event.Event == nil || event.Event.ContextUsage == nil {
		return nil
	}
	record := &taskapi.ContextUsageRecord{Snapshot: session.CloneContextUsageSnapshot(*event.Event.ContextUsage)}
	if event.Event.Invocation != nil {
		record.Invocation = session.CloneEventInvocation(*event.Event.Invocation)
	}
	return record
}
