package kernel

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

type replayTaskPanelTarget struct {
	index          int
	callID         string
	toolName       string
	taskKind       taskapi.Kind
	taskID         string
	internalTaskID string
	handle         string
	terminalID     string
	sessionID      string
	hasFinalOutput bool
	env            eventstream.Envelope
}

type replayTaskPanelInsert struct {
	index int
	env   eventstream.Envelope
}

type replayTaskPanelFinal struct {
	kind       taskapi.Kind
	status     string
	text       string
	rawOutput  map[string]any
	taskMeta   map[string]any
	keys       map[string]bool
	visibleID  string
	terminalID string
	updatedAt  time.Time
}

func (g *Gateway) augmentReplayTaskPanelEvents(ctx context.Context, ref session.SessionRef, events []eventstream.Envelope, targetHistory []eventstream.Envelope) []eventstream.Envelope {
	if g == nil || g.tasks == nil || (len(events) == 0 && len(targetHistory) == 0) {
		return events
	}
	targets := replayTaskPanelTargets(events)
	historyTargets := replayTaskPanelTargets(targetHistory)
	if len(targets) == 0 && len(historyTargets) == 0 {
		return events
	}
	entries, err := g.tasks.ListSession(ctx, ref)
	if err != nil || len(entries) == 0 {
		return events
	}
	inserts := map[int][]eventstream.Envelope{}
	prepends := []replayTaskPanelInsert{}
	restoredCallIDs := map[string]bool{}
	for _, entry := range entries {
		finalPanel, ok := replayTaskPanelFinalFromEntry(entry)
		if !ok {
			continue
		}
		restoredInWindow := false
		for _, target := range replayTaskPanelTargetsForFinal(targets, finalPanel) {
			if target.hasFinalOutput || restoredCallIDs[target.callID] {
				continue
			}
			restoredCallIDs[target.callID] = true
			restoredInWindow = true
			inserts[target.index] = append(inserts[target.index], replayTaskPanelEnvelope(target, finalPanel))
		}
		if restoredInWindow {
			continue
		}
		for _, target := range replayTaskPanelTargetsForFinal(historyTargets, finalPanel) {
			if target.hasFinalOutput || restoredCallIDs[target.callID] {
				continue
			}
			restoredCallIDs[target.callID] = true
			prepends = append(prepends, replayTaskPanelInsert{
				index: target.index,
				env:   replayTaskPanelEnvelope(target, finalPanel),
			})
		}
	}
	if len(inserts) == 0 && len(prepends) == 0 {
		return events
	}
	sort.SliceStable(prepends, func(i, j int) bool {
		return prepends[i].index < prepends[j].index
	})
	out := make([]eventstream.Envelope, 0, len(events)+len(inserts)+len(prepends))
	for _, insert := range prepends {
		out = append(out, insert.env)
	}
	for i, env := range events {
		out = append(out, env)
		out = append(out, inserts[i]...)
	}
	return out
}

func replayTaskPanelTargets(events []eventstream.Envelope) map[string]replayTaskPanelTarget {
	out := map[string]replayTaskPanelTarget{}
	for i, env := range events {
		target, ok := replayTaskPanelTargetFromEnvelope(i, env)
		if !ok {
			continue
		}
		if existing, exists := out[target.callID]; exists {
			target.hasFinalOutput = target.hasFinalOutput || existing.hasFinalOutput
		}
		out[target.callID] = target
	}
	for callID, target := range out {
		if strings.TrimSpace(target.taskID) == "" &&
			strings.TrimSpace(target.internalTaskID) == "" &&
			strings.TrimSpace(target.handle) == "" &&
			strings.TrimSpace(target.terminalID) == "" &&
			strings.TrimSpace(target.sessionID) == "" {
			delete(out, callID)
		}
	}
	return out
}

