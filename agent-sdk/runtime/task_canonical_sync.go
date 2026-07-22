package runtime

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

func (tm *taskRuntime) syncCanonicalToolResult(ctx context.Context, ref session.SessionRef, event *session.Event) error {
	if tm == nil || tm.store == nil || event == nil || session.EventTypeOf(event) != session.EventTypeToolResult || event.Tool == nil {
		return nil
	}
	output := session.CloneState(event.Tool.Output)
	if len(output) == 0 {
		return nil
	}
	toolName := names.ExecutableOrSelf(event.Tool.Name)
	if toolName != names.RunCommand && toolName != names.Task && toolName != names.Spawn {
		return nil
	}
	if tasks, ok := canonicalTaskBatchOutputs(output["tasks"]); ok {
		var firstErr error
		for _, item := range tasks {
			if !canonicalTaskBatchOutputSyncable(item) {
				continue
			}
			if err := tm.syncCanonicalToolOutput(ctx, ref, toolName, "", item, event); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return firstErr
	}
	return tm.syncCanonicalToolOutput(ctx, ref, toolName, "", output, event)
}

func canonicalTaskBatchOutputs(value any) ([]map[string]any, bool) {
	switch items := value.(type) {
	case nil:
		return nil, false
	case []any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			itemMap, ok := item.(map[string]any)
			if !ok {
				continue
			}
			out = append(out, session.CloneState(itemMap))
		}
		return out, true
	case []map[string]any:
		out := make([]map[string]any, 0, len(items))
		for _, item := range items {
			out = append(out, session.CloneState(item))
		}
		return out, true
	default:
		return nil, false
	}
}

func canonicalTaskBatchOutputSyncable(output map[string]any) bool {
	if len(output) == 0 || strings.TrimSpace(firstNonEmpty(taskStringValue(output["handle"]), taskStringValue(output["task_id"]))) == "" {
		return false
	}
	if _, hasError := output["error"]; hasError && strings.TrimSpace(taskStringValue(output["state"])) == "" {
		return false
	}
	return true
}

func (tm *taskRuntime) syncCanonicalToolOutput(ctx context.Context, ref session.SessionRef, toolName string, targetKind string, output map[string]any, event *session.Event) error {
	taskID := firstNonEmpty(
		taskRuntimeMetaString(event.Meta, "task", "task_id"),
	)
	// Pre-TaskHandle canonical history exposed the internal TaskID in task_id.
	// Keep that history replayable without treating opaque IDs as public
	// handles. New results carry handle and keep TaskID in runtime metadata.
	if taskID == "" {
		legacyTaskID := strings.TrimSpace(taskStringValue(output["task_id"]))
		if legacyTaskID != "" {
			if entry, err := tm.store.Get(ctx, legacyTaskID); err == nil && entry != nil && strings.TrimSpace(entry.Session.SessionID) == strings.TrimSpace(ref.SessionID) {
				taskID = legacyTaskID
			}
		}
	}
	if taskID == "" {
		handle := taskStringValue(output["handle"])
		if handle != "" {
			identity, err := tm.resolveTaskHandle(ctx, ref, handle)
			if err != nil {
				return err
			}
			taskID = identity.taskID
		}
	}
	if taskID == "" {
		return nil
	}
	metaKind := strings.ToLower(firstNonEmpty(
		taskRuntimeMetaString(event.Meta, "task", "kind"),
		taskRuntimeMetaString(event.Meta, "task", "task_kind"),
		taskRuntimeMetaString(event.Meta, "tool", "target_kind"),
	))
	targetKind = firstNonEmpty(strings.ToLower(strings.TrimSpace(targetKind)), metaKind)
	switch {
	case toolName == names.RunCommand || targetKind == string(taskapi.KindCommand):
		_, err := tm.syncCanonicalTaskEntry(ctx, ref, taskID, taskapi.KindCommand, output, event)
		return err
	case toolName == names.Spawn || targetKind == string(taskapi.KindSubagent):
		_, err := tm.syncCanonicalTaskEntry(ctx, ref, taskID, taskapi.KindSubagent, output, event)
		return err
	case toolName == names.Task:
		if synced, err := tm.syncCanonicalTaskEntry(ctx, ref, taskID, taskapi.KindCommand, output, event); err != nil || synced {
			return err
		}
		_, err := tm.syncCanonicalTaskEntry(ctx, ref, taskID, taskapi.KindSubagent, output, event)
		return err
	default:
		return nil
	}
}

