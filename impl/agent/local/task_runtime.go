package local

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
	"github.com/OnslaughtSnail/caelis/ports/session"
	taskapi "github.com/OnslaughtSnail/caelis/ports/task"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

type taskToolObserver struct {
	call     tool.Call
	def      tool.Definition
	observer tool.Observer
}

func (o taskToolObserver) ObserveTaskSnapshot(snapshot taskapi.Snapshot) {
	if o.observer == nil {
		return
	}
	o.observer.ObserveToolResult(taskSnapshotToolResult(o.call, o.def, snapshot))
}

func (tm *taskRuntime) Wait(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return tm.control(ctx, ref, req, func(target taskControlTarget) (taskapi.Snapshot, error) {
		return target.Wait(ctx, req)
	})
}

func (tm *taskRuntime) Write(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return tm.control(ctx, ref, req, func(target taskControlTarget) (taskapi.Snapshot, error) {
		return target.Write(ctx, req)
	})
}

func (tm *taskRuntime) Cancel(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return tm.control(ctx, ref, req, func(target taskControlTarget) (taskapi.Snapshot, error) {
		return target.Cancel(ctx, req)
	})
}

func canonicalTaskResult(result map[string]any) map[string]any {
	if result == nil {
		return nil
	}
	out, _ := tool.TruncateMap(result, tool.DefaultTruncationPolicy())
	return out
}

func taskSnapshotToolResult(call tool.Call, def tool.Definition, snapshot taskapi.Snapshot) tool.Result {
	payload := taskToolPayload(snapshot)
	if payload == nil {
		payload = map[string]any{}
	}
	payload, _ = tool.TruncateMap(payload, tool.DefaultTruncationPolicy())
	meta := taskToolMeta(snapshot)
	raw, _ := json.Marshal(payload)
	return tool.Result{
		ID:       strings.TrimSpace(call.ID),
		Name:     strings.TrimSpace(def.Name),
		Content:  []model.Part{model.NewJSONPart(raw)},
		Metadata: meta,
	}
}

func taskToolMeta(snapshot taskapi.Snapshot) map[string]any {
	meta := map[string]any{}
	taskMeta := taskRuntimeMetaSection(meta, "task")
	visibleTaskID := taskVisibleID(snapshot)
	taskMeta["kind"] = strings.TrimSpace(string(snapshot.Kind))
	taskMeta["state"] = strings.TrimSpace(string(snapshot.State))
	taskMeta["running"] = snapshot.Running
	taskMeta["task_id"] = visibleTaskID
	if sessionID := strings.TrimSpace(snapshot.Ref.SessionID); sessionID != "" {
		taskMeta["session_id"] = sessionID
	}
	if internalTaskID := strings.TrimSpace(snapshot.Ref.TaskID); snapshot.Kind != taskapi.KindSubagent && internalTaskID != "" && internalTaskID != visibleTaskID {
		taskMeta["internal_task_id"] = internalTaskID
	}
	if cursor, ok := taskInt64Value(snapshot.Metadata["output_cursor"]); ok && cursor >= 0 {
		taskMeta["output_cursor"] = cursor
	} else if snapshot.Kind == taskapi.KindSubagent && snapshot.StdoutCursor >= 0 {
		taskMeta["output_cursor"] = snapshot.StdoutCursor
	} else if snapshot.Kind != taskapi.KindSubagent {
		if text, _ := snapshot.Result["result"].(string); text != "" {
			taskMeta["output_cursor"] = int64(len([]byte(text)))
		}
	}
	if terminalID := firstNonEmpty(strings.TrimSpace(snapshot.Terminal.TerminalID), strings.TrimSpace(snapshot.Ref.TerminalID), taskStringValue(snapshot.Metadata["terminal_id"])); terminalID != "" {
		taskMeta["terminal_id"] = terminalID
	}
	for _, key := range []string{"source", "interaction", "agent", "agent_id", "handle", "mention", "prompt", "turn_id", "turn_seq"} {
		if value, ok := snapshot.Metadata[key]; ok {
			taskMeta[key] = value
		}
	}
	return meta
}