func replayTaskPanelTargetFromEnvelope(index int, env eventstream.Envelope) (replayTaskPanelTarget, bool) {
	if env.Kind != eventstream.KindSessionUpdate || env.Scope == eventstream.ScopeParticipant || env.Scope == eventstream.ScopeSubagent {
		return replayTaskPanelTarget{}, false
	}
	var (
		callID    string
		toolName  string
		status    string
		rawOutput map[string]any
		meta      map[string]any
	)
	if update, ok := eventstream.ToolCallFromEnvelope(env); ok {
		callID = strings.TrimSpace(update.ToolCallID)
		toolName = replayRuntimeToolName(eventstream.UpdateMeta(update), update.Title)
		status = strings.TrimSpace(update.Status)
		rawOutput = schema.NormalizeRawMap(update.RawOutput)
		meta = metautil.Merge(eventstream.UpdateMeta(update), env.Meta)
	} else if update, ok := eventstream.ToolCallUpdateFromEnvelope(env); ok {
		callID = strings.TrimSpace(update.ToolCallID)
		toolName = replayRuntimeToolName(eventstream.UpdateMeta(update), replayStringValue(update.Title))
		status = replayStringValue(update.Status)
		rawOutput = schema.NormalizeRawMap(update.RawOutput)
		meta = metautil.Merge(eventstream.UpdateMeta(update), env.Meta)
	} else {
		return replayTaskPanelTarget{}, false
	}
	if callID == "" {
		return replayTaskPanelTarget{}, false
	}
	taskMeta := replayRuntimeSection(meta, EventMetaRuntimeTask)
	toolMeta := replayRuntimeSection(meta, EventMetaRuntimeTool)
	taskKind := replayTargetTaskKind(taskMeta, toolMeta, rawOutput)
	if !replayRestorableTaskKind(taskKind) {
		return replayTaskPanelTarget{}, false
	}
	target := replayTaskPanelTarget{
		index:          index,
		callID:         callID,
		toolName:       toolName,
		taskKind:       taskKind,
		taskID:         firstNonEmpty(replayAnyString(taskMeta[EventMetaRuntimeTaskID]), replayAnyString(rawOutput[EventMetaRuntimeTaskID])),
		internalTaskID: firstNonEmpty(replayAnyString(taskMeta[EventMetaRuntimeTaskInternalID]), replayAnyString(rawOutput[EventMetaRuntimeTaskInternalID])),
		handle:         firstNonEmpty(replayAnyString(taskMeta[EventMetaRuntimeTaskHandle]), replayAnyString(rawOutput[EventMetaRuntimeTaskHandle]), replayAnyString(rawOutput["mention"])),
		terminalID:     firstNonEmpty(replayAnyString(taskMeta[EventMetaRuntimeTaskTerminalID]), replayAnyString(rawOutput[EventMetaRuntimeTaskTerminalID])),
		sessionID:      firstNonEmpty(replayAnyString(taskMeta[EventMetaRuntimeTaskSessionID]), replayAnyString(rawOutput[EventMetaRuntimeTaskSessionID])),
		hasFinalOutput: replayPanelHasFinalOutput(status, taskMeta, rawOutput),
		env:            env,
	}
	if target.toolName == "" {
		target.toolName = replayAnyString(toolMeta["name"])
	}
	return target, true
}

func replayTargetTaskKind(taskMeta map[string]any, toolMeta map[string]any, rawOutput map[string]any) taskapi.Kind {
	for _, value := range []string{
		replayAnyString(taskMeta[EventMetaRuntimeTaskKind]),
		replayAnyString(toolMeta[EventMetaRuntimeTargetKind]),
		replayAnyString(rawOutput["task_kind"]),
		replayAnyString(rawOutput[EventMetaRuntimeTaskKind]),
	} {
		kind := taskapi.Kind(strings.ToLower(strings.TrimSpace(value)))
		if replayRestorableTaskKind(kind) {
			return kind
		}
	}
	return ""
}

func replayRestorableTaskKind(kind taskapi.Kind) bool {
	switch kind {
	case taskapi.KindCommand, taskapi.KindSubagent:
		return true
	default:
		return false
	}
}

func replayPanelHasFinalOutput(status string, taskMeta map[string]any, rawOutput map[string]any) bool {
	state := firstNonEmpty(status, replayAnyString(taskMeta[EventMetaRuntimeTaskState]), replayAnyString(rawOutput[EventMetaRuntimeTaskState]))
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "completed", "failed", "interrupted", "cancelled", "canceled":
	default:
		return false
	}
	for _, key := range []string{EventMetaRuntimeTaskFinal, "finalMessage", EventMetaRuntimeTaskResult, "output", "text", EventMetaRuntimeTaskError} {
		if strings.TrimSpace(replayAnyString(rawOutput[key])) != "" || strings.TrimSpace(replayAnyString(taskMeta[key])) != "" {
			return true
		}
	}
	return false
}

func replayTaskPanelTargetsForFinal(targets map[string]replayTaskPanelTarget, final replayTaskPanelFinal) []replayTaskPanelTarget {
	if len(targets) == 0 || final.kind == "" {
		return nil
	}
	out := make([]replayTaskPanelTarget, 0, len(targets))
	for _, target := range targets {
		if target.taskKind != "" && target.taskKind != final.kind {
			continue
		}
		matched := false
		for _, value := range []string{target.taskID, target.internalTaskID, target.handle, target.terminalID, target.sessionID} {
			if key := replayTaskPanelKey(value); key != "" && final.keys[key] {
				matched = true
				break
			}
		}
		if matched {
			out = append(out, target)
		}
	}
	return out
}

