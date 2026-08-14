package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

func (r *Runtime) recoverRuntimeState(ctx context.Context, ref session.SessionRef) error {
	if r == nil || r.tasks == nil || r.tasks.store == nil {
		return nil
	}
	entries, err := r.tasks.listSessionEntries(ctx, ref)
	if err != nil {
		return fmt.Errorf("agent-sdk/runtime: list durable Task recovery state: %w", err)
	}
	for _, entry := range entries {
		if entry == nil {
			continue
		}
		if handled, err := r.recoverLegacySubagentContinue(ctx, entry); handled {
			if err != nil {
				return err
			}
			continue
		}
		if !entry.Running {
			continue
		}
		switch entry.Kind {
		case task.KindCommand:
			if err := r.recoverCommandEntry(ctx, entry); err != nil {
				return err
			}
		case task.KindSubagent:
			if err := r.recoverSubagentEntry(ctx, entry); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runtime) recoverCommandEntry(ctx context.Context, entry *task.Entry) error {
	if r == nil || r.tasks == nil || entry == nil || !entry.Running {
		return nil
	}
	release, claimed := r.tasks.tryClaimSubagentOperation(entry.Session, entry.TaskID)
	if !claimed {
		return nil
	}
	defer release()
	if r.tasks.hasActiveCommandTask(entry) {
		return nil
	}
	rehydrated, err := r.tasks.rehydrateCommandTask(entry)
	if err != nil {
		state := commandRecoveryState(entry)
		next := commandRecoveryTerminalEntry(entry, state, commandRecoveryDiagnostic(state, err))
		return r.tasks.persistCommandRecoveryEntry(ctx, next)
	}
	if rehydrated == nil {
		state := commandRecoveryState(entry)
		next := commandRecoveryTerminalEntry(
			entry,
			state,
			commandRecoveryDiagnostic(state, fmt.Errorf("command rehydrate returned no task")),
		)
		return r.tasks.persistCommandRecoveryEntry(ctx, next)
	}
	if rehydrated.running {
		phase := taskStringValue(rehydrated.metadata["command_phase"])
		if rehydrated.session == nil && commandUnknownWhileRunningPhase(phase) {
			next := commandRecoveryTerminalEntry(entry, task.StateUnknownOutcome, commandRecoveryDiagnostic(task.StateUnknownOutcome, nil))
			return r.tasks.persistCommandRecoveryEntry(ctx, next)
		}
		return nil
	}
	diagnostic := strings.TrimSpace(taskStringValue(rehydrated.result["error"]))
	next := commandRecoveryTerminalEntry(entry, rehydrated.state, diagnostic)
	return r.tasks.persistCommandRecoveryEntry(ctx, next)
}

func commandRecoveryState(entry *task.Entry) task.State {
	if entry != nil && commandUnknownWhileRunningPhase(taskStringValue(entry.Metadata["command_phase"])) {
		return task.StateUnknownOutcome
	}
	return task.StateInterrupted
}

func commandRecoveryDiagnostic(state task.State, err error) string {
	base := "task interrupted during resume"
	if state == task.StateUnknownOutcome {
		base = "command effect outcome is unavailable after process restart"
	}
	if detail := strings.TrimSpace(errorText(err)); detail != "" {
		return base + ": " + detail
	}
	return base
}

func commandRecoveryTerminalEntry(entry *task.Entry, state task.State, diagnostic string) *task.Entry {
	next := task.CloneEntry(entry)
	if next == nil {
		return nil
	}
	if state != task.StateUnknownOutcome {
		state = task.StateInterrupted
	}
	diagnostic = strings.TrimSpace(diagnostic)
	if diagnostic == "" {
		diagnostic = commandRecoveryDiagnostic(state, nil)
	}

	next.Running = false
	next.State = state
	if state == task.StateUnknownOutcome {
		next.Result = map[string]any{
			"state": string(state),
			"error": diagnostic,
		}
	} else {
		if next.Result == nil {
			next.Result = map[string]any{}
		}
		next.Result["state"] = string(state)
		next.Result["error"] = diagnostic
		if strings.TrimSpace(taskStringValue(next.Result["result"])) == "" {
			next.Result["result"] = diagnostic
		}
	}
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	next.Metadata["state"] = string(state)
	next.Metadata["running"] = false
	if state == task.StateUnknownOutcome {
		if commandCancelPhase(taskStringValue(next.Metadata["command_phase"])) {
			next.Metadata["command_phase"] = commandPhaseCancelUnknown
		} else {
			next.Metadata["command_phase"] = commandPhaseUnknown
		}
	}
	return next
}

func (tm *taskRuntime) persistCommandRecoveryEntry(ctx context.Context, entry *task.Entry) error {
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return err
	}
	tm.mu.RLock()
	cached := tm.tasks[strings.TrimSpace(entry.TaskID)]
	tm.mu.RUnlock()
	if cached != nil && strings.TrimSpace(cached.sessionRef.SessionID) == strings.TrimSpace(entry.Session.SessionID) {
		applyCommandEntry(cached, entry)
	}
	return nil
}

func (tm *taskRuntime) hasActiveCommandTask(entry *task.Entry) bool {
	if tm == nil || entry == nil {
		return false
	}
	taskID := strings.TrimSpace(entry.TaskID)
	sessionID := strings.TrimSpace(entry.Session.SessionID)
	if taskID == "" || sessionID == "" {
		return false
	}
	tm.mu.RLock()
	active := tm.tasks[taskID]
	tm.mu.RUnlock()
	if active == nil || strings.TrimSpace(active.sessionRef.SessionID) != sessionID {
		return false
	}
	active.mu.Lock()
	defer active.mu.Unlock()
	return active.running && active.session != nil
}

func (r *Runtime) recoverSubagentEntry(ctx context.Context, entry *task.Entry) error {
	if r == nil || r.tasks == nil || entry == nil {
		return nil
	}
	if !entry.Running {
		return nil
	}
	if r.tasks.hasActiveSubagentTask(entry) {
		return nil
	}
	next := interruptedSubagentEntry(entry, subagentInterruptedSummary(entry))
	return r.tasks.persistTaskEntry(ctx, next)
}

func subagentInterruptedSummary(entry *task.Entry) string {
	if entry == nil {
		return "subagent interrupted during resume"
	}
	agent := ""
	if entry.Spec != nil {
		if raw, ok := entry.Spec["agent"].(string); ok {
			agent = strings.TrimSpace(raw)
		}
	}
	if agent == "" {
		return "subagent interrupted during resume"
	}
	return fmt.Sprintf("%s interrupted during resume", agent)
}
