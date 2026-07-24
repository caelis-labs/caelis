package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

func (tm *taskRuntime) lookupCommand(ctx context.Context, ref session.SessionRef, taskID string) (*commandTask, error) {
	tm.mu.RLock()
	task, ok := tm.tasks[strings.TrimSpace(taskID)]
	tm.mu.RUnlock()
	if ok && task != nil {
		if strings.TrimSpace(task.sessionRef.SessionID) != strings.TrimSpace(ref.SessionID) {
			return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
		}
		return task, nil
	}
	if tm.store == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	entry, err := tm.store.Get(ctx, strings.TrimSpace(taskID))
	if err != nil || entry == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	if strings.TrimSpace(entry.Session.SessionID) != strings.TrimSpace(ref.SessionID) {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	if entry.Kind != taskapi.KindCommand {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	entry, err = tm.backfillCanonicalTaskEntry(ctx, ref, entry)
	if err != nil {
		return nil, err
	}
	rehydrated, err := tm.rehydrateCommandTask(entry)
	if err != nil {
		return nil, err
	}
	tm.mu.Lock()
	tm.tasks[rehydrated.ref.TaskID] = rehydrated
	tm.mu.Unlock()
	return rehydrated, nil
}

// lookupCommandCanonical reloads durable command state while the caller holds
// the session-scoped operation claim. A configured store failure is returned
// before any sandbox side effect; cached state is never used as a fallback.
func (tm *taskRuntime) lookupCommandCanonical(ctx context.Context, ref session.SessionRef, taskID string) (*commandTask, error) {
	if tm == nil || tm.store == nil {
		return tm.lookupCommand(ctx, ref, taskID)
	}
	ref = session.NormalizeSessionRef(ref)
	taskID = strings.TrimSpace(taskID)
	entry, err := tm.store.Get(ctx, taskID)
	if err != nil {
		return nil, fmt.Errorf("agent-sdk/runtime: reload command task %q: %w", taskID, err)
	}
	if !storedTaskEntryMatches(entry, ref, taskapi.KindCommand) {
		return nil, fmt.Errorf("agent-sdk/runtime: task %q not found", taskID)
	}
	entry, err = tm.backfillCanonicalTaskEntry(ctx, ref, entry)
	if err != nil {
		return nil, err
	}
	command, err := tm.commandFromDurableEntry(entry)
	if err != nil {
		return nil, err
	}
	tm.installCommandTask(command)
	return command, nil
}

func (tm *taskRuntime) commandFromDurableEntry(entry *taskapi.Entry) (*commandTask, error) {
	if tm == nil || entry == nil {
		return nil, fmt.Errorf("agent-sdk/runtime: task entry is required")
	}
	tm.mu.RLock()
	current := tm.tasks[strings.TrimSpace(entry.TaskID)]
	tm.mu.RUnlock()
	if current == nil || !entry.Running {
		return tm.rehydrateCommandTask(entry)
	}

	current.mu.Lock()
	if !commandCanAdoptDurableEntryLocked(current, entry) {
		current.mu.Unlock()
		return tm.rehydrateCommandTask(entry)
	}
	current.sessionRef = session.NormalizeSessionRef(entry.Session)
	current.handle = firstNonEmpty(entry.Handle, taskSpecString(entry.Spec, "handle"), taskStringValue(entry.Metadata["handle"]))
	current.command = taskSpecString(entry.Spec, "command")
	current.workdir = taskSpecString(entry.Spec, "workdir")
	current.parentCall = taskSpecString(entry.Spec, "parent_call")
	current.requestDigest = taskSpecString(entry.Spec, "command_request_digest")
	current.title = strings.TrimSpace(entry.Title)
	current.createdAt = entry.CreatedAt
	current.revision = entry.Revision
	current.lease = taskapi.CloneLease(entry.Lease)
	current.state = entry.State
	current.running = entry.Running
	current.outputState.applyDurableCheckpoint(parseCommandOutputCheckpoint(entry))
	current.result = session.CloneState(entry.Result)
	current.metadata = session.CloneState(entry.Metadata)
	current.mu.Unlock()
	return current, nil
}

func commandCanAdoptDurableEntryLocked(current *commandTask, entry *taskapi.Entry) bool {
	if current == nil || current.session == nil || entry == nil {
		return false
	}
	if commandSessionMatchesEntry(current.session, entry) {
		return true
	}
	phase := taskStringValue(entry.Metadata["command_phase"])
	return strings.TrimSpace(entry.Terminal.SessionID) == "" &&
		(phase == commandPhaseEffectClaimed || phase == commandPhaseUnknown) &&
		current.requestDigest != "" && current.requestDigest == taskSpecString(entry.Spec, "command_request_digest")
}

func commandSessionMatchesEntry(handle sandbox.Session, entry *taskapi.Entry) bool {
	if handle == nil || entry == nil {
		return false
	}
	terminal := handle.Terminal()
	return strings.TrimSpace(terminal.SessionID) != "" &&
		strings.TrimSpace(terminal.SessionID) == strings.TrimSpace(entry.Terminal.SessionID) &&
		strings.TrimSpace(terminal.TerminalID) == strings.TrimSpace(entry.Terminal.TerminalID)
}
