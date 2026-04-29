package local

import (
	"context"
	"fmt"
	"strings"

	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktask "github.com/OnslaughtSnail/caelis/sdk/task"
)

func (r *Runtime) recoverRuntimeState(ctx context.Context, ref sdksession.SessionRef) error {
	if r == nil || r.tasks == nil || r.tasks.store == nil {
		return nil
	}
	entries := r.tasks.listSessionEntries(ctx, ref)
	for _, entry := range entries {
		if entry == nil || !entry.Running {
			continue
		}
		switch entry.Kind {
		case sdktask.KindBash:
			if err := r.recoverBashEntry(ctx, entry); err != nil {
				return err
			}
		case sdktask.KindSubagent:
			if err := r.recoverSubagentEntry(ctx, entry); err != nil {
				return err
			}
		}
	}
	return nil
}

func (r *Runtime) recoverBashEntry(ctx context.Context, entry *sdktask.Entry) error {
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
	next := sdktask.CloneEntry(entry)
	next.Running = false
	next.State = sdktask.StateInterrupted
	if next.Result == nil {
		next.Result = map[string]any{}
	}
	next.Result["state"] = string(sdktask.StateInterrupted)
	next.Result["error"] = "task interrupted during resume"
	if _, ok := next.Result["result"]; !ok {
		next.Result["result"] = "task interrupted during resume"
	}
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	next.Metadata["state"] = string(sdktask.StateInterrupted)
	return r.tasks.persistTaskEntry(ctx, next)
}

func (r *Runtime) recoverSubagentEntry(ctx context.Context, entry *sdktask.Entry) error {
	if r == nil || r.tasks == nil || entry == nil || !entry.Running {
		return nil
	}
	if r.subagents != nil {
		return nil
	}
	next := sdktask.CloneEntry(entry)
	next.Running = false
	next.State = sdktask.StateInterrupted
	if next.Result == nil {
		next.Result = map[string]any{}
	}
	next.Result["state"] = string(sdktask.StateInterrupted)
	next.Result["result"] = subagentInterruptedSummary(entry)
	if next.Metadata == nil {
		next.Metadata = map[string]any{}
	}
	next.Metadata["state"] = string(sdktask.StateInterrupted)
	return r.tasks.persistTaskEntry(ctx, next)
}

func subagentInterruptedSummary(entry *sdktask.Entry) string {
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
