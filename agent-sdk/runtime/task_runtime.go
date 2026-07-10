package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
)

const taskIDRandomBytes = 6

func randomTaskID() (string, error) {
	var raw [taskIDRandomBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("agent-sdk/runtime: generate task id: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

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

func (tm *taskRuntime) WaitUntilDone(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest, budget time.Duration) (taskapi.Snapshot, bool, error) {
	if budget <= 0 {
		snapshot, err := tm.Wait(ctx, ref, req)
		return snapshot, false, err
	}
	deadline := time.Now().Add(budget)
	var last taskapi.Snapshot
	var err error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		waitReq := req
		waitReq.Yield = remaining
		started := time.Now()
		last, err = tm.Wait(ctx, ref, waitReq)
		if err != nil {
			return last, false, err
		}
		if !last.Running {
			return last, false, nil
		}
		elapsed := time.Since(started)
		if elapsed < 25*time.Millisecond {
			backoff := minDuration(remaining-elapsed, 100*time.Millisecond)
			if backoff > 0 {
				timer := time.NewTimer(backoff)
				select {
				case <-ctx.Done():
					timer.Stop()
					return last, false, ctx.Err()
				case <-timer.C:
				}
			}
		}
	}
	if last.Running {
		probe := req
		probe.Yield = 0
		if probeSnap, probeErr := tm.Wait(ctx, ref, probe); probeErr == nil {
			last = probeSnap
		}
		return last, last.Running, err
	}
	return last, false, err
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

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

func taskSnapshotToolResult(call tool.Call, def tool.Definition, snapshot taskapi.Snapshot) tool.Result {
	return taskSnapshotToolResultWithPayload(call, def, snapshot, taskToolPayload(snapshot))
}

func taskControlSnapshotToolResult(call tool.Call, def tool.Definition, snapshot taskapi.Snapshot, action string, waitUntilDone bool, timedOut bool, actualWaitMS int) tool.Result {
	if strings.EqualFold(strings.TrimSpace(action), "cancel") {
		return taskSnapshotToolResultWithPayload(call, def, snapshot, taskCancelToolPayload(snapshot))
	}
	payload := taskToolPayload(snapshot)
	if strings.EqualFold(strings.TrimSpace(action), "wait") {
		applyWaitUntilDonePayloadHints(payload, waitUntilDone, timedOut, snapshot)
		payload["actual_wait_time_ms"] = actualWaitMS
	}
	return taskSnapshotToolResultWithPayload(call, def, snapshot, payload)
}

type taskBatchControlItem struct {
	TaskID       string
	Snapshot     taskapi.Snapshot
	Err          error
	OK           bool
	TimedOut     bool
	ActualWaitMS int
}

func taskBatchControlToolResult(call tool.Call, def tool.Definition, items []taskBatchControlItem, action string, waitUntilDone bool, actualWaitMS int) tool.Result {
	payload := taskBatchControlPayload(items, action, waitUntilDone, actualWaitMS)
	raw, _ := json.Marshal(payload)
	return tool.Result{
		ID:      strings.TrimSpace(call.ID),
		Name:    strings.TrimSpace(def.Name),
		Content: []model.Part{model.NewJSONPart(raw)},
		IsError: taskBatchHasError(items),
	}
}

func taskSnapshotToolResultWithPayload(call tool.Call, def tool.Definition, snapshot taskapi.Snapshot, payload map[string]any) tool.Result {
	if payload == nil {
		payload = map[string]any{}
	}
	meta := taskToolMeta(snapshot)
	raw, _ := json.Marshal(payload)
	return tool.Result{
		ID:       strings.TrimSpace(call.ID),
		Name:     strings.TrimSpace(def.Name),
		Content:  []model.Part{model.NewJSONPart(raw)},
		Metadata: meta,
	}
}

func taskBatchControlPayload(items []taskBatchControlItem, action string, waitUntilDone bool, actualWaitMS int) map[string]any {
	tasks := make([]any, 0, len(items))
	normalizedAction := strings.ToLower(strings.TrimSpace(action))
	for _, item := range items {
		if item.Err != nil {
			payload := map[string]any{
				"task_id": strings.TrimSpace(item.TaskID),
				"error":   item.Err.Error(),
			}
			if normalizedAction == "wait" {
				payload["actual_wait_time_ms"] = item.ActualWaitMS
			}
			tasks = append(tasks, payload)
			continue
		}
		var payload map[string]any
		if strings.EqualFold(strings.TrimSpace(action), "cancel") {
			payload = taskCancelToolPayload(item.Snapshot)
		} else {
			payload = taskToolPayload(item.Snapshot)
			if strings.EqualFold(strings.TrimSpace(action), "wait") {
				applyWaitUntilDonePayloadHints(payload, waitUntilDone, item.TimedOut, item.Snapshot)
				payload["actual_wait_time_ms"] = item.ActualWaitMS
			}
		}
		tasks = append(tasks, payload)
	}
	out := map[string]any{
		"action": normalizedAction,
		"count":  len(tasks),
		"failed": taskBatchErrorCount(items),
		"tasks":  tasks,
	}
	if normalizedAction == "wait" {
		out["actual_wait_time_ms"] = actualWaitMS
	}
	return out
}

func applyWaitUntilDonePayloadHints(payload map[string]any, waitUntilDone bool, timedOut bool, snapshot taskapi.Snapshot) {
	if payload == nil || !waitUntilDone {
		return
	}
	if timedOut && snapshot.Running {
		payload["wait_timed_out"] = true
		payload["still_running"] = true
	}
}

func taskBatchHasError(items []taskBatchControlItem) bool {
	return taskBatchErrorCount(items) > 0
}

func taskBatchErrorCount(items []taskBatchControlItem) int {
	count := 0
	for _, item := range items {
		if item.Err != nil {
			count++
		}
	}
	return count
}

func taskCancelToolPayload(snapshot taskapi.Snapshot) map[string]any {
	return map[string]any{
		"task_id": taskVisibleID(snapshot),
		"state":   string(snapshot.State),
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
	return commandTaskToolPayload(snapshot)
}

func commandTaskToolPayload(snapshot taskapi.Snapshot) map[string]any {
	visibleTaskID := taskVisibleID(snapshot)
	payload := map[string]any{
		"task_id": visibleTaskID,
	}
	if snapshot.Running {
		payload["state"] = string(snapshot.State)
		if latestOutput, _ := snapshot.Result["latest_output"].(string); taskOutputHasNonBlankLine(latestOutput) {
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
	if hintCode, _ := snapshot.Result["hint_code"].(string); strings.TrimSpace(hintCode) != "" {
		payload["hint_code"] = strings.TrimSpace(hintCode)
	}
	if hint, _ := snapshot.Result["hint"].(string); strings.TrimSpace(hint) != "" {
		payload["hint"] = strings.TrimSpace(hint)
	}
	if severity, _ := snapshot.Result["hint_severity"].(string); strings.TrimSpace(severity) != "" {
		payload["hint_severity"] = strings.TrimSpace(severity)
	}
	return payload
}

func subagentTaskToolPayload(snapshot taskapi.Snapshot) map[string]any {
	payload := map[string]any{
		"task_id": taskVisibleID(snapshot),
		"state":   string(snapshot.State),
	}
	if snapshot.Running {
		if preview := taskRawStringValue(snapshot.Result["output_preview"]); taskOutputHasNonBlankLine(preview) {
			payload["text"] = preview
		}
		return payload
	}
	finalMessage := firstNonBlankTaskOutput(taskRawStringValue(snapshot.Result["final_message"]), taskRawStringValue(snapshot.Result["result"]))
	if taskOutputHasNonBlankLine(finalMessage) {
		payload["final_message"] = finalMessage
	}
	if errText := taskRawStringValue(snapshot.Result["error"]); taskOutputHasNonBlankLine(errText) {
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
	if store, ok := tm.store.(taskapi.CASStore); ok {
		expected := entry.Revision
		if expected == 0 {
			if current, err := tm.store.Get(ctx, entry.TaskID); err == nil && current != nil {
				expected = current.Revision
				entry.Revision = current.Revision
				if entry.Lease.ID == "" {
					entry.Lease = taskapi.CloneLease(current.Lease)
				}
			}
		}
		persisted, err := store.Put(ctx, taskapi.PutRequest{Entry: entry, ExpectedRevision: expected})
		if err != nil {
			return err
		}
		if persisted != nil {
			*entry = *taskapi.CloneEntry(persisted)
			tm.updateTaskPersistence(entry)
		}
		return nil
	}
	return tm.store.Upsert(ctx, entry)
}

func (tm *taskRuntime) updateTaskPersistence(entry *taskapi.Entry) {
	if tm == nil || entry == nil {
		return
	}
	tm.mu.RLock()
	command := tm.tasks[entry.TaskID]
	subagent := tm.subagents[entry.TaskID]
	tm.mu.RUnlock()
	if command != nil {
		command.mu.Lock()
		command.revision = entry.Revision
		command.lease = taskapi.CloneLease(entry.Lease)
		command.mu.Unlock()
	}
	if subagent != nil {
		subagent.mu.Lock()
		subagent.revision = entry.Revision
		subagent.lease = taskapi.CloneLease(entry.Lease)
		subagent.mu.Unlock()
	}
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

func taskRawStringValue(raw any) string {
	text, _ := raw.(string)
	return text
}

func firstNonBlankTaskOutput(values ...string) string {
	for _, value := range values {
		if taskOutputHasNonBlankLine(value) {
			return value
		}
	}
	return ""
}

func taskOutputHasNonBlankLine(text string) bool {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}
