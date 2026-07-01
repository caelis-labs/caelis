package tuiapp

import (
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/displaypolicy"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
	"github.com/OnslaughtSnail/caelis/surfaces/transcript"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/acpprojector"
)

// Transitional aliases keep the TUI renderer readable during the transcript
// extraction. Shared surface code should use surfaces/transcript directly.
type TranscriptEventKind = transcript.EventKind

const (
	TranscriptEventNarrative   = transcript.EventNarrative
	TranscriptEventNotice      = transcript.EventNotice
	TranscriptEventPlan        = transcript.EventPlan
	TranscriptEventTool        = transcript.EventTool
	TranscriptEventApproval    = transcript.EventApproval
	TranscriptEventParticipant = transcript.EventParticipant
	TranscriptEventLifecycle   = transcript.EventLifecycle
	TranscriptEventUsage       = transcript.EventUsage
)

type TranscriptNarrativeKind = transcript.NarrativeKind

const (
	TranscriptNarrativeUser      = transcript.NarrativeUser
	TranscriptNarrativeAssistant = transcript.NarrativeAssistant
	TranscriptNarrativeReasoning = transcript.NarrativeReasoning
	TranscriptNarrativeSystem    = transcript.NarrativeSystem
	TranscriptNarrativeNotice    = transcript.NarrativeNotice
)

type TranscriptEvent = transcript.Event

func mergeTranscriptMeta(base map[string]any, overlay map[string]any) map[string]any {
	return transcript.MergeMeta(base, overlay)
}

func transcriptToolDisplayName(name string, title string, kind string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	if kind = strings.TrimSpace(kind); kind != "" {
		return kind
	}
	return strings.TrimSpace(title)
}

func transcriptToolStream(status string, isErr bool) string {
	if isErr || strings.EqualFold(strings.TrimSpace(status), "failed") {
		return "stderr"
	}
	return "stdout"
}

func directedParticipantUserDisplay(event TranscriptEvent) string {
	if event.Scope != ACPProjectionParticipant {
		return ""
	}
	handle := firstNonEmpty(
		participantMentionFromHandle(asString(event.Meta["mention"])),
		participantMentionFromHandle(asString(event.Meta["handle"])),
		participantMentionFromHandle(event.Actor),
	)
	if handle == "" {
		return ""
	}
	text := firstNonEmpty(
		strings.TrimSpace(asString(event.Meta["display_input"])),
		strings.TrimSpace(asString(event.Meta["display_title"])),
		strings.TrimSpace(event.Text),
	)
	if text == "" {
		return handle
	}
	return handle + " " + text
}

func directedParticipantUserDequeueText(event TranscriptEvent) string {
	if event.Scope != ACPProjectionParticipant {
		return strings.TrimSpace(event.Text)
	}
	return firstNonEmpty(
		strings.TrimSpace(asString(event.Meta["display_input"])),
		strings.TrimSpace(asString(event.Meta["display_title"])),
		strings.TrimSpace(event.Text),
	)
}

func participantMentionFromHandle(handle string) string {
	handle = strings.TrimSpace(handle)
	if handle == "" {
		return ""
	}
	if strings.HasPrefix(handle, "@") {
		return handle
	}
	return "@" + handle
}

