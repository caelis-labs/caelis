package transcript

import (
	"strings"

	"github.com/caelis-labs/caelis/ports/displaypolicy"
)

func NormalizeToolStartStatus(status string) string {
	status = strings.TrimSpace(status)
	if status == "" || strings.EqualFold(status, ToolStatusStarted) {
		return ToolStatusRunning
	}
	return status
}

func NormalizeToolResultStatus(status string, rawOutput map[string]any, isErr bool, defaultSuccessStatus string) (string, bool) {
	status = strings.TrimSpace(status)
	if status == "" {
		if inferred, ok := InferFinalStatusFromRawOutput(rawOutput); ok {
			status = inferred
		} else if isErr {
			status = ToolStatusFailed
		} else {
			status = strings.TrimSpace(defaultSuccessStatus)
		}
	}
	if status == "" {
		status = ToolStatusCompleted
	}
	isErr = isErr || strings.EqualFold(status, ToolStatusFailed)
	return status, isErr
}

func ToolStatusFinal(status string, isErr bool) bool {
	if isErr {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case ToolStatusCompleted, ToolStatusFailed, ToolStatusInterrupted, ToolStatusCancelled, "canceled":
		return true
	default:
		return false
	}
}

func ToolStream(status string, isErr bool) string {
	if isErr || strings.EqualFold(strings.TrimSpace(status), ToolStatusFailed) {
		return "stderr"
	}
	return "stdout"
}

func StandardToolOutput(status string, isErr bool) string {
	normalized := strings.ToLower(strings.TrimSpace(status))
	if isErr || normalized == ToolStatusFailed {
		return ToolStatusFailed
	}
	switch normalized {
	case ToolStatusCompleted:
		return ToolStatusCompleted
	case ToolStatusCancelled, "canceled":
		return ToolStatusCancelled
	case ToolStatusInterrupted, "terminated":
		return ToolStatusInterrupted
	}
	return ""
}

func SuppressToolResultOutput(toolName string, toolKind string, output string, synthetic bool, isErr bool) bool {
	if isErr {
		return false
	}
	if !displaypolicy.IsExplorationTool(displaypolicy.SemanticToolName(toolName, toolKind)) {
		return false
	}
	trimmed := strings.TrimSpace(output)
	return synthetic || strings.EqualFold(trimmed, ToolStatusCompleted)
}

func InferFinalStatusFromRawOutput(rawOutput map[string]any) (string, bool) {
	if len(rawOutput) == 0 {
		return "", false
	}
	if state := strings.ToLower(strings.TrimSpace(rawDisplayString(rawOutput["state"]))); state != "" {
		switch state {
		case ToolStatusCompleted, ToolStatusFailed, ToolStatusInterrupted, ToolStatusCancelled:
			return state, true
		case "canceled":
			return ToolStatusCancelled, true
		case "terminated", "timed_out", "timeout":
			return ToolStatusInterrupted, true
		}
	}
	exitCode, ok := rawExitCode(rawOutput)
	if !ok {
		return "", false
	}
	if exitCode < 0 {
		return ToolStatusCancelled, true
	}
	if exitCode == 0 {
		return ToolStatusCompleted, true
	}
	return ToolStatusFailed, true
}

func rawExitCode(rawOutput map[string]any) (int, bool) {
	raw, ok := rawOutput["exit_code"]
	if !ok || raw == nil {
		return 0, false
	}
	return rawInt(raw)
}
