package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
)

const taskIDRandomBytes = 6

func randomTaskID() (string, error) {
	var raw [taskIDRandomBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate Task identity: %w", err)
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
	normalized := normalizeTaskControlRequest(req)
	if err := validateTaskControlPrincipal(normalized.Principal); err != nil {
		return taskapi.Snapshot{}, err
	}
	identity, err := tm.resolveControlIdentity(ctx, ref, normalized.TaskID)
	if err != nil {
		return taskapi.Snapshot{}, err
	}
	if identity.kind == taskapi.KindCommand {
		task, err := tm.lookupCommand(ctx, ref, identity.taskID)
		if err != nil {
			return taskapi.Snapshot{}, err
		}
		return tm.waitCommandTask(ctx, ref, task, normalized)
	}
	return tm.control(ctx, ref, req, taskControlObserve, func(target taskControlTarget, normalized taskapi.ControlRequest) (taskapi.Snapshot, error) {
		return target.Wait(ctx, normalized)
	})
}

func (tm *taskRuntime) Read(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return tm.control(ctx, ref, req, taskControlObserve, func(target taskControlTarget, normalized taskapi.ControlRequest) (taskapi.Snapshot, error) {
		return target.Read(ctx, normalized)
	})
}

func (tm *taskRuntime) Write(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return tm.control(ctx, ref, req, taskControlExclusive, func(target taskControlTarget, normalized taskapi.ControlRequest) (taskapi.Snapshot, error) {
		return target.Write(ctx, normalized)
	})
}

func (tm *taskRuntime) Cancel(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return tm.control(ctx, ref, req, taskControlCancel, func(target taskControlTarget, normalized taskapi.ControlRequest) (taskapi.Snapshot, error) {
		return target.Cancel(ctx, normalized)
	})
}

func taskSnapshotToolResult(call tool.Call, def tool.Definition, snapshot taskapi.Snapshot) tool.Result {
	return taskSnapshotToolResultWithPayload(call, def, snapshot, taskToolPayload(snapshot))
}

func taskControlSnapshotToolResult(call tool.Call, def tool.Definition, snapshot taskapi.Snapshot, action string, actualWaitMS int) tool.Result {
	if strings.EqualFold(strings.TrimSpace(action), "cancel") {
		if snapshot.Kind == taskapi.KindSubagent {
			return taskSnapshotTextToolResult(call, def, snapshot, subagentInterruptedText(snapshot))
		}
		return taskSnapshotToolResultWithPayload(call, def, snapshot, taskCancelToolPayload(snapshot))
	}
	payload := taskToolPayload(snapshot)
	if strings.EqualFold(strings.TrimSpace(action), "wait") {
		payload["actual_wait_time_ms"] = actualWaitMS
	}
	return taskSnapshotToolResultWithPayload(call, def, snapshot, payload)
}

func taskSnapshotTextToolResult(call tool.Call, def tool.Definition, snapshot taskapi.Snapshot, text string) tool.Result {
	return tool.Result{
		ID:       strings.TrimSpace(call.ID),
		Name:     strings.TrimSpace(def.Name),
		Content:  []model.Part{model.NewTextPart(strings.TrimSpace(text))},
		Metadata: taskToolMeta(snapshot),
	}
}

type taskBatchControlItem struct {
	Handle       string
	Snapshot     taskapi.Snapshot
	Err          error
	OK           bool
	ActualWaitMS int
}

func taskBatchControlToolResult(call tool.Call, def tool.Definition, items []taskBatchControlItem, action string, actualWaitMS int) tool.Result {
	payload := taskBatchControlPayload(items, action, actualWaitMS)
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

func taskBatchControlPayload(items []taskBatchControlItem, action string, actualWaitMS int) map[string]any {
	tasks := make([]any, 0, len(items))
	normalizedAction := strings.ToLower(strings.TrimSpace(action))
	for _, item := range items {
		if item.Err != nil {
			payload := map[string]any{
				"handle": strings.TrimSpace(item.Handle),
				"error":  item.Err.Error(),
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
	if snapshot.Kind == taskapi.KindSubagent {
		return map[string]any{"message": subagentInterruptedText(snapshot)}
	}
	return map[string]any{
		"handle": taskPublicHandle(snapshot),
		"state":  string(snapshot.State),
	}
}

func subagentInterruptedText(snapshot taskapi.Snapshot) string {
	handle := strings.TrimPrefix(taskPublicHandle(snapshot), "@")
	if snapshot.Running {
		return fmt.Sprintf("Cancel requested for @%s; wait for it to stop.", handle)
	}
	switch snapshot.State {
	case taskapi.StateCompleted, taskapi.StateFailed:
		return fmt.Sprintf("Subagent @%s is already %s.", handle, snapshot.State)
	default:
		return fmt.Sprintf("Subagent @%s is interrupted.", handle)
	}
}

func taskToolMeta(snapshot taskapi.Snapshot) map[string]any {
	meta := map[string]any{}
	taskMeta := taskRuntimeMetaSection(meta, "task")
	taskMeta["kind"] = strings.TrimSpace(string(snapshot.Kind))
	taskMeta["state"] = strings.TrimSpace(string(snapshot.State))
	taskMeta["running"] = snapshot.Running
	taskMeta["task_id"] = strings.TrimSpace(snapshot.Ref.TaskID)
	taskMeta["handle"] = taskPublicHandle(snapshot)
	if sessionID := strings.TrimSpace(snapshot.Ref.SessionID); sessionID != "" {
		taskMeta["session_id"] = sessionID
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
	if cursor, ok := taskInt64Value(snapshot.Metadata["output_start_cursor"]); ok && cursor >= 0 {
		taskMeta["output_start_cursor"] = cursor
	}
	if delta, ok := snapshot.Metadata["output_delta"].(string); ok && delta != "" {
		taskMeta["output_delta"] = delta
	}
	if snapshot.Kind == taskapi.KindSubagent && snapshot.EventCursor >= 0 {
		taskMeta["event_cursor"] = snapshot.EventCursor
	}
	if terminalID := firstNonEmpty(strings.TrimSpace(snapshot.Terminal.TerminalID), strings.TrimSpace(snapshot.Ref.TerminalID), taskStringValue(snapshot.Metadata["terminal_id"])); terminalID != "" {
		taskMeta["terminal_id"] = terminalID
	}
	for _, key := range []string{"source", "participant_role", "agent", "agent_id", "handle", "mention", "prompt", "turn_id", "turn_seq", "parent_call", "parent_tool"} {
		if value, ok := snapshot.Metadata[key]; ok {
			taskMeta[key] = value
		}
	}
	return meta
}

func taskToolPayload(snapshot taskapi.Snapshot) map[string]any {
	var payload map[string]any
	if snapshot.Kind == taskapi.KindSubagent {
		payload = subagentTaskToolPayload(snapshot)
	} else {
		payload = commandTaskToolPayload(snapshot)
	}
	if payload == nil {
		payload = map[string]any{}
	}
	if targetKind := strings.TrimSpace(string(snapshot.Kind)); targetKind != "" {
		payload["target_kind"] = targetKind
	}
	if snapshot.Kind == taskapi.KindSubagent {
		if turnID := firstNonEmpty(
			taskStringValue(snapshot.Result["turn_id"]),
			taskStringValue(snapshot.Metadata["turn_id"]),
		); turnID != "" {
			payload["turn_id"] = turnID
		}
	}
	for _, key := range []string{"parent_call", "parent_tool"} {
		if value := strings.TrimSpace(taskStringValue(snapshot.Metadata[key])); value != "" {
			payload[key] = value
		}
	}
	if snapshot.Kind == taskapi.KindCommand && strings.TrimSpace(taskStringValue(payload["parent_call"])) != "" {
		// Command Tasks are created only by RunCommand. Carry that canonical
		// relation in the model-visible result even for snapshots rehydrated
		// from older entries that persisted only the parent call ID.
		payload["parent_tool"] = shell.RunCommandToolName
	}
	return payload
}

func commandTaskToolPayload(snapshot taskapi.Snapshot) map[string]any {
	payload := map[string]any{
		"handle": taskPublicHandle(snapshot),
	}
	if snapshot.Running {
		payload["state"] = string(snapshot.State)
		payload["supports_input"] = snapshot.SupportsInput
		if errText, _ := snapshot.Result["error"].(string); strings.TrimSpace(errText) != "" {
			payload["error"] = strings.TrimSpace(errText)
		}
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
	if hint, _ := snapshot.Result["system_hint"].(string); strings.TrimSpace(hint) != "" {
		payload["system_hint"] = strings.TrimSpace(hint)
	}
	return payload
}

func subagentTaskToolPayload(snapshot taskapi.Snapshot) map[string]any {
	payload := map[string]any{
		"handle": taskPublicHandle(snapshot),
		"state":  string(snapshot.State),
	}
	if responses, ok := snapshot.Result[subagentFinalResponsesResultKey]; ok {
		payload[subagentFinalResponsesResultKey] = responses
		if items, valid := responses.([]any); valid && len(items) > 0 {
			if latest, mapped := items[len(items)-1].(map[string]any); mapped {
				if finalMessage := taskRawStringValue(latest["final_message"]); taskOutputHasNonBlankLine(finalMessage) {
					// Keep the singular field as a compatibility alias for the
					// newest unread response. final_responses is authoritative when
					// more than one child Turn completed between observations.
					payload["final_message"] = finalMessage
				}
			}
		}
	}
	if diagnostic, ok := subagentFailureDiagnostic(snapshot.State, taskRawStringValue(snapshot.Result["error"])); ok {
		payload["error"] = diagnostic
		return payload
	}
	if snapshot.Running {
		payload["activity_cursor"] = snapshot.EventCursor
		if preview := taskRawStringValue(snapshot.Result["output_preview"]); taskOutputHasNonBlankLine(preview) {
			payload["output_preview"] = preview
		}
		return payload
	}
	finalMessage := firstNonBlankTaskOutput(taskRawStringValue(snapshot.Result["final_message"]), taskRawStringValue(snapshot.Result["result"]))
	if taskOutputHasNonBlankLine(finalMessage) {
		payload["final_message"] = finalMessage
	}
	return payload
}

func taskPublicHandle(snapshot taskapi.Snapshot) string {
	return normalizeTaskHandle(firstNonEmpty(snapshot.Handle, taskStringValue(snapshot.Result["handle"]), taskStringValue(snapshot.Metadata["handle"]), strings.TrimSpace(snapshot.Ref.TaskID)))
}

func (tm *taskRuntime) persistTaskEntry(ctx context.Context, entry *taskapi.Entry) error {
	return tm.persistTaskEntryWithConflictInvalidation(ctx, entry, true)
}

func (tm *taskRuntime) persistTaskEntryWithConflictInvalidation(ctx context.Context, entry *taskapi.Entry, invalidateOnConflict bool) error {
	if tm == nil || tm.store == nil || entry == nil {
		return nil
	}
	if entry.Kind == taskapi.KindSubagent {
		normalizeSubagentEntryResult(entry, entry.FailureDiagnostic)
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
			if !session.IsCommitted(err) {
				var conflict *taskapi.RevisionConflictError
				if invalidateOnConflict && entry.Kind == taskapi.KindSubagent && errors.As(err, &conflict) {
					tm.invalidateSubagentTask(entry.Session, entry.TaskID, expected)
				}
				return err
			}
			committedErr := err
			if !sameCommittedTaskEntry(persisted, entry, expected) {
				reloaded, loadErr := tm.store.Get(context.WithoutCancel(ctx), entry.TaskID)
				if loadErr != nil || !sameCommittedTaskEntry(reloaded, entry, expected) {
					return errors.Join(committedErr, loadErr)
				}
				persisted = reloaded
			}
		}
		if persisted != nil {
			*entry = *taskapi.CloneEntry(persisted)
			tm.updateTaskPersistence(entry)
			tm.notifyTaskCommitted(entry)
		}
		return nil
	}
	if err := tm.store.Upsert(ctx, entry); err != nil {
		return err
	}
	tm.notifyTaskCommitted(entry)
	return nil
}

func sameCommittedTaskEntry(persisted, requested *taskapi.Entry, expected uint64) bool {
	if persisted == nil || requested == nil || persisted.Revision != expected+1 {
		return false
	}
	want := taskapi.SanitizeEntryForPersistence(requested, taskapi.ResultPersistenceCanonical)
	if want == nil {
		return false
	}
	want.Revision = persisted.Revision
	return reflect.DeepEqual(taskapi.CloneEntry(persisted), want)
}

func (tm *taskRuntime) persistSpawnEntry(ctx context.Context, entry *taskapi.Entry) error {
	if tm == nil || tm.store == nil || entry == nil {
		return errors.New("durable CAS Task store is required before subagent spawn")
	}
	if entry.Kind == taskapi.KindSubagent {
		normalizeSubagentEntryResult(entry, entry.FailureDiagnostic)
	}
	store, ok := tm.store.(taskapi.CASStore)
	if !ok {
		return errors.New("subagent spawn requires task.CASStore")
	}
	expected := entry.Revision
	persisted, err := store.Put(ctx, taskapi.PutRequest{Entry: entry, ExpectedRevision: expected})
	if err != nil {
		if !session.IsCommitted(err) {
			tm.invalidateSubagentTask(entry.Session, entry.TaskID, expected)
			return err
		}
		committedErr := err
		if !sameCommittedTaskEntry(persisted, entry, expected) {
			reloaded, loadErr := tm.store.Get(context.WithoutCancel(ctx), entry.TaskID)
			if loadErr != nil || !sameCommittedTaskEntry(reloaded, entry, expected) {
				return errors.Join(committedErr, loadErr)
			}
			persisted = reloaded
		}
	}
	if persisted == nil {
		return errors.New("CAS Task store returned no persisted spawn entry")
	}
	*entry = *taskapi.CloneEntry(persisted)
	tm.updateTaskPersistence(entry)
	tm.notifyTaskCommitted(entry)
	return nil
}

func (tm *taskRuntime) notifyTaskCommitted(entry *taskapi.Entry) {
	if tm == nil || entry == nil {
		return
	}
	if tm.taskCommitted != nil {
		tm.taskCommitted(taskapi.CloneEntry(entry))
	}
	if tm.activityChanged != nil && !entry.Running {
		tm.activityChanged(session.NormalizeSessionRef(entry.Session))
	}
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

func (tm *taskRuntime) listSessionEntries(ctx context.Context, ref session.SessionRef) ([]*taskapi.Entry, error) {
	if tm == nil {
		return nil, nil
	}
	if tm.store != nil {
		listed, err := tm.store.ListSession(ctx, ref)
		if err != nil {
			return nil, err
		}
		out := make([]*taskapi.Entry, 0, len(listed))
		for _, entry := range listed {
			out = append(out, taskapi.CloneEntry(entry))
		}
		return out, nil
	}
	return nil, nil
}

func taskSpecString(values map[string]any, key string) string {
	if values == nil {
		return ""
	}
	raw := values[key]
	text, _ := raw.(string)
	return strings.TrimSpace(text)
}

func taskSpecBool(values map[string]any, key string) bool {
	if values == nil {
		return false
	}
	value, _ := values[key].(bool)
	return value
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