func taskToolPayload(snapshot taskapi.Snapshot) map[string]any {
	if snapshot.Kind == taskapi.KindSubagent {
		return subagentTaskToolPayload(snapshot)
	}
	return bashTaskToolPayload(snapshot)
}

func bashTaskToolPayload(snapshot taskapi.Snapshot) map[string]any {
	visibleTaskID := taskVisibleID(snapshot)
	payload := map[string]any{}
	if snapshot.Running {
		payload["task_id"] = visibleTaskID
		payload["state"] = string(snapshot.State)
		if latestOutput, _ := snapshot.Result["latest_output"].(string); strings.TrimSpace(latestOutput) != "" {
			payload["latest_output"] = latestOutput
		}
		return payload
	}
	payload["state"] = string(snapshot.State)
	if text, _ := snapshot.Result["result"].(string); text != "" {
		payload["result"] = text
	}
	if errText, _ := snapshot.Result["error"].(string); strings.TrimSpace(errText) != "" {
		payload["error"] = strings.TrimSpace(errText)
	}
	if exitCode, ok := snapshot.Result["exit_code"]; ok {
		payload["exit_code"] = exitCode
	}
	return payload
}

func subagentTaskToolPayload(snapshot taskapi.Snapshot) map[string]any {
	payload := map[string]any{
		"task_id": taskVisibleID(snapshot),
		"state":   string(snapshot.State),
	}
	if snapshot.Running {
		if preview := strings.TrimSpace(taskStringValue(snapshot.Result["output_preview"])); preview != "" {
			payload["text"] = preview
		}
		return payload
	}
	finalMessage := firstNonEmpty(taskStringValue(snapshot.Result["final_message"]), taskStringValue(snapshot.Result["result"]))
	if strings.TrimSpace(finalMessage) != "" {
		payload["final_message"] = strings.TrimSpace(finalMessage)
	}
	if errText := strings.TrimSpace(taskStringValue(snapshot.Result["error"])); errText != "" {
		payload["error"] = errText
	}
	return payload
}

func taskVisibleID(snapshot taskapi.Snapshot) string {
	if snapshot.Kind == taskapi.KindSubagent {
		if handle := firstNonEmpty(taskStringValue(snapshot.Result["handle"]), taskStringValue(snapshot.Metadata["handle"])); handle != "" {
			return normalizeSubagentHandle(handle)
		}
	}
	return strings.TrimSpace(snapshot.Ref.TaskID)
}

func (tm *taskRuntime) persistTaskEntry(ctx context.Context, entry *taskapi.Entry) error {
	if tm == nil || tm.store == nil || entry == nil {
		return nil
	}
	return tm.store.Upsert(ctx, entry)
}

func (tm *taskRuntime) listSessionEntries(ctx context.Context, ref session.SessionRef) []*taskapi.Entry {
	if tm == nil {
		return nil
	}
	if tm.store != nil {
		listed, err := tm.store.ListSession(ctx, ref)
		if err == nil && len(listed) > 0 {
			out := make([]*taskapi.Entry, 0, len(listed))
			for _, entry := range listed {
				out = append(out, taskapi.CloneEntry(entry))
			}
			return out
		}
	}
	sessionID := strings.TrimSpace(ref.SessionID)
	tm.mu.RLock()
	ids := append([]string(nil), tm.order[sessionID]...)
	tm.mu.RUnlock()
	out := make([]*taskapi.Entry, 0, len(ids))
	for _, taskID := range ids {
		tm.mu.RLock()
		if task, ok := tm.tasks[taskID]; ok && task != nil {
			task.mu.Lock()
			out = append(out, task.entrySnapshot(tm.runtime.now()))
			task.mu.Unlock()
			tm.mu.RUnlock()
			continue
		}
		if task, ok := tm.subagents[taskID]; ok && task != nil {
			task.mu.Lock()
			out = append(out, task.entrySnapshot(tm.runtime.now()))
			task.mu.Unlock()
		}
		tm.mu.RUnlock()
	}
	return out
}

func taskSpecString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	raw := values[key]
	text, _ := raw.(string)
	return strings.TrimSpace(text)
}

func taskStringValue(raw any) string {
	text, _ := raw.(string)
	return strings.TrimSpace(text)
}
