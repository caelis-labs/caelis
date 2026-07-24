package tuiapp

import (
	"strings"
)

func splitTaskAction(action string) (string, string) {
	action = sanitizeRenderableText(action)
	if action == "" {
		return "", ""
	}
	if display := normalizeRawTaskAction(action); display != "" {
		action = display
	}
	verb, detail, ok := strings.Cut(action, " ")
	if !ok {
		return normalizeTaskVerb(action), ""
	}
	verb = normalizeTaskVerb(verb)
	detail = strings.TrimSpace(detail)
	if isTaskActionVerb(verb) {
		detail = stripInternalTaskIDDetail(detail)
	}
	return verb, detail
}

func normalizeRawTaskAction(action string) string {
	fields := strings.Fields(strings.TrimSpace(action))
	if len(fields) == 0 || !strings.EqualFold(fields[0], "TASK") {
		return ""
	}
	return taskControlDisplayFallback(action)
}

func normalizeTaskVerb(verb string) string {
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "wait":
		return "Wait"
	case "read":
		return "Read"
	case "write":
		return "Write"
	case "cancel":
		return "Cancel"
	default:
		return strings.TrimSpace(verb)
	}
}

func isTaskActionVerb(verb string) bool {
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "wait", "read", "write", "cancel":
		return true
	default:
		return false
	}
}

func stripInternalTaskIDDetail(detail string) string {
	fields := strings.Fields(strings.TrimSpace(detail))
	if len(fields) == 0 {
		return ""
	}
	if taskHandleDisplay(fields[0]) != "" {
		return strings.TrimSpace(detail)
	}
	return strings.TrimSpace(strings.Join(fields[1:], " "))
}

func isTaskHandleDetail(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	if strings.HasSuffix(lower, "s") || strings.HasSuffix(lower, "ms") {
		return false
	}
	if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
		return false
	}
	return true
}

func isTaskControlEvent(ev SubagentEvent) bool {
	return ev.Kind == SEToolCall && strings.EqualFold(strings.TrimSpace(ev.Name), "TASK")
}

func isSubagentTaskWriteEvent(events []SubagentEvent, idx int) bool {
	if idx < 0 || idx >= len(events) {
		return false
	}
	ev := events[idx]
	if !isTaskControlEvent(ev) || taskEventAction(ev) != "write" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(ev.TaskTargetKind), "subagent") {
		return true
	}
	taskID := strings.TrimSpace(ev.TaskHandle)
	if taskID == "" {
		return false
	}
	for i := idx - 1; i >= 0; i-- {
		prev := events[i]
		if prev.Kind != SEToolCall || strings.TrimSpace(prev.TaskHandle) != taskID {
			continue
		}
		if strings.EqualFold(toolSemanticName(prev.Name, prev.ToolKind), "SPAWN") {
			return true
		}
		if isTerminalPanelToolEvent(prev) {
			return false
		}
	}
	return false
}

func isTaskWritePanelEvent(ev SubagentEvent) bool {
	return isTaskControlEvent(ev) &&
		taskEventAction(ev) == "write" &&
		strings.EqualFold(strings.TrimSpace(ev.TaskTargetKind), "subagent")
}

func taskEventAction(ev SubagentEvent) string {
	if action := strings.ToLower(strings.TrimSpace(ev.TaskAction)); action != "" {
		return action
	}
	verb, _ := splitTaskAction(ev.Args)
	return strings.ToLower(strings.TrimSpace(verb))
}