func (tm *taskRuntime) syncCanonicalTaskEntry(ctx context.Context, ref session.SessionRef, taskID string, kind taskapi.Kind, output map[string]any, event *session.Event) (bool, error) {
	entry, ok := tm.storedTaskEntry(ctx, ref, taskID, kind)
	if !ok {
		return false, nil
	}
	if entry.State == taskapi.StateUnknownOutcome {
		return true, nil
	}
	status := ""
	if event != nil && event.Tool != nil {
		status = event.Tool.Status
	}
	updatedAt := time.Time{}
	if event != nil {
		updatedAt = event.Time
	}
	applyCanonicalTaskEntry(entry, output, status, updatedAt)
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		return false, err
	}
	return true, nil
}

func (tm *taskRuntime) storedTaskEntry(ctx context.Context, ref session.SessionRef, taskID string, kind taskapi.Kind) (*taskapi.Entry, bool) {
	if tm == nil || tm.store == nil {
		return nil, false
	}
	if entry, err := tm.store.Get(ctx, taskID); err == nil && entry != nil && storedTaskEntryMatches(entry, ref, kind) {
		return entry, true
	}
	return nil, false
}

func storedTaskEntryMatches(entry *taskapi.Entry, ref session.SessionRef, kind taskapi.Kind) bool {
	return entry != nil && strings.TrimSpace(entry.Session.SessionID) == strings.TrimSpace(ref.SessionID) && entry.Kind == kind
}

func applyCanonicalTaskEntry(entry *taskapi.Entry, output map[string]any, status string, updatedAt time.Time) {
	if entry == nil {
		return
	}
	entry.Result = session.CloneState(output)
	if entry.Kind == taskapi.KindCommand {
		syncCanonicalCommandTaskMetadata(entry, output)
	}
	if state := taskStateFromCanonicalOutput(output, status, entry.State); state != "" {
		entry.State = state
		entry.Running = taskStateRunning(state)
	}
	if !updatedAt.IsZero() {
		entry.UpdatedAt = updatedAt
	}
}

type canonicalTaskHistoryOutput struct {
	Output    map[string]any
	Status    string
	UpdatedAt time.Time
}

func (tm *taskRuntime) backfillCanonicalTaskEntry(ctx context.Context, ref session.SessionRef, entry *taskapi.Entry) (*taskapi.Entry, error) {
	entry = taskapi.CloneEntry(entry)
	if entry == nil || tm == nil || tm.runtime == nil || tm.runtime.sessions == nil || tm.store == nil {
		return entry, nil
	}
	// Unknown outcome is a durable safety state produced after a later effect
	// claim or failed reconciliation. Historical model-visible tool output has
	// no operation revision and therefore cannot safely prove that state stale.
	if entry.State == taskapi.StateUnknownOutcome {
		return entry, nil
	}
	events, err := tm.runtime.sessions.Events(ctx, session.EventsRequest{SessionRef: ref})
	if err != nil {
		return entry, nil
	}
	var (
		found  bool
		latest canonicalTaskHistoryOutput
	)
	for _, event := range events {
		for _, candidate := range canonicalTaskHistoryOutputs(event) {
			if !canonicalTaskOutputMatchesEntry(entry, candidate.Output) {
				continue
			}
			latest = candidate
			found = true
		}
	}
	if !found {
		return entry, nil
	}
	if !entry.UpdatedAt.IsZero() && (latest.UpdatedAt.IsZero() || !latest.UpdatedAt.After(entry.UpdatedAt)) {
		return entry, nil
	}
	applyCanonicalTaskEntry(entry, latest.Output, latest.Status, latest.UpdatedAt)
	if err := tm.persistTaskEntry(ctx, entry); err != nil {
		var conflict *taskapi.RevisionConflictError
		if !errors.As(err, &conflict) {
			return nil, err
		}
		reloaded, loadErr := tm.store.Get(context.WithoutCancel(ctx), entry.TaskID)
		if loadErr != nil || !storedTaskEntryMatches(reloaded, ref, entry.Kind) {
			return nil, errors.Join(err, loadErr)
		}
		return taskapi.CloneEntry(reloaded), nil
	}
	return entry, nil
}

