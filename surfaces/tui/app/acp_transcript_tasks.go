package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"
)

func renderACPTaskStageRows(blockID string, events []SubagentEvent, idx int, status string, width int, ctx BlockRenderContext, _ acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	stage, end := compactTaskWaitRun(events, idx, status)
	actions := taskControlEvents(stage)
	if len(actions) == 0 {
		return nil, idx, false
	}
	rows := make([]RenderedRow, 0, 1)
	for _, detail := range taskWaitRunRows(actions, width) {
		rows = append(rows, StyledPlainRow(blockID, detail, styleTaskSummaryRow(detail, ctx)))
	}
	return rows, end, true
}

func compactTaskWaitRun(events []SubagentEvent, idx int, status string) ([]SubagentEvent, int) {
	if idx < 0 || idx >= len(events) || !isTaskWaitControlEvent(events, idx) {
		return nil, idx
	}
	end := idx
	for end+1 < len(events) && isTaskWaitControlEvent(events, end+1) {
		end++
	}
	settled := isTerminalACPTranscriptStatus(status) || hasLaterTranscriptStep(events, end+1)
	if !settled {
		return nil, idx
	}
	return events[idx : end+1], end
}

func compactTaskStage(events []SubagentEvent, idx int, status string) ([]SubagentEvent, int) {
	return collectTaskStage(events, idx, status, false)
}

func potentialTaskStage(events []SubagentEvent, idx int, status string) ([]SubagentEvent, int) {
	return collectTaskStage(events, idx, status, true)
}

func collectTaskStage(events []SubagentEvent, idx int, status string, includeLiveTail bool) ([]SubagentEvent, int) {
	if idx < 0 || idx >= len(events) {
		return nil, idx
	}
	stage := make([]SubagentEvent, 0, 6)
	end := idx - 1
	for i := idx; i < len(events); {
		step, ok := collectTaskTranscriptStep(events, i)
		if !ok {
			break
		}
		settled := isTerminalACPTranscriptStatus(status) || (step.allDone && hasLaterTranscriptStep(events, step.end+1))
		if !settled {
			if includeLiveTail && len(stage) == 0 {
				stage = append(stage, events[step.start:step.end+1]...)
				end = step.end
			}
			break
		}
		stage = append(stage, events[step.start:step.end+1]...)
		end = step.end
		i = step.end + 1
	}
	return stage, end
}

func collectTaskTranscriptStep(events []SubagentEvent, idx int) (transcriptStep, bool) {
	if idx < 0 || idx >= len(events) {
		return transcriptStep{}, false
	}
	i := idx
	for i < len(events) && isTaskNarrativeEvent(events[i]) {
		i++
	}
	if i >= len(events) || !isGroupedTaskControlEvent(events, i) {
		return transcriptStep{}, false
	}
	step := transcriptStep{
		start:   idx,
		end:     i,
		allDone: true,
	}
	for i < len(events) && isGroupedTaskControlEvent(events, i) {
		step.end = i
		i++
	}
	return step, true
}

func taskControlEvents(events []SubagentEvent) []SubagentEvent {
	out := make([]SubagentEvent, 0, len(events))
	seen := map[string]struct{}{}
	for _, ev := range events {
		if !isTaskControlEvent(ev) {
			continue
		}
		key := strings.TrimSpace(ev.CallID)
		if key != "" {
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
		}
		out = append(out, ev)
	}
	return out
}

func taskWaitRunRows(events []SubagentEvent, width int) []string {
	waits := make([]string, 0, len(events))
	for _, ev := range events {
		verb, detail := splitTaskAction(ev.Args)
		if !strings.EqualFold(verb, "Wait") {
			continue
		}
		waits = append(waits, detail)
	}
	detail := strings.Join(compactNonEmpty(waits), ", ")
	return wrapExplorationSummaryDetail("• ", "Wait", detail, width)
}

func taskStageDetailRows(events []SubagentEvent, width int) []string {
	type taskSummaryItem struct {
		verb   string
		detail string
	}
	var waits []string
	items := make([]taskSummaryItem, 0, len(events))
	for _, ev := range events {
		verb, detail := splitTaskAction(ev.Args)
		if verb == "" {
			continue
		}
		if strings.EqualFold(verb, "Wait") {
			waits = append(waits, detail)
			continue
		}
		items = append(items, taskSummaryItem{verb: verb, detail: detail})
	}
	if len(waits) > 0 {
		items = append([]taskSummaryItem{{
			verb:   "Wait",
			detail: strings.Join(compactNonEmpty(waits), ", "),
		}}, items...)
	}
	rows := make([]string, 0, len(items))
	for _, item := range items {
		prefix := "  "
		if len(rows) == 0 {
			prefix += "└ "
		} else {
			prefix += "  "
		}
		rows = append(rows, wrapExplorationSummaryDetail(prefix, item.verb, item.detail, width)...)
	}
	return rows
}

