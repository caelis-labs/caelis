package transcript

import (
	"strconv"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
)

func TerminalFinalWithoutContent(input ToolOutputFallbackInput) bool {
	if !strings.EqualFold(strings.TrimSpace(input.Status), ToolStatusCompleted) {
		return false
	}
	if input.Error || strings.EqualFold(strings.TrimSpace(input.Status), ToolStatusFailed) {
		return false
	}
	return display.IsTerminalPanelTool(input.ToolName, input.ToolKind)
}

func TerminalNoOutputPlaceholder(input ToolOutputFallbackInput) bool {
	if !TerminalFinalWithoutContent(input) {
		return false
	}
	if TerminalRawOutputHasText(input.RawOutput) {
		return false
	}
	if TerminalRuntimeOutputText(input.Meta) != "" {
		return false
	}
	return HasTerminalPanelMeta(input.Meta)
}

func TerminalExitCodeOutputText(input ToolOutputFallbackInput) string {
	if !display.IsTerminalPanelTool(input.ToolName, input.ToolKind) {
		return ""
	}
	if !input.Error && !strings.EqualFold(strings.TrimSpace(input.Status), ToolStatusFailed) {
		return ""
	}
	exitCode := rawIntOrZero(input.RawOutput["exit_code"])
	if exitCode <= 0 {
		return ""
	}
	return "exit " + strconv.Itoa(exitCode)
}

func TerminalRawOutputHasText(rawOutput map[string]any) bool {
	for _, key := range []string{"result", "output", "stdout", "stderr", "error", "latest_output", "output_preview", "final_message", "finalMessage", "text"} {
		if text := rawDisplayString(rawOutput[key]); strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

func TerminalToolOutputText(input ToolOutputFallbackInput) string {
	if text := TerminalRuntimeOutputText(input.Meta); text != "" {
		return text
	}
	canonical, _ := names.Resolve(input.ToolName)
	if canonical == names.Task && commandTaskTargetKind(display.ToolTaskTargetKind(nil, input.RawOutput, input.Meta)) {
		if TerminalTaskStillRunning(input.RawOutput, input.Meta) || !ToolStatusFinal(input.Status, input.Error) {
			return firstRawNonEmpty(rawDisplayString(input.RawOutput["latest_output"]), rawDisplayString(input.RawOutput["output_preview"]))
		}
		if text := display.CommandTaskOutputText(input.RawOutput); text != "" {
			return text
		}
		return ""
	}
	if !display.IsTerminalPanelTool(input.ToolName, input.ToolKind) {
		return ""
	}
	if !HasTerminalPanelMeta(input.Meta) {
		return ""
	}
	if canonical == names.Spawn {
		if input.Error || strings.EqualFold(strings.TrimSpace(input.Status), ToolStatusFailed) {
			return firstRawNonEmpty(rawDisplayString(input.RawOutput["stderr"]), rawDisplayString(input.RawOutput["error"]))
		}
		if ToolStatusFinal(input.Status, input.Error) {
			return display.SubagentTaskOutputText(input.RawOutput)
		}
		return firstRawNonEmpty(rawDisplayString(input.RawOutput["text"]), rawDisplayString(input.RawOutput["stdout"]), rawDisplayString(input.RawOutput["output_preview"]), rawDisplayString(input.RawOutput["stderr"]))
	}
	if TerminalTaskStillRunning(input.RawOutput, input.Meta) {
		return firstRawNonEmpty(rawDisplayString(input.RawOutput["latest_output"]), rawDisplayString(input.RawOutput["output_preview"]))
	}
	if !ToolStatusFinal(input.Status, input.Error) {
		return firstRawNonEmpty(rawDisplayString(input.RawOutput["latest_output"]), rawDisplayString(input.RawOutput["output_preview"]))
	}
	if text := display.CommandTaskOutputText(input.RawOutput); text != "" {
		return text
	}
	return ""
}

// DelegatedTaskResultText returns the canonical final Spawn/Task result. The
// durable tool output is authoritative; runtime task metadata is only a
// compatibility fallback for older stored events.
func DelegatedTaskResultText(input ToolOutputFallbackInput) string {
	canonical, _ := names.Resolve(input.ToolName)
	if (canonical != names.Spawn && canonical != names.Task) || !ToolStatusFinal(input.Status, input.Error) {
		return ""
	}
	targetKind := strings.ToLower(firstRawNonEmpty(
		rawDisplayString(input.RawOutput["target_kind"]),
		display.ToolTaskTargetKind(nil, input.RawOutput, input.Meta),
	))
	state := rawDisplayString(input.RawOutput["state"])
	if canonical == names.Spawn || !commandTaskTargetKind(targetKind) {
		if text := display.SubagentTaskFinalText(state, input.RawOutput); text != "" {
			return text
		}
	} else if TerminalRawOutputHasText(input.RawOutput) {
		if text := display.CommandTaskFinalText(state, input.RawOutput); text != "" {
			return text
		}
	}
	taskMeta := RuntimeTaskMeta(input.Meta)
	state = firstRawNonEmpty(rawDisplayString(taskMeta["state"]), state)
	if canonical == names.Spawn || !commandTaskTargetKind(targetKind) {
		return display.SubagentTaskFinalText(state, taskMeta)
	}
	if TerminalRawOutputHasText(taskMeta) {
		return display.CommandTaskFinalText(state, taskMeta)
	}
	return ""
}

func commandTaskTargetKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "command", "terminal":
		return true
	default:
		return false
	}
}

func TerminalTaskStillRunning(rawOutput map[string]any, meta map[string]any) bool {
	if rawBool(rawOutput["running"]) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(rawDisplayString(rawOutput["state"])), "running") {
		return true
	}
	taskMeta := RuntimeTaskMeta(meta)
	if rawBool(taskMeta["running"]) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(rawDisplayString(taskMeta["state"])), "running")
}

func HasTerminalPanelMeta(meta map[string]any) bool {
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

func TerminalRuntimeOutputText(meta map[string]any) string {
	if output, ok := metautil.TerminalOutput(meta); ok {
		return output.Data
	}
	return ""
}