func transcriptToolStatusFinal(status string, isErr bool) bool {
	if isErr {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "interrupted", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func standardToolOutput(status string, isErr bool) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	if isErr || normalized == "failed" {
		return "failed"
	}
	switch normalized {
	case "completed":
		return "completed"
	case "cancelled", "canceled":
		return "cancelled"
	case "interrupted", "terminated":
		return "interrupted"
	}
	return ""
}

func suppressToolResultOutput(toolName string, toolKind string, output string, synthetic bool, isErr bool) bool {
	if isErr {
		return false
	}
	if !isExplorationSummaryTool(toolName, toolKind) {
		return false
	}
	trimmed := strings.TrimSpace(output)
	return synthetic || strings.EqualFold(trimmed, "completed")
}

func isExplorationSummaryTool(toolName string, toolKind string) bool {
	return displaypolicy.IsExplorationTool(toolSemanticName(toolName, toolKind))
}

func terminalFinalWithoutContent(toolName string, toolKind string, status string, isErr bool) bool {
	if !strings.EqualFold(strings.TrimSpace(status), "completed") {
		return false
	}
	if isErr || strings.EqualFold(strings.TrimSpace(status), "failed") {
		return false
	}
	return isTerminalPanelToolKind(toolName, toolKind)
}

func terminalNoOutputPlaceholder(toolName string, toolKind string, rawOutput map[string]any, meta map[string]any, content []acpprojector.ToolContent, status string, isErr bool) bool {
	if !terminalFinalWithoutContent(toolName, toolKind, status, isErr) {
		return false
	}
	if terminalRawOutputHasText(rawOutput) {
		return false
	}
	if terminalRuntimeOutputText(meta) != "" {
		return false
	}
	return hasTerminalPanelMeta(meta)
}

func terminalExitCodeOutputText(toolName string, toolKind string, rawOutput map[string]any, status string, isErr bool) string {
	if !isTerminalPanelToolKind(toolName, toolKind) {
		return ""
	}
	if !isErr && !strings.EqualFold(strings.TrimSpace(status), "failed") {
		return ""
	}
	exitCode := displayInt(rawOutput["exit_code"])
	if exitCode <= 0 {
		return ""
	}
	return "exit " + strconv.Itoa(exitCode)
}

func terminalRawOutputHasText(rawOutput map[string]any) bool {
	for _, key := range []string{"result", "output", "stdout", "stderr", "error", "latest_output", "output_preview", "final_message", "finalMessage", "text"} {
		if text := asString(rawOutput[key]); strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

func terminalToolOutputText(toolName string, toolKind string, rawOutput map[string]any, meta map[string]any, content []acpprojector.ToolContent, status string, isErr bool) string {
	if text := terminalUniversalOutputText(meta, content); text != "" {
		return text
	}
	return terminalKindSpecificOutputText(toolName, toolKind, rawOutput, meta, content, status, isErr)
}

func terminalUniversalOutputText(meta map[string]any, content []acpprojector.ToolContent) string {
	if text := terminalRuntimeOutputText(meta); text != "" {
		return text
	}
	return ""
}

func terminalKindSpecificOutputText(toolName string, toolKind string, rawOutput map[string]any, meta map[string]any, content []acpprojector.ToolContent, status string, isErr bool) string {
	if !isTerminalPanelToolKind(toolName, toolKind) && !strings.EqualFold(strings.TrimSpace(toolName), "TASK") {
		return ""
	}
	if !hasTerminalPanelMeta(meta) {
		return ""
	}
	name := strings.ToUpper(strings.TrimSpace(toolName))
	if name == "SPAWN" {
		if isErr || strings.EqualFold(strings.TrimSpace(status), "failed") {
			return firstNonEmpty(asString(rawOutput["stderr"]), asString(rawOutput["error"]))
		}
		if transcriptToolStatusFinal(status, isErr) {
			return displaypolicy.SubagentTaskOutputText(rawOutput)
		}
		return firstNonEmpty(asString(rawOutput["text"]), asString(rawOutput["stdout"]), asString(rawOutput["output_preview"]), asString(rawOutput["stderr"]))
	}
	if terminalTaskStillRunning(rawOutput, meta) {
		return firstNonEmpty(asString(rawOutput["latest_output"]), asString(rawOutput["output_preview"]))
	}
	if !transcriptToolStatusFinal(status, isErr) {
		return firstNonEmpty(asString(rawOutput["latest_output"]), asString(rawOutput["output_preview"]))
	}
	if text := displaypolicy.CommandTaskOutputText(rawOutput); text != "" {
		return text
	}
	return ""
}

func taskControlResult(semanticName string, rawInput map[string]any, displayOutput map[string]any, meta map[string]any) bool {
	if !strings.EqualFold(strings.TrimSpace(semanticName), "TASK") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(displaypolicy.ToolTaskAction(rawInput, displayOutput, meta))) {
	case "wait", "cancel":
		return true
	default:
		return false
	}
}

func terminalTaskStillRunning(rawOutput map[string]any, meta map[string]any) bool {
	if boolValue(rawOutput["running"]) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(asString(rawOutput["state"])), "running") {
		return true
	}
	taskMeta := eventRuntimeTaskMeta(meta)
	if boolValue(taskMeta["running"]) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(asString(taskMeta["state"])), "running")
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func hasTerminalPanelMeta(meta map[string]any) bool {
	if _, ok := metautil.TerminalInfo(meta); ok {
		return true
	}
	if _, ok := metautil.TerminalOutput(meta); ok {
		return true
	}
	if _, ok := metautil.TerminalExit(meta); ok {
		return true
	}
	return false
}

func terminalRuntimeOutputText(meta map[string]any) string {
	if output, ok := metautil.TerminalOutput(meta); ok {
		return output.Data
	}
	taskMeta := eventRuntimeTaskMeta(meta)
	for _, key := range []string{"output_text", "latest_output", "output_preview", "result", "output", "stdout", "stderr", "error", "final_message", "finalMessage", "text"} {
		if text := asString(taskMeta[key]); text != "" {
			return text
		}
	}
	return ""
}

func toolDisplayMetaOutput(toolName string, meta map[string]any) map[string]any {
	out := map[string]any{}
	toolMeta := eventRuntimeToolMeta(meta)
	taskMeta := eventRuntimeTaskMeta(meta)
	switch strings.ToUpper(strings.TrimSpace(toolName)) {
	case "RUN_COMMAND", "SPAWN", "TASK":
		if taskID := firstNonEmpty(asString(toolMeta["target_id"]), asString(taskMeta["task_id"])); taskID != "" {
			out["task_id"] = taskID
		}
		for _, key := range []string{"yield_time_ms", "effective_yield_time_ms", "yield_time_ms_defaulted"} {
			if value, ok := toolMeta[key]; ok {
				out[key] = value
			}
		}
		if strings.EqualFold(toolName, "RUN_COMMAND") {
			break
		}
		for _, key := range []string{"agent", "agent_id", "handle", "mention", "prompt", "target_kind", "action", "input"} {
			if value, ok := toolMeta[key]; ok {
				out[key] = value
			}
			if value, ok := taskMeta[key]; ok {
				out[key] = value
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func eventRuntimeToolMeta(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	return toolMeta
}

func eventRuntimeTaskMeta(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	taskMeta, _ := runtimeMeta["task"].(map[string]any)
	return taskMeta
}
