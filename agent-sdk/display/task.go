package display

import "strings"

func ToolTaskID(input map[string]any, output map[string]any, meta map[string]any) string {
	return firstNonEmpty(
		MetaString(meta, "caelis", "runtime", "tool", "target_id"),
		MapString(output, "handle"),
		MapString(output, "task_id"),
		MapString(input, "task_id"),
	)
}

func ToolTaskAction(input map[string]any, output map[string]any, meta map[string]any) string {
	return strings.ToLower(firstNonEmpty(
		MetaString(meta, "caelis", "runtime", "tool", "action"),
		MapString(output, "action"),
		MapString(input, "action"),
	))
}

func ToolTaskInput(input map[string]any, output map[string]any, meta map[string]any) string {
	return firstNonEmpty(
		MetaString(meta, "caelis", "runtime", "tool", "input"),
		MapString(output, "input"),
		MapString(input, "input"),
	)
}

func ToolTaskTargetKind(input map[string]any, output map[string]any, meta map[string]any) string {
	return strings.ToLower(firstNonEmpty(
		MetaString(meta, "caelis", "runtime", "tool", "target_kind"),
		MapString(output, "target_kind"),
		MapString(output, "kind"),
		MapString(input, "target_kind"),
	))
}

func CommandTaskOutputText(output map[string]any) string {
	return firstNonBlankText(
		mapText(output, "result"),
		mapText(output, "output"),
		mapText(output, "stdout"),
		mapText(output, "stderr"),
		mapText(output, "error"),
		mapText(output, "text"),
	)
}

func CommandTaskFinalText(state string, result map[string]any) string {
	if text := firstNonBlankText(
		mapText(result, "result"),
		mapText(result, "output"),
		mapText(result, "text"),
	); text != "" {
		return text
	}
	if text := MergedTerminalOutput(mapText(result, "stdout"), mapText(result, "stderr")); text != "" {
		return text
	}
	if strings.EqualFold(strings.TrimSpace(state), "failed") {
		if text := MapString(result, "error"); text != "" {
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

func SubagentTaskOutputText(output map[string]any) string {
	return firstNonBlankText(
		mapText(output, "final_message"),
		mapText(output, "finalMessage"),
		mapText(output, "result"),
		mapText(output, "output"),
		mapText(output, "text"),
		mapText(output, "error"),
	)
}

func SubagentTaskFinalText(state string, result map[string]any) string {
	if strings.EqualFold(strings.TrimSpace(state), "failed") {
		if text := firstNonBlankText(mapText(result, "error"), mapText(result, "result")); text != "" {
			return text
		}
	}
	return SubagentTaskOutputText(result)
}

func MergedTerminalOutput(stdout string, stderr string) string {
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

func mapText(values map[string]any, key string) string {
	text, _ := values[key].(string)
	return text
}

func firstNonBlankText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