func canonicalTaskHistoryOutputs(event *session.Event) []canonicalTaskHistoryOutput {
	if event == nil || session.EventTypeOf(event) != session.EventTypeToolResult || event.Tool == nil {
		return nil
	}
	toolName := names.ExecutableOrSelf(event.Tool.Name)
	if toolName != names.RunCommand && toolName != names.Task && toolName != names.Spawn {
		return nil
	}
	if tasks, ok := canonicalTaskBatchOutputs(event.Tool.Output["tasks"]); ok {
		out := make([]canonicalTaskHistoryOutput, 0, len(tasks))
		for _, item := range tasks {
			if !canonicalTaskBatchOutputSyncable(item) {
				continue
			}
			out = append(out, canonicalTaskHistoryOutput{
				Output:    item,
				Status:    event.Tool.Status,
				UpdatedAt: event.Time,
			})
		}
		return out
	}
	output := session.CloneState(event.Tool.Output)
	if len(output) == 0 || strings.TrimSpace(firstNonEmpty(taskStringValue(output["handle"]), taskStringValue(output["task_id"]))) == "" {
		return nil
	}
	return []canonicalTaskHistoryOutput{{
		Output:    output,
		Status:    event.Tool.Status,
		UpdatedAt: event.Time,
	}}
}

func canonicalTaskOutputMatchesEntry(entry *taskapi.Entry, output map[string]any) bool {
	if entry == nil || len(output) == 0 {
		return false
	}
	keys := map[string]bool{}
	for _, value := range []string{
		entry.TaskID,
		taskStringValue(entry.Result["task_id"]),
		taskStringValue(entry.Result["handle"]),
		taskStringValue(entry.Result["internal_task_id"]),
		taskStringValue(entry.Metadata["task_id"]),
		taskStringValue(entry.Metadata["handle"]),
		taskStringValue(entry.Metadata["internal_task_id"]),
		taskSpecString(entry.Spec, "task_id"),
		taskSpecString(entry.Spec, "handle"),
		taskSpecString(entry.Spec, "internal_task_id"),
	} {
		addCanonicalTaskMatchKey(keys, entry.Kind, value)
	}
	for _, value := range []string{
		taskStringValue(output["task_id"]),
		taskStringValue(output["handle"]),
		taskStringValue(output["internal_task_id"]),
	} {
		if canonicalTaskMatchKeyExists(keys, entry.Kind, value) {
			return true
		}
	}
	return false
}

func addCanonicalTaskMatchKey(keys map[string]bool, kind taskapi.Kind, value string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return
	}
	keys[value] = true
	if kind == taskapi.KindSubagent {
		if handle := normalizeTaskHandle(value); handle != "" {
			keys[handle] = true
		}
	}
}

func canonicalTaskMatchKeyExists(keys map[string]bool, kind taskapi.Kind, value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	if keys[value] {
		return true
	}
	if kind == taskapi.KindSubagent {
		return keys[normalizeTaskHandle(value)]
	}
	return false
}

func syncCanonicalCommandTaskMetadata(entry *taskapi.Entry, output map[string]any) {
	if entry == nil {
		return
	}
	if entry.Metadata == nil {
		entry.Metadata = map[string]any{}
	}
	if text := taskRawStringValue(output["result"]); text != "" {
		cursor := int64(len([]byte(text)))
		entry.Metadata["output_cursor"] = cursor
		entry.Metadata["model_output_cursor"] = cursor
		return
	}
	delete(entry.Metadata, "output_cursor")
	delete(entry.Metadata, "model_output_cursor")
}

func taskStateFromCanonicalOutput(output map[string]any, status string, fallback taskapi.State) taskapi.State {
	if state := taskapi.State(strings.TrimSpace(taskStringValue(output["state"]))); state != "" {
		return state
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "waiting_input", "waiting_approval":
		return taskapi.StateRunning
	case "failed":
		return taskapi.StateFailed
	case "interrupted":
		return taskapi.StateInterrupted
	case "cancelled", "canceled":
		return taskapi.StateCancelled
	case "completed":
		return taskapi.StateCompleted
	default:
		return fallback
	}
}

func taskStateRunning(state taskapi.State) bool {
	switch state {
	case taskapi.StateRunning, taskapi.StateWaitingInput, taskapi.StateWaitingApproval:
		return true
	default:
		return false
	}
}

func taskRuntimeMetaString(meta map[string]any, section string, key string) string {
	sectionMap := taskRuntimeMetaReadSection(meta, section)
	return taskStringValue(sectionMap[key])
}

func taskRuntimeMetaReadSection(meta map[string]any, section string) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtime, _ := caelis["runtime"].(map[string]any)
	out, _ := runtime[strings.TrimSpace(section)].(map[string]any)
	return out
}
