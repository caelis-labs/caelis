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
	entries := r.tasks.listSessionEntries(ctx, ref)
	for _, entry := range entries {
		if entry == nil || (!entry.Running && !entryHasPendingContinue(entry)) {
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
	rehydrated, err := r.tasks.rehydrateCommandTask(entry)
	if err != nil {
		return nil
	}
	if rehydrated == nil || rehydrated.running {
		return nil
	}
	next := task.CloneEntry(entry)
	next.Running = false
	next.State = task.StateInterrupted
	if next.Result == nil {
		next.Result = map[string]any{}
	}
	next.Result["state"] = string(task.StateInterrupted)
	next.Result["error"] = "task interrupted during resume"
	if strings.TrimSpace(taskStringValue(next.Result["result"])) == "" {
		next.Result["result"] = "task interrupted during resume"
	}
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	next.Metadata["state"] = string(task.StateInterrupted)
	return r.tasks.persistTaskEntry(ctx, next)
}

func (r *Runtime) recoverSubagentEntry(ctx context.Context, entry *task.Entry) error {
	if r == nil || r.tasks == nil || entry == nil {
		return nil
	}
	if entryHasPendingContinue(entry) {
		release, claimed := r.tasks.tryClaimSubagentOperation(entry.Session, entry.TaskID)
		if !claimed {
			return nil
		}
		defer release()
		next := task.CloneEntry(entry)
		next.Running = false
		next.State = task.StateUnknownOutcome
		next.SupportsInput = false
		if next.Result == nil {
			next.Result = map[string]any{}
		}
		if next.Metadata == nil {
			next.Metadata = map[string]any{}
		}
		reason := "runtime restarted after the remote continuation claim; outcome is unknown"
		next.Result["state"] = string(task.StateUnknownOutcome)
		next.Result["error"] = reason
		next.Metadata["continue_phase"] = string(continuePhaseUnknownOutcome)
		next.Metadata["continue_reason"] = reason
		if next.Spec == nil {
			next.Spec = map[string]any{}
		}
		next.Spec["continue_phase"] = string(continuePhaseUnknownOutcome)
		return r.tasks.persistTaskEntry(ctx, next)
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

func (tm *taskRuntime) recoverPendingSubagentControlClaimed(ctx context.Context, subagent *subagentTask) error {
	if tm == nil || subagent == nil || continuePhaseOfTask(subagent) != continuePhasePending {
		return nil
	}
	return tm.markSubagentContinueUnknown(
		context.WithoutCancel(ctx),
		subagent,
		"runtime restarted after the remote continuation claim; outcome is unknown",
	)
}

func entryHasPendingContinue(entry *task.Entry) bool {
	if entry == nil {
		return false
	}
	return normalizeContinuePhase(firstNonEmpty(
		taskStringValue(entry.Metadata["continue_phase"]),
		taskSpecString(entry.Spec, "continue_phase"),
	)) == continuePhasePending
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
