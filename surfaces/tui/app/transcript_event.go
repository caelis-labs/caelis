package tuiapp

import (
	"strconv"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/acpprojector"
)

type TranscriptEventKind string

const (
	TranscriptEventNarrative   TranscriptEventKind = "narrative"
	TranscriptEventNotice      TranscriptEventKind = "notice"
	TranscriptEventPlan        TranscriptEventKind = "plan"
	TranscriptEventTool        TranscriptEventKind = "tool"
	TranscriptEventApproval    TranscriptEventKind = "approval"
	TranscriptEventParticipant TranscriptEventKind = "participant"
	TranscriptEventLifecycle   TranscriptEventKind = "lifecycle"
	TranscriptEventUsage       TranscriptEventKind = "usage"
)

type TranscriptNarrativeKind string

const (
	TranscriptNarrativeUser      TranscriptNarrativeKind = "user"
	TranscriptNarrativeAssistant TranscriptNarrativeKind = "assistant"
	TranscriptNarrativeReasoning TranscriptNarrativeKind = "reasoning"
	TranscriptNarrativeSystem    TranscriptNarrativeKind = "system"
	TranscriptNarrativeNotice    TranscriptNarrativeKind = "notice"
)

type TranscriptEvent struct {
	Kind       TranscriptEventKind
	Scope      ACPProjectionScope
	ScopeID    string
	Actor      string
	OccurredAt time.Time

	NarrativeKind TranscriptNarrativeKind
	Text          string
	Final         bool

	ToolCallID          string
	ToolName            string
	ToolKind            string
	ToolTitle           string
	ToolArgs            string
	ToolFullArgs        string
	ToolOutput          string
	ToolStream          string
	ToolStatus          string
	ToolError           bool
	ToolOutputSynthetic bool
	ToolTaskID          string
	ToolTaskAction      string
	ToolTaskInput       string
	ToolTaskTargetKind  string

	PlanEntries []PlanEntry

	ApprovalTool    string
	ApprovalCommand string
	ApprovalStatus  string
	ApprovalRisk    string
	ApprovalAuth    string
	ApprovalText    string

	State string

	Usage *eventstream.UsageSnapshot

	AnchorToolCallID     string
	AnchorToolName       string
	MirroredToParentTool bool
}

func mergeTranscriptMeta(base map[string]any, overlay map[string]any) map[string]any {
	if len(base) == 0 {
		return cloneAnyMap(overlay)
	}
	out := cloneAnyMap(base)
	for key, value := range overlay {
		if baseMap, ok := out[key].(map[string]any); ok {
			if overlayMap, ok := value.(map[string]any); ok {
				out[key] = mergeTranscriptMeta(baseMap, overlayMap)
				continue
			}
		}
		out[key] = value
	}
	return out
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
	if isErr || strings.EqualFold(strings.TrimSpace(status), "failed") {
		return "failed"
	}
	if transcriptToolStatusFinal(status, isErr) {
		return "completed"
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
	switch strings.ToUpper(strings.TrimSpace(toolSemanticName(toolName, toolKind))) {
	case "READ", "LIST", "GLOB", "SEARCH", "RG", "FIND":
		return true
	default:
		return false
	}
}

func terminalFinalWithoutContent(toolName string, toolKind string, status string, isErr bool) bool {
	if !transcriptToolStatusFinal(status, isErr) {
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
	if terminalContentText(content) != "" {
		return false
	}
	if terminalRawOutputHasText(rawOutput) {
		return false
	}
	if terminalRuntimeOutputText(meta) != "" || terminalOutputMetaText(meta) != "" {
		return false
	}
	return len(content) == 0 || hasStandardTerminalContent(content)
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
	if text := terminalOutputMetaText(meta); text != "" {
		return text
	}
	if text := terminalRuntimeOutputText(meta); text != "" {
		return text
	}
	if text := terminalContentText(content); text != "" {
		return text
	}
	return ""
}

func terminalKindSpecificOutputText(toolName string, toolKind string, rawOutput map[string]any, meta map[string]any, content []acpprojector.ToolContent, status string, isErr bool) string {
	if !isTerminalPanelToolKind(toolName, toolKind) && !strings.EqualFold(strings.TrimSpace(toolName), "TASK") {
		return ""
	}
	if !hasStandardTerminalContent(content) {
		return ""
	}
	name := strings.ToUpper(strings.TrimSpace(toolName))
	if name == "SPAWN" {
		if isErr || strings.EqualFold(strings.TrimSpace(status), "failed") {
			return firstNonEmpty(asString(rawOutput["stderr"]), asString(rawOutput["error"]))
		}
		if transcriptToolStatusFinal(status, isErr) {
			return firstNonEmpty(asString(rawOutput["final_message"]), asString(rawOutput["finalMessage"]), asString(rawOutput["result"]), asString(rawOutput["output"]), asString(rawOutput["text"]))
		}
		return firstNonEmpty(asString(rawOutput["text"]), asString(rawOutput["stdout"]), asString(rawOutput["output_preview"]), asString(rawOutput["stderr"]))
	}
	if terminalTaskStillRunning(rawOutput, meta) {
		return firstNonEmpty(asString(rawOutput["latest_output"]), asString(rawOutput["output_preview"]))
	}
	if !transcriptToolStatusFinal(status, isErr) {
		return firstNonEmpty(asString(rawOutput["latest_output"]), asString(rawOutput["output_preview"]))
	}
	if text := firstNonEmpty(asString(rawOutput["result"]), asString(rawOutput["output"]), asString(rawOutput["stdout"]), asString(rawOutput["stderr"]), asString(rawOutput["error"])); text != "" {
		return text
	}
	return ""
}

func taskControlResult(semanticName string, rawInput map[string]any, displayOutput map[string]any, meta map[string]any) bool {
	if !strings.EqualFold(strings.TrimSpace(semanticName), "TASK") {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(toolDisplayTaskAction(rawInput, displayOutput, meta))) {
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

func hasStandardTerminalContent(content []acpprojector.ToolContent) bool {
	for _, item := range content {
		if strings.EqualFold(strings.TrimSpace(item.Type), "terminal") && strings.TrimSpace(item.TerminalID) != "" {
			return true
		}
	}
	return false
}

func terminalContentText(content []acpprojector.ToolContent) string {
	var out strings.Builder
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if text := protocolTextContent(item.Content); text != "" {
			appendTerminalContentText(&out, text)
		}
	}
	return out.String()
}

func appendTerminalContentText(out *strings.Builder, text string) {
	if out == nil || text == "" {
		return
	}
	if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") && !strings.HasPrefix(text, "\n") {
		out.WriteByte('\n')
	}
	out.WriteString(text)
}

func terminalOutputMetaText(meta map[string]any) string {
	output := terminalMetaSection(meta, "terminal_output")
	return asString(output["data"])
}

func terminalRuntimeOutputText(meta map[string]any) string {
	taskMeta := eventRuntimeTaskMeta(meta)
	for _, key := range []string{"output_text", "latest_output", "output_preview", "result", "output", "stdout", "stderr", "error", "final_message", "finalMessage", "text"} {
		if text := asString(taskMeta[key]); text != "" {
			return text
		}
	}
	return ""
}

func terminalInfoToolName(meta map[string]any) string {
	info := terminalMetaSection(meta, "terminal_info")
	return firstNonEmpty(asString(info["tool"]), asString(info["tool_name"]), asString(info["name"]))
}

func terminalMetaSection(meta map[string]any, key string) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	section, _ := meta[key].(map[string]any)
	return section
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
