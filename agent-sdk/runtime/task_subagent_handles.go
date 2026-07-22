package runtime

import (
	"context"
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/agenthandle"
)

func subagentTerminalID(taskID string) string {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return ""
	}
	return "subagent-" + taskID
}

func allocateSubagentHandle(activeSession session.Session, agent string) string {
	return agenthandle.Allocate(subagentHandlesFromSession(activeSession), agent)
}

func (tm *taskRuntime) reserveTaskHandle(ctx context.Context, activeSession session.Session, ref session.SessionRef, kind taskapi.Kind, hint string) (string, error) {
	used := subagentHandlesFromSession(activeSession)
	sessionID := strings.TrimSpace(ref.SessionID)
	if tm.store != nil && sessionID != "" {
		entries, err := tm.store.ListSession(ctx, session.NormalizeSessionRef(ref))
		if err != nil {
			return "", fmt.Errorf("agent-sdk/runtime: list task handles: %w", err)
		}
		for _, entry := range entries {
			if entry == nil {
				continue
			}
			handle := firstNonEmpty(entry.Handle, taskStringValue(entry.Result["handle"]), taskStringValue(entry.Metadata["handle"]), taskSpecString(entry.Spec, "handle"))
			if normalized := normalizeTaskHandle(handle); normalized != "" {
				used[normalized] = struct{}{}
			}
		}
	}
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, task := range tm.tasks {
		if task == nil || strings.TrimSpace(task.sessionRef.SessionID) != sessionID {
			continue
		}
		if normalized := normalizeTaskHandle(task.handle); normalized != "" {
			used[normalized] = struct{}{}
		}
	}
	for _, task := range tm.subagents {
		if task == nil || strings.TrimSpace(task.sessionRef.SessionID) != sessionID {
			continue
		}
		for _, handle := range []string{task.handle, taskStringValue(task.metadata["handle"]), taskStringValue(task.result["handle"])} {
			if normalized := normalizeTaskHandle(handle); normalized != "" {
				used[normalized] = struct{}{}
			}
		}
	}
	if sessionID != "" {
		for handle := range tm.handles[sessionID] {
			if normalized := normalizeTaskHandle(handle); normalized != "" {
				used[normalized] = struct{}{}
			}
		}
	}
	handle := allocateTaskHandle(used, kind, hint)
	tm.rememberTaskHandleLocked(sessionID, handle)
	return handle, nil
}

func allocateTaskHandle(used map[string]struct{}, kind taskapi.Kind, hint string) string {
	if kind == taskapi.KindSubagent {
		return agenthandle.Allocate(used, hint)
	}
	base := agenthandle.NormalizeBase(hint)
	if base == "" {
		base = "task"
	}
	for suffix := 1; ; suffix++ {
		candidate := base
		if suffix > 1 {
			candidate = fmt.Sprintf("%s-%d", base, suffix)
		}
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
}

func (tm *taskRuntime) rememberTaskHandleLocked(sessionID string, handle string) {
	sessionID = strings.TrimSpace(sessionID)
	handle = normalizeTaskHandle(handle)
	if sessionID == "" || handle == "" {
		return
	}
	if tm.handles == nil {
		tm.handles = map[string]map[string]struct{}{}
	}
	if tm.handles[sessionID] == nil {
		tm.handles[sessionID] = map[string]struct{}{}
	}
	tm.handles[sessionID][handle] = struct{}{}
}

func subagentHandlesFromSession(activeSession session.Session) map[string]struct{} {
	used := map[string]struct{}{}
	for _, participant := range activeSession.Participants {
		handle := normalizeTaskHandle(participant.Label)
		if handle != "" {
			used[handle] = struct{}{}
		}
	}
	return used
}

func normalizeTaskHandle(value string) string {
	return taskapi.NormalizeHandle(value)
}
