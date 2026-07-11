package runtime

import (
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

func (tm *taskRuntime) reserveSubagentHandle(activeSession session.Session, ref session.SessionRef, agent string) string {
	used := subagentHandlesFromSession(activeSession)
	sessionID := strings.TrimSpace(ref.SessionID)
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for _, task := range tm.subagents {
		if task == nil || strings.TrimSpace(task.sessionRef.SessionID) != sessionID {
			continue
		}
		for _, handle := range []string{task.handle, taskStringValue(task.metadata["handle"]), taskStringValue(task.result["handle"])} {
			if normalized := normalizeSubagentHandle(handle); normalized != "" {
				used[normalized] = struct{}{}
			}
		}
	}
	if sessionID != "" {
		for handle := range tm.handles[sessionID] {
			if normalized := normalizeSubagentHandle(handle); normalized != "" {
				used[normalized] = struct{}{}
			}
		}
	}
	handle := agenthandle.Allocate(used, agent)
	tm.rememberSubagentHandleLocked(sessionID, handle)
	return handle
}

func (tm *taskRuntime) rememberSubagentHandleLocked(sessionID string, handle string) {
	sessionID = strings.TrimSpace(sessionID)
	handle = normalizeSubagentHandle(handle)
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
		handle := normalizeSubagentHandle(participant.Label)
		if handle != "" {
			used[handle] = struct{}{}
		}
	}
	return used
}

func normalizeSubagentHandle(value string) string {
	return taskapi.NormalizeHandle(value)
}