func replayTaskEntryKeys(entry *taskapi.Entry) map[string]bool {
	keys := map[string]bool{}
	if entry == nil {
		return keys
	}
	for _, value := range []string{
		entry.TaskID,
		replayAnyString(entry.Spec[EventMetaRuntimeTaskID]),
		replayAnyString(entry.Spec[EventMetaRuntimeTaskHandle]),
		replayAnyString(entry.Spec["command"]),
		replayAnyString(entry.Spec[EventMetaRuntimeTaskTerminalID]),
		replayAnyString(entry.Spec[EventMetaRuntimeTaskSessionID]),
		entry.Terminal.TerminalID,
		entry.Terminal.SessionID,
		replayAnyString(entry.Metadata[EventMetaRuntimeTaskHandle]),
		replayAnyString(entry.Metadata["mention"]),
		replayAnyString(entry.Metadata[EventMetaRuntimeTaskID]),
		replayAnyString(entry.Metadata[EventMetaRuntimeTaskInternalID]),
		replayAnyString(entry.Metadata[EventMetaRuntimeTaskTerminalID]),
		replayAnyString(entry.Metadata[EventMetaRuntimeTaskSessionID]),
		replayAnyString(entry.Result[EventMetaRuntimeTaskHandle]),
		replayAnyString(entry.Result["mention"]),
		replayAnyString(entry.Result[EventMetaRuntimeTaskID]),
		replayAnyString(entry.Result[EventMetaRuntimeTaskTerminalID]),
		replayAnyString(entry.Result[EventMetaRuntimeTaskSessionID]),
		replayAnyString(entry.Result["command"]),
	} {
		if key := replayTaskPanelKey(value); key != "" {
			keys[key] = true
		}
	}
	return keys
}

func replayTaskPanelKey(value string) string {
	value = strings.TrimPrefix(strings.TrimSpace(value), "@")
	if value == "" {
		return ""
	}
	return strings.ToLower(value)
}

func replayTaskPanelEnvelope(target replayTaskPanelTarget, final replayTaskPanelFinal) eventstream.Envelope {
	terminalID := firstNonEmpty(target.callID, target.terminalID, final.terminalID)
	meta := replayTaskPanelMeta(target, final, terminalID)
	update := schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    target.callID,
		Status:        replayStringPtr(final.status),
		RawOutput:     metautil.CloneMap(final.rawOutput),
		Meta:          meta,
	}
	if terminalID != "" {
		update.Content = []schema.ToolCallContent{{Type: "terminal", TerminalID: terminalID}}
	}
	env := target.env
	env.EventID = ""
	env.Final = true
	env.OccurredAt = final.updatedAt
	env.Update = update
	env.Meta = nil
	env.Cursor = ""
	env.ProjectionID = ""
	return env
}

func replayTaskPanelFinalFromEntry(entry *taskapi.Entry) (replayTaskPanelFinal, bool) {
	if entry == nil || !replayRestorableTaskKind(entry.Kind) || entry.Running {
		return replayTaskPanelFinal{}, false
	}
	status := replayTaskStatus(entry)
	finalText := replayTaskFinalText(entry)
	if strings.TrimSpace(finalText) == "" {
		return replayTaskPanelFinal{}, false
	}
	visibleID := replayTaskVisibleID(entry)
	return replayTaskPanelFinal{
		kind:       entry.Kind,
		status:     status,
		text:       finalText,
		rawOutput:  replayTaskRawOutput(entry, finalText, status, visibleID),
		taskMeta:   replayTaskMeta(entry, finalText, status, visibleID),
		keys:       replayTaskEntryKeys(entry),
		visibleID:  visibleID,
		terminalID: replayTaskValue(entry, EventMetaRuntimeTaskTerminalID),
		updatedAt:  replayTaskUpdatedAt(entry),
	}, true
}

func replayTaskStatus(entry *taskapi.Entry) string {
	if entry == nil {
		return schema.ToolStatusCompleted
	}
	switch entry.State {
	case taskapi.StateFailed:
		return schema.ToolStatusFailed
	case taskapi.StateCancelled:
		return "cancelled"
	case taskapi.StateInterrupted, taskapi.StateTerminated:
		return "interrupted"
	default:
		return schema.ToolStatusCompleted
	}
}