func taskStageExpandedRows(blockID string, events []SubagentEvent, width int, ctx BlockRenderContext, token string) []RenderedRow {
	rows := make([]RenderedRow, 0, len(events))
	for _, ev := range events {
		first := len(rows) == 0
		switch ev.Kind {
		case SEReasoning:
			rows = append(rows, renderExplorationNarrativeRows(blockID, ev.Text, width, ctx, ctx.Theme.ReasoningStyle(), token, first)...)
		case SEAssistant:
			rows = append(rows, renderExplorationNarrativeRows(blockID, ev.Text, width, ctx, ctx.Theme.TextStyle(), token, first)...)
		case SEToolCall:
			if !isTaskControlEvent(ev) {
				continue
			}
			rows = append(rows, renderTaskControlRow(blockID, ev, width, ctx, token, first))
		}
	}
	return rows
}

func renderTaskControlRow(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext, token string, first bool) RenderedRow {
	verb, detail := splitTaskAction(ev.Args)
	if verb == "" {
		verb = "Task"
	}
	prefix := explorationChildPrefix(first)
	detail = truncateTailDisplay(detail, maxInt(16, width-displayColumns(prefix)-displayColumns(verb)-1))
	plain := prefix + strings.TrimSpace(verb+" "+detail)
	return StyledPlainClickableRow(blockID, plain, styleTaskSummaryRow(plain, ctx), token)
}

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
	case "wait", "write", "cancel":
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

func taskStageKey(events []SubagentEvent) string {
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		if id := strings.TrimSpace(ev.CallID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	return "tasks:" + strings.Join(ids, ",")
}

func acpTaskStageClickToken(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return "acp_task_stage:" + key
}

func isTaskNarrativeEvent(ev SubagentEvent) bool {
	return ev.Kind == SEReasoning || ev.Kind == SEAssistant
}

func hasTaskNarrative(events []SubagentEvent) bool {
	for _, ev := range events {
		if isTaskNarrativeEvent(ev) {
			return true
		}
	}
	return false
}

func styleTaskSummaryRow(row string, ctx BlockRenderContext) string {
	plainPrefix := ""
	content := row
	if strings.HasPrefix(row, "• ") {
		plainPrefix = "• "
		content = strings.TrimPrefix(row, plainPrefix)
	} else if strings.HasPrefix(row, "  └ ") {
		plainPrefix = "  └ "
		content = strings.TrimPrefix(row, plainPrefix)
	} else if strings.HasPrefix(row, "    ") {
		plainPrefix = "    "
		content = strings.TrimPrefix(row, plainPrefix)
	} else if strings.HasPrefix(row, "  ") {
		plainPrefix = "  "
		content = strings.TrimPrefix(row, plainPrefix)
	}
	verb, detail := splitTaskAction(content)
	styled := ctx.Theme.TranscriptMetaStyle().Render(plainPrefix)
	if verb != "" {
		styled += toolActionStyle(ctx, verb).Render(verb)
	}
	if detail != "" {
		if strings.EqualFold(verb, "Cancel") {
			styled += " " + ctx.Theme.SecondaryTextStyle().Render(detail)
		} else {
			styled += " " + styleTaskDetail(detail, ctx)
		}
	}
	return styled
}

func styleTaskDetail(detail string, ctx BlockRenderContext) string {
	detail = strings.TrimSpace(detail)
	first, rest, ok := strings.Cut(detail, " ")
	if !ok {
		if isTaskHandleDetail(detail) {
			return lipgloss.NewStyle().Foreground(ctx.Theme.Focus).Render(detail)
		}
		return ctx.Theme.SecondaryTextStyle().Render(detail)
	}
	if !isTaskHandleDetail(first) {
		return ctx.Theme.SecondaryTextStyle().Render(detail)
	}
	return lipgloss.NewStyle().Foreground(ctx.Theme.Focus).Render(first) + ctx.Theme.SecondaryTextStyle().Render(" "+rest)
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

func isGroupedTaskControlEvent(events []SubagentEvent, idx int) bool {
	if idx < 0 || idx >= len(events) {
		return false
	}
	return isTaskControlEvent(events[idx]) && taskEventAction(events[idx]) != "write"
}

func isTaskWaitControlEvent(events []SubagentEvent, idx int) bool {
	if idx < 0 || idx >= len(events) {
		return false
	}
	return isTaskControlEvent(events[idx]) && taskEventAction(events[idx]) == "wait"
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
	taskID := strings.TrimSpace(ev.TaskID)
	if taskID == "" {
		return false
	}
	for i := idx - 1; i >= 0; i-- {
		prev := events[i]
		if prev.Kind != SEToolCall || strings.TrimSpace(prev.TaskID) != taskID {
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
