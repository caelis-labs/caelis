package local

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/task"
)

func (r *Runtime) recoverRuntimeState(ctx context.Context, ref session.SessionRef) error {
	if r == nil || r.tasks == nil || r.tasks.store == nil {
		return nil
	}
	entries := r.tasks.listSessionEntries(ctx, ref)
	for _, entry := range entries {
		if entry == nil || !entry.Running {
			continue
		}
		switch entry.Kind {
		case task.KindBash:
			if err := r.recoverBashEntry(ctx, entry); err != nil {
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

func (r *Runtime) recoverBashEntry(ctx context.Context, entry *task.Entry) error {
	if r == nil || r.tasks == nil || entry == nil || !entry.Running {
		return nil
	}
	rehydrated, err := r.tasks.rehydrateBashTask(entry)
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
	if r == nil || r.tasks == nil || entry == nil || !entry.Running {
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
