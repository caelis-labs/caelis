package runtime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/internal/toolbinding"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/tool"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/codemode"
)

const taskIDRandomBytes = 6

func randomTaskID() (string, error) {
	var raw [taskIDRandomBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate Task identity: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}

func taskIDForOperation(ref session.SessionRef, originalID string) (string, error) {
	originalID = strings.TrimSpace(originalID)
	if originalID == "" {
		return randomTaskID()
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(ref.SessionID) + "\x00" + originalID))
	return hex.EncodeToString(sum[:taskIDRandomBytes]), nil
}

type taskToolObserver struct {
	ctx context.Context
}

func (o taskToolObserver) ObserveTaskSnapshot(snapshot taskapi.Snapshot) {
	ctx, _ := toolbinding.Observe(o.ctx)
	result, _ := taskSnapshotToolResult(ctx, snapshot)
	toolbinding.ReportProgress(ctx, result)
}

func (tm *taskRuntime) Wait(ctx context.Context, ref session.SessionRef, req taskapi.ControlRequest) (taskapi.Snapshot, error) {
	return tm.control(ctx, ref, req, taskControlWait, func(target taskControlTarget, normalized taskapi.ControlRequest) (taskapi.Snapshot, error) {
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

func taskSnapshotToolResult(ctx context.Context, snapshot taskapi.Snapshot) (tool.Result, error) {
	return taskSnapshotToolResultWithPayload(ctx, snapshot, taskToolPayload(snapshot))
}

func taskControlSnapshotToolResult(ctx context.Context, snapshot taskapi.Snapshot, action string, actualWaitMS int) (tool.Result, error) {
	if strings.EqualFold(strings.TrimSpace(action), "cancel") {
		return taskSnapshotToolResultWithPayload(ctx, snapshot, taskCancelToolPayload(snapshot))
	}
	payload := taskToolPayload(snapshot)
	if strings.EqualFold(strings.TrimSpace(action), "wait") {
		payload["actual_wait_time_ms"] = actualWaitMS
	}
	return taskSnapshotToolResultWithPayload(ctx, snapshot, payload)
}

type taskBatchControlItem struct {
	Handle       string
	Snapshot     taskapi.Snapshot
	Err          error
	ActualWaitMS int
}

func taskBatchControlToolResult(items []taskBatchControlItem, action string, actualWaitMS int) (tool.Result, error) {
	payload := taskBatchControlPayload(items, action, actualWaitMS)
	result := tool.Result{}
	failed := taskBatchErrorCount(items) > 0
	if action != "cancel" {
		for i, item := range items {
			if item.Err != nil || item.Snapshot.Kind != taskapi.KindCodeMode {
				continue
			}
			itemPayload := payload["tasks"].([]map[string]any)[i]
			if selected, legacy, ok := takeCodeModeTaskResult(itemPayload); ok {
				failed = failed || legacy.Failed
				addCodeModeTaskResult(&result, itemPayload, selected)
			}
		}
	}
	return withTaskPayload(result, payload, failed)
}

func taskSnapshotToolResultWithPayload(ctx context.Context, snapshot taskapi.Snapshot, payload map[string]any) (tool.Result, error) {
	if payload == nil {
		payload = map[string]any{}
	}
	observation := toolbinding.Observed(ctx)
	observation.TaskEvent = taskToolMeta(snapshot)
	var result tool.Result
	failed := codeModeTaskFailed(snapshot)
	if snapshot.Kind == taskapi.KindCodeMode {
		if selected, legacy, ok := takeCodeModeTaskResult(payload); ok {
			failed = failed || legacy.Failed
			observeScriptTask(ctx, snapshot, legacy)
			addCodeModeTaskResult(&result, payload, selected)
		}
	}
	return withTaskPayload(result, payload, failed)
}

func codeModeTaskFailed(snapshot taskapi.Snapshot) bool {
	return snapshot.Kind == taskapi.KindCodeMode && (snapshot.State == taskapi.StateUnknownOutcome || snapshot.State == taskapi.StateFailed || snapshot.State == taskapi.StateInterrupted || snapshot.State == taskapi.StateCancelled)
}

// Read pre-content-only script dispositions until their Task snapshots expire.
type legacyScriptDisposition struct {
	Failed bool `json:"is_error"`

	Metadata struct {
		Completion string `json:"code_mode_script_completion"`
	} `json:"metadata"`
}

func observeScriptTask(ctx context.Context, snapshot taskapi.Snapshot, old legacyScriptDisposition) {
	o := toolbinding.Observed(ctx)

	completion, _ := snapshot.Metadata[codemode.MetadataScriptCompletion].(string)
	if completion == "" {
		completion = old.Metadata.Completion
	}
	if completion != "" {
		o.ScriptCompletion = completion
	}
}
func takeCodeModeTaskResult(payload map[string]any) (tool.Result, legacyScriptDisposition, bool) {
	var result tool.Result
	raw := taskStringValue(payload["tool_result_json"])
	delete(payload, "tool_result_json")
	if raw == "" || json.Unmarshal([]byte(raw), &result) != nil {
		return tool.Result{}, legacyScriptDisposition{}, false
	}
	// Current snapshots record failure in Task state. Read the historical
	// field for older snapshots, which could carry their own failed reply.
	var legacy legacyScriptDisposition
	_ = json.Unmarshal([]byte(raw), &legacy)
	return result, legacy, true
}

func addCodeModeTaskResult(result *tool.Result, payload map[string]any, selected tool.Result) {
	var historical []json.RawMessage
	for _, block := range selected.Content {
		if structured, ok := block.(tool.StructuredContent); ok {
			payload["result"] = structured.Value
			continue
		}
		// Pre-block Task snapshots stored unmarked JSON parts. Keep their order
		// and raw tokens inside the snapshot's current structured payload.
		if block.ContentType() == tool.ContentType(model.PartKindJSON) {
			for _, part := range tool.ModelParts([]tool.ContentBlock{block}) {
				if raw := part.JSONValue(); json.Valid(raw) {
					historical = append(historical, raw)
				}
			}
			continue
		}
		result.Content = append(result.Content, block)
	}
	if len(historical) == 1 && payload["result"] == nil {
		payload["result"] = historical[0]
	} else if len(historical) > 0 {
		payload["result_parts"] = historical
	}
}

func withTaskPayload(result tool.Result, payload map[string]any, failed bool) (tool.Result, error) {
	raw, _ := json.Marshal(payload)
	result.Content = append([]tool.ContentBlock{tool.StructuredContent{Value: raw}}, result.Content...)
	if failed {
		return result, tool.FailedReply("one or more task results failed")
	}
	return result, nil
}

func taskBatchControlPayload(items []taskBatchControlItem, action string, actualWaitMS int) map[string]any {
	tasks := make([]map[string]any, 0, len(items))
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

func taskBatchErrorCount(items []taskBatchControlItem) int {
	count := 0
	for _, item := range items {
		if item.Err != nil || codeModeTaskFailed(item.Snapshot) {
			count++
		}
	}
	return count
}

func taskCancelToolPayload(snapshot taskapi.Snapshot) map[string]any {
	return map[string]any{
		"handle": taskPublicHandle(snapshot),
		"state":  string(snapshot.State),
	}
}

func taskToolMeta(snapshot taskapi.Snapshot) map[string]any {
	meta := map[string]any{}
	if snapshot.Kind == taskapi.KindCodeMode {
		if source, ok := snapshot.Metadata["code_mode_source"]; ok {
			meta["code_mode_source"] = source
		}
	}
	taskMeta := toolbinding.RuntimeSection(meta, "task")
	taskMeta["kind"] = strings.TrimSpace(string(snapshot.Kind))
	taskMeta["state"] = strings.TrimSpace(string(snapshot.State))
	taskMeta["running"] = snapshot.Running
	taskMeta["task_id"] = strings.TrimSpace(snapshot.Ref.TaskID)
	taskMeta["handle"] = taskPublicHandle(snapshot)
	if snapshot.Kind == taskapi.KindCodeMode {
		if cursor, ok := taskInt64Value(snapshot.Metadata["model_output_cursor"]); ok {
			taskMeta["model_output_cursor"] = cursor
		}
	}
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
	for _, key := range []string{"source", "participant_role", "agent", "agent_id", "mention", "prompt", "turn_id", "turn_seq", "parent_call", "parent_tool"} {
		if value, ok := snapshot.Metadata[key]; ok {
			taskMeta[key] = value
		}
	}
	return meta
}

func taskToolPayload(snapshot taskapi.Snapshot) map[string]any {
	var payload map[string]any
	switch snapshot.Kind {
	case taskapi.KindCodeMode:
		payload = session.CloneState(snapshot.Result)
		if payload == nil {
			payload = map[string]any{}
		}
		payload["handle"] = snapshot.Handle
		if cursor, ok := taskInt64Value(snapshot.Metadata["model_output_cursor"]); ok {
			payload["model_output_cursor"] = cursor
		}
		payload["state"] = string(snapshot.State)
		payload["running"] = snapshot.Running
	case taskapi.KindSubagent:
		payload = subagentTaskToolPayload(snapshot)
	default:
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
	if code, _ := snapshot.Result["error_code"].(string); code != "" {
		payload["error_code"] = code
	}
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
	script := tm.codeTasks[entry.TaskID]
	tm.mu.RUnlock()
	if command != nil {
		command.mu.Lock()
		command.revision = entry.Revision
		command.mu.Unlock()
	}
	if script != nil {
		script.mu.Lock()
		if entry.Revision >= script.entry.Revision {
			script.entry = taskapi.CloneEntry(entry)
		}
		script.mu.Unlock()
	}
	if subagent != nil {
		subagent.mu.Lock()
		subagent.revision = entry.Revision
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
