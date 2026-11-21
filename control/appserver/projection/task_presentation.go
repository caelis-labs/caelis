package projection

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/display"
)

// ToolTaskHandle returns the public Session-scoped Task identity used in
// tool display and observation metadata. Legacy task_id payloads still supply
// a display handle for historical transcripts.
func ToolTaskHandle(input map[string]any, output map[string]any, meta map[string]any) string {
	return firstTaskDisplayValue(
		display.MetaString(meta, "caelis", "runtime", "tool", "target_handle"),
		display.MapString(output, "handle"),
		display.MapString(input, "handle"),
		display.MapString(output, "task_id"),
		display.MapString(input, "task_id"),
	)
}

// ToolTaskAction returns the normalized action for a Task row.
func ToolTaskAction(input map[string]any, output map[string]any, meta map[string]any) string {
	return strings.ToLower(firstTaskDisplayValue(
		display.MetaString(meta, "caelis", "runtime", "tool", "action"),
		display.MapString(output, "action"),
		display.MapString(input, "action"),
	))
}

// ToolTaskInput returns the reported input for a Task row.
func ToolTaskInput(input map[string]any, output map[string]any, meta map[string]any) string {
	return firstTaskDisplayValue(
		display.MetaString(meta, "caelis", "runtime", "tool", "input"),
		display.MapString(output, "input"),
		display.MapString(input, "input"),
	)
}

// ToolTaskTargetKind returns the target kind used to choose a Task presentation.
func ToolTaskTargetKind(input map[string]any, output map[string]any, meta map[string]any) string {
	return strings.ToLower(firstTaskDisplayValue(
		display.MetaString(meta, "caelis", "runtime", "tool", "target_kind"),
		display.MapString(output, "target_kind"),
		display.MapString(output, "kind"),
		display.MapString(input, "target_kind"),
	))
}

// CommandTaskOutputText projects available text from a command Task output.
func CommandTaskOutputText(output map[string]any) string {
	return projectedFirstNonBlankRaw(
		projectedToolRawString(output["result"]),
		projectedToolRawString(output["output"]),
		projectedToolRawString(output["stdout"]),
		projectedToolRawString(output["stderr"]),
		projectedToolRawString(output["error"]),
		projectedToolRawString(output["text"]),
	)
}

// CommandTaskFinalText projects a final command Task result with legacy stream fallbacks.
func CommandTaskFinalText(state string, result map[string]any) string {
	if text := projectedFirstNonBlankRaw(
		projectedToolRawString(result["result"]),
		projectedToolRawString(result["output"]),
		projectedToolRawString(result["text"]),
	); text != "" {
		return text
	}
	if text := mergedTerminalOutput(projectedToolRawString(result["stdout"]), projectedToolRawString(result["stderr"])); text != "" {
		return text
	}
	if strings.EqualFold(strings.TrimSpace(state), "failed") {
		if text := display.MapString(result, "error"); text != "" {
			return text
		}
	}
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "completed", "failed":
		return "(no output)"
	default:
		return ""
	}
}

// SubagentTaskOutputText selects the first available child output field.
func SubagentTaskOutputText(output map[string]any) string {
	return projectedFirstNonBlankRaw(
		projectedToolRawString(output["final_message"]),
		projectedToolRawString(output["finalMessage"]),
		projectedToolRawString(output["result"]),
		projectedToolRawString(output["output"]),
		projectedToolRawString(output["text"]),
		projectedToolRawString(output["error"]),
	)
}

// SubagentTaskFinalText selects final child output, preferring errors for failed Tasks.
func SubagentTaskFinalText(state string, result map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(state), "failed") {
		if text := projectedFirstNonBlankRaw(projectedToolRawString(result["error"]), projectedToolRawString(result["result"])); text != "" {
			return text
		}
	}
	return SubagentTaskOutputText(result)
}

func mergedTerminalOutput(stdout string, stderr string) string {
	if strings.TrimSpace(stdout) == "" {
		stdout = ""
	}
	if strings.TrimSpace(stderr) == "" {
		stderr = ""
	}
	switch {
	case stdout != "" && stderr != "":
		if strings.HasSuffix(stdout, "\n") || strings.HasSuffix(stdout, "\r") ||
			strings.HasPrefix(stderr, "\n") || strings.HasPrefix(stderr, "\r") {
			return stdout + stderr
		}
		return stdout + "\n" + stderr
	case stdout != "":
		return stdout
	case stderr != "":
		return stderr
	default:
		return ""
	}
}

// Historical tool metadata could carry the literal string "<nil>".
func firstTaskDisplayValue(values ...string) string {
	for _, value := range values {
		if value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}