func replayTaskRawOutput(entry *taskapi.Entry, finalText string, status string, visibleID string) map[string]any {
	out := map[string]any{
		EventMetaRuntimeTaskState:   status,
		EventMetaRuntimeTaskRunning: false,
	}
	if visibleID != "" {
		out[EventMetaRuntimeTaskID] = visibleID
	}
	if entry != nil && entry.Kind == taskapi.KindCommand {
		replayCommandTaskRawOutput(out, entry, finalText, status)
		return out
	}
	replaySubagentTaskRawOutput(out, entry, finalText, status)
	return out
}

func replayTaskMeta(entry *taskapi.Entry, finalText string, status string, visibleID string) map[string]any {
	taskKind := taskapi.Kind("")
	if entry != nil {
		taskKind = entry.Kind
	}
	taskMeta := map[string]any{
		EventMetaRuntimeTaskKind:    string(taskKind),
		EventMetaRuntimeTaskState:   status,
		EventMetaRuntimeTaskRunning: false,
	}
	if visibleID != "" {
		taskMeta[EventMetaRuntimeTaskID] = visibleID
	}
	if internalID := replayTaskInternalID(entry); internalID != "" && internalID != visibleID {
		taskMeta[EventMetaRuntimeTaskInternalID] = internalID
	}
	for _, key := range []string{"agent", "agent_id", EventMetaRuntimeTaskHandle, "mention", "prompt", "command", "workdir", EventMetaRuntimeTaskSessionID, EventMetaRuntimeTaskTerminalID, "turn_id", "turn_seq"} {
		if value := replayTaskValue(entry, key); value != "" {
			taskMeta[key] = value
		}
	}
	if entry != nil && entry.Kind == taskapi.KindCommand {
		if finalText != "" {
			taskMeta[EventMetaRuntimeTaskResult] = finalText
		}
		if exitCode, ok := entry.Result["exit_code"]; ok {
			taskMeta["exit_code"] = exitCode
		}
		if errText := replayAnyString(entry.Result[EventMetaRuntimeTaskError]); errText != "" {
			taskMeta[EventMetaRuntimeTaskError] = errText
		}
		return taskMeta
	}
	if status == schema.ToolStatusFailed {
		taskMeta[EventMetaRuntimeTaskError] = finalText
	} else {
		taskMeta[EventMetaRuntimeTaskFinal] = finalText
		taskMeta[EventMetaRuntimeTaskResult] = finalText
	}
	return taskMeta
}

func replayCommandTaskRawOutput(out map[string]any, entry *taskapi.Entry, finalText string, status string) {
	if out == nil || entry == nil {
		return
	}
	if finalText != "" {
		out[EventMetaRuntimeTaskResult] = finalText
	}
	if errText := replayAnyString(entry.Result[EventMetaRuntimeTaskError]); errText != "" {
		out[EventMetaRuntimeTaskError] = errText
	} else if status == schema.ToolStatusFailed && finalText != "" {
		out[EventMetaRuntimeTaskError] = finalText
	}
	for _, key := range []string{"exit_code", "hint_code", "hint", "hint_severity", "error_code"} {
		if value, ok := entry.Result[key]; ok {
			out[key] = value
		}
	}
	for _, key := range []string{"command", "workdir", EventMetaRuntimeTaskSessionID, EventMetaRuntimeTaskTerminalID} {
		if value := replayTaskValue(entry, key); value != "" {
			out[key] = value
		}
	}
}

func replaySubagentTaskRawOutput(out map[string]any, entry *taskapi.Entry, finalText string, status string) {
	if out == nil {
		return
	}
	if finalText != "" {
		if status == schema.ToolStatusFailed {
			out[EventMetaRuntimeTaskError] = finalText
		} else {
			out[EventMetaRuntimeTaskFinal] = finalText
			out[EventMetaRuntimeTaskResult] = finalText
		}
	}
	for _, key := range []string{"agent", "agent_id", EventMetaRuntimeTaskHandle, "mention", EventMetaRuntimeTaskSessionID, EventMetaRuntimeTaskTerminalID} {
		if value := replayTaskValue(entry, key); value != "" {
			out[key] = value
		}
	}
}

func replayTaskPanelMeta(target replayTaskPanelTarget, final replayTaskPanelFinal, terminalID string) map[string]any {
	toolMeta := map[string]any{
		EventMetaRuntimeTargetKind: string(final.kind),
	}
	if toolName := strings.TrimSpace(target.toolName); toolName != "" {
		toolMeta[EventMetaRuntimeToolName] = toolName
	}
	if final.visibleID != "" {
		toolMeta[EventMetaRuntimeTargetID] = final.visibleID
	}
	meta := metautil.WithCompactRuntimeSection(nil, EventMetaRuntimeTool, toolMeta)
	meta = metautil.WithCompactRuntimeSection(meta, EventMetaRuntimeTask, final.taskMeta)
	if terminalID != "" {
		meta = metautil.WithTerminalInfo(meta, terminalID)
		meta = metautil.WithTerminalOutput(meta, terminalID, final.text)
	}
	return meta
}

func replayTaskFinalText(entry *taskapi.Entry) string {
	if entry == nil {
		return ""
	}
	switch entry.Kind {
	case taskapi.KindCommand:
		return display.CommandTaskFinalText(string(entry.State), entry.Result)
	case taskapi.KindSubagent:
		return display.SubagentTaskFinalText(string(entry.State), entry.Result)
	default:
		return ""
	}
}

func replayTaskVisibleID(entry *taskapi.Entry) string {
	if entry == nil {
		return ""
	}
	if entry.Kind == taskapi.KindCommand {
		return firstNonEmpty(
			replayAnyString(entry.Result[EventMetaRuntimeTaskID]),
			replayAnyString(entry.Metadata[EventMetaRuntimeTaskID]),
			replayAnyString(entry.Spec[EventMetaRuntimeTaskID]),
			strings.TrimSpace(entry.TaskID),
		)
	}
	return firstNonEmpty(
		replayAnyString(entry.Result[EventMetaRuntimeTaskID]),
		replayAnyString(entry.Metadata[EventMetaRuntimeTaskID]),
		replayAnyString(entry.Result[EventMetaRuntimeTaskHandle]),
		replayAnyString(entry.Metadata[EventMetaRuntimeTaskHandle]),
		replayAnyString(entry.Spec[EventMetaRuntimeTaskHandle]),
		strings.TrimSpace(entry.TaskID),
	)
}

func replayTaskInternalID(entry *taskapi.Entry) string {
	if entry == nil {
		return ""
	}
	return strings.TrimSpace(entry.TaskID)
}

func replayTaskValue(entry *taskapi.Entry, key string) string {
	if entry == nil {
		return ""
	}
	if key == EventMetaRuntimeTaskTerminalID {
		if value := firstNonEmpty(entry.Terminal.TerminalID, replayAnyString(entry.Result[key]), replayAnyString(entry.Metadata[key]), replayAnyString(entry.Spec[key])); value != "" {
			return value
		}
	}
	if key == EventMetaRuntimeTaskSessionID {
		if value := firstNonEmpty(entry.Terminal.SessionID, replayAnyString(entry.Result[key]), replayAnyString(entry.Metadata[key]), replayAnyString(entry.Spec[key])); value != "" {
			return value
		}
	}
	return firstNonEmpty(
		replayAnyString(entry.Result[key]),
		replayAnyString(entry.Metadata[key]),
		replayAnyString(entry.Spec[key]),
	)
}

func replayTaskUpdatedAt(entry *taskapi.Entry) time.Time {
	if entry == nil {
		return time.Time{}
	}
	if !entry.UpdatedAt.IsZero() {
		return entry.UpdatedAt
	}
	return entry.CreatedAt
}

func replayRuntimeToolName(meta map[string]any, title string) string {
	if name := replayAnyString(replayRuntimeSection(meta, EventMetaRuntimeTool)[EventMetaRuntimeToolName]); name != "" {
		return name
	}
	if name := replayToolNameFromTitle(title); name != "" {
		return name
	}
	return ""
}

func replayToolNameFromTitle(title string) string {
	fields := strings.Fields(strings.TrimSpace(title))
	if len(fields) == 0 {
		return ""
	}
	candidate := strings.Trim(fields[0], "`:()[]{}")
	if candidate == "" || candidate != strings.ToUpper(candidate) {
		return ""
	}
	hasLetter := false
	for _, r := range candidate {
		if r >= 'A' && r <= 'Z' {
			hasLetter = true
			continue
		}
		if (r >= '0' && r <= '9') || r == '_' || r == '-' {
			continue
		}
		return ""
	}
	if !hasLetter {
		return ""
	}
	return candidate
}

func replayRuntimeSection(meta map[string]any, section string) map[string]any {
	caelis, _ := meta[EventMetaRoot].(map[string]any)
	runtimeMeta, _ := caelis[EventMetaRuntime].(map[string]any)
	values, _ := runtimeMeta[strings.TrimSpace(section)].(map[string]any)
	return values
}

func replayAnyString(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	default:
		return ""
	}
}

func replayStringPtr(value string) *string {
	value = strings.TrimSpace(value)
	return &value
}

func replayStringValue(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}
