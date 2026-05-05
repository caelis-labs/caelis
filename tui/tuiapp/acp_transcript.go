package tuiapp

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/OnslaughtSnail/caelis/sdk/displaypolicy"
	"github.com/OnslaughtSnail/caelis/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

// ACP transcript rendering is driven by protocol event classes rather than
// by session source. Internal ACP children and external ACP participants
// should therefore render through the same event timeline.

type acpTranscriptRenderOptions struct {
	EmptyPlaceholder       string
	UseStatusPlaceholder   bool
	PlaceholderAsMeta      bool
	HideWaitingApprovalRow bool
	HideCompletedRow       bool
	ToolOutputPanels       bool
	ToolPanelExpanded      func(callID string) bool
	ToolPanelFullOutput    func(callID string) bool
	ToolPanelRows          func(toolPanelRenderRequest) []RenderedRow
	ExplorationExpanded    func(key string) bool
	ToolPanelScrollState   func(callID string) toolPanelScrollState
	ReasoningExpanded      func(key string) bool
}

type toolPanelRenderRequest struct {
	BlockID  string
	CallID   string
	ToolName string
	Text     string
	Width    int
	Ctx      BlockRenderContext
	Err      bool
}

const (
	acpToolInlineArgsMaxWidth  = 72
	acpToolDetailPreviewBudget = 4
	acpTerminalPanelMaxLines   = 5
)

func renderACPTranscriptRows(blockID string, events []SubagentEvent, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []RenderedRow {
	visible := visibleNarrativeEvents(events, status)
	rows := make([]RenderedRow, 0, len(visible)+2)
	hasContent := false
	lastGroup := acpTranscriptGroupNone
	for i := 0; i < len(visible); i++ {
		ev := visible[i]
		switch ev.Kind {
		case SEPlan:
			rows = append(rows, renderACPPlanRows(blockID, ev, width, ctx)...)
			hasContent = hasContent || len(ev.PlanEntries) > 0
			lastGroup = acpTranscriptGroupPlan
		case SEReasoning:
			if taskRows, consumed, ok := renderACPTaskStageRows(blockID, visible, i, width, ctx, opts); ok {
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupTask, false)
				rows = append(rows, taskRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupTask
				i = consumed
				continue
			}
			if explorationRows, consumed, ok := renderACPExplorationStageRows(blockID, visible, i, width, ctx, opts); ok {
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupExploration, false)
				rows = append(rows, explorationRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupExploration
				i = consumed
				continue
			}
			if ev.Text != "" {
				shouldFoldReasoning := reasoningShouldFold(visible, i, status)
				text := ev.Text
				reasoningEnd := i
				if shouldFoldReasoning {
					text, reasoningEnd = collectConsecutiveReasoning(visible, i)
					expanded := reasoningExpanded(opts, reasoningFoldKey(i))
					if !expanded {
						rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupNarrative, false)
						rows = append(rows, renderACPReasoningSummaryRow(blockID, SubagentEvent{Text: text}, i, width, ctx, expanded))
						hasContent = true
						lastGroup = acpTranscriptGroupNarrative
						i = reasoningEnd
						continue
					}
				}
				if !shouldFoldReasoning {
					rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupNarrative, false)
					rows = append(rows, renderParticipantTurnNarrativeRows(blockID, text, tuikit.LineStyleReasoning, width, ctx, participantNarrativeEventActive(visible, reasoningEnd, status))...)
				} else {
					rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupNarrative, false)
					rows = append(rows, renderACPReasoningExpandedRows(blockID, text, i, width, ctx, participantNarrativeEventActive(visible, reasoningEnd, status))...)
				}
				hasContent = true
				lastGroup = acpTranscriptGroupNarrative
				i = reasoningEnd
			}
		case SEAssistant:
			if taskRows, consumed, ok := renderACPTaskStageRows(blockID, visible, i, width, ctx, opts); ok {
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupTask, false)
				rows = append(rows, taskRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupTask
				i = consumed
				continue
			}
			if explorationRows, consumed, ok := renderACPExplorationStageRows(blockID, visible, i, width, ctx, opts); ok {
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupExploration, false)
				rows = append(rows, explorationRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupExploration
				i = consumed
				continue
			}
			if ev.Text != "" {
				text := ev.Text
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupNarrative, false)
				rows = append(rows, renderParticipantTurnNarrativeRows(blockID, text, tuikit.LineStyleAssistant, width, ctx, participantNarrativeEventActive(visible, i, status))...)
				hasContent = true
				lastGroup = acpTranscriptGroupNarrative
			}
		case SEToolCall:
			if taskRows, consumed, ok := renderACPTaskStageRows(blockID, visible, i, width, ctx, opts); ok {
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupTask, false)
				rows = append(rows, taskRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupTask
				i = consumed
				continue
			}
			if explorationRows, consumed, ok := renderACPExplorationStageRows(blockID, visible, i, width, ctx, opts); ok {
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupExploration, false)
				rows = append(rows, explorationRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupExploration
				i = consumed
				continue
			}
			if groupRows, consumed, ok := renderACPExplorationGroupRows(blockID, visible, i, width, ctx, opts); ok {
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupExploration, false)
				rows = append(rows, groupRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupExploration
				i = consumed
				continue
			}
			toolRows, consumed := renderACPToolLifecycleRows(blockID, visible, i, width, ctx, opts)
			if len(toolRows) > 0 {
				attached := lastGroup == acpTranscriptGroupNarrative && toolContinuesPreviousNarrative(visible, i)
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupTool, attached)
				rows = append(rows, toolRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupTool
			}
			i = consumed
		case SEApproval:
			continue
		}
	}
	if !hasContent {
		placeholder := strings.TrimSpace(opts.EmptyPlaceholder)
		if placeholder == "" && opts.UseStatusPlaceholder {
			placeholder = participantTurnEmptyPlaceholder(status)
		}
		if placeholder != "" {
			style := ctx.Theme.HelpHintTextStyle().Width(width)
			if opts.PlaceholderAsMeta {
				style = ctx.Theme.TranscriptMetaStyle().Width(width)
			}
			rows = append(rows, StyledPlainRow(blockID, placeholder, style.Render(placeholder)))
		}
	}
	rows = append(rows, renderACPStatusRows(blockID, status, width, ctx, opts)...)
	return rows
}

type acpTranscriptGroupKind int

const (
	acpTranscriptGroupNone acpTranscriptGroupKind = iota
	acpTranscriptGroupNarrative
	acpTranscriptGroupExploration
	acpTranscriptGroupTool
	acpTranscriptGroupTask
	acpTranscriptGroupPlan
)

func appendACPTranscriptGroupGap(rows []RenderedRow, blockID string, previous acpTranscriptGroupKind, current acpTranscriptGroupKind, attached bool) []RenderedRow {
	if len(rows) == 0 || previous == acpTranscriptGroupNone || current == acpTranscriptGroupNone {
		return rows
	}
	if attached {
		return rows
	}
	if previous == acpTranscriptGroupNarrative && current == acpTranscriptGroupNarrative {
		return rows
	}
	return appendBlankRowIfNeeded(rows, blockID)
}

func toolContinuesPreviousNarrative(events []SubagentEvent, idx int) bool {
	if idx <= 0 || idx >= len(events) {
		return false
	}
	switch events[idx-1].Kind {
	case SEReasoning, SEAssistant:
	default:
		return false
	}
	ev := events[idx]
	if ev.Kind != SEToolCall || isTaskControlEvent(ev) || isCompactExplorationTool(ev) {
		return false
	}
	return true
}

func appendBlankRowIfNeeded(rows []RenderedRow, blockID string) []RenderedRow {
	if len(rows) == 0 {
		return rows
	}
	if strings.TrimSpace(rows[len(rows)-1].Plain) == "" {
		return rows
	}
	return append(rows, PlainRow(blockID, ""))
}

func reasoningFoldKey(idx int) string {
	return strconv.Itoa(idx)
}

func reasoningExpanded(opts acpTranscriptRenderOptions, key string) bool {
	if opts.ReasoningExpanded == nil {
		return false
	}
	return opts.ReasoningExpanded(key)
}

func reasoningShouldFold(events []SubagentEvent, idx int, _ string) bool {
	if idx < 0 || idx >= len(events) || events[idx].Kind != SEReasoning {
		return false
	}
	for i := idx + 1; i < len(events); i++ {
		ev := events[i]
		if ev.Kind == SEReasoning {
			continue
		}
		if ev.Kind == SEToolCall && ev.Done && isAttentionLoopTool(ev.Name) {
			return true
		}
	}
	return false
}

func collectConsecutiveReasoning(events []SubagentEvent, idx int) (string, int) {
	if idx < 0 || idx >= len(events) || events[idx].Kind != SEReasoning {
		return "", idx
	}
	text := ""
	end := idx
	for i := idx; i < len(events); i++ {
		if events[i].Kind != SEReasoning {
			break
		}
		text = appendDeltaStreamChunk(text, events[i].Text)
		end = i
	}
	return collapseRepeatedNarrativeText(text), end
}

func renderACPReasoningSummaryRow(blockID string, ev SubagentEvent, idx int, width int, ctx BlockRenderContext, expanded bool) RenderedRow {
	arrow := ">"
	if expanded {
		arrow = "∨"
	}
	preview := reasoningPreviewText(ev.Text, width)
	plain := strings.TrimSpace(arrow + " " + preview)
	styled := ctx.Theme.ToolNameStyle().Bold(true).Render(arrow)
	if preview != "" {
		styled += ctx.Theme.ReasoningStyle().Render(" " + preview)
	}
	return StyledPlainClickableRow(blockID, plain, styled, acpReasoningClickToken(reasoningFoldKey(idx)))
}

func reasoningPreviewText(text string, width int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" {
		return ""
	}
	budget := maxInt(12, width-displayColumns("> "))
	return strings.TrimSpace(truncateReasoningPreviewMiddle(text, budget))
}

func reasoningExpandedBodyVisible(text string, width int) bool {
	normalized := strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if normalized == "" {
		return false
	}
	return normalized != reasoningPreviewText(text, width)
}

func renderACPReasoningExpandedRows(blockID string, text string, idx int, width int, ctx BlockRenderContext, active bool) []RenderedRow {
	rows := renderParticipantTurnNarrativeRows(blockID, text, tuikit.LineStyleReasoning, width, ctx, active)
	rows = applyClickTokenToRows(rows, acpReasoningClickToken(reasoningFoldKey(idx)))
	if len(rows) == 0 {
		return rows
	}
	firstPlain := strings.TrimPrefix(rows[0].Plain, "· ")
	firstPlain = strings.TrimPrefix(firstPlain, "  ")
	plain := strings.TrimSpace("∨ " + firstPlain)
	styled := ctx.Theme.ToolNameStyle().Bold(true).Render("∨")
	if firstPlain != "" {
		styled += ctx.Theme.ReasoningStyle().Render(" " + firstPlain)
	}
	rows[0] = StyledPlainClickableRow(blockID, plain, styled, acpReasoningClickToken(reasoningFoldKey(idx)))
	return rows
}

func truncateReasoningPreviewMiddle(text string, budget int) string {
	text = strings.TrimSpace(text)
	if budget <= 0 || displayColumns(text) <= budget {
		return text
	}
	if budget <= 3 {
		return truncateDisplayCells(text, budget)
	}
	leftBudget := maxInt(1, (budget-3+1)/2)
	rightBudget := maxInt(1, budget-3-leftBudget)
	left := truncateDisplayCells(text, leftBudget)
	right := truncateDisplayCellsFromEnd(text, rightBudget)
	return strings.TrimSpace(left) + "..." + strings.TrimSpace(right)
}

func truncateDisplayCells(text string, budget int) string {
	if budget <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0
	for _, r := range text {
		w := displayColumns(string(r))
		if used+w > budget {
			break
		}
		b.WriteRune(r)
		used += w
	}
	return b.String()
}

func truncateDisplayCellsFromEnd(text string, budget int) string {
	if budget <= 0 {
		return ""
	}
	runes := []rune(text)
	used := 0
	start := len(runes)
	for i := len(runes) - 1; i >= 0; i-- {
		w := displayColumns(string(runes[i]))
		if used+w > budget {
			break
		}
		used += w
		start = i
	}
	return string(runes[start:])
}

func applyClickTokenToRows(rows []RenderedRow, token string) []RenderedRow {
	if token == "" {
		return rows
	}
	out := append([]RenderedRow(nil), rows...)
	for i := range out {
		out[i].ClickToken = token
	}
	return out
}

func renderACPTaskStageRows(blockID string, events []SubagentEvent, idx int, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	stage, end := compactTaskStage(events, idx)
	actions := taskControlEvents(stage)
	if len(actions) == 0 {
		return nil, idx, false
	}
	key := taskStageKey(actions)
	token := acpTaskStageClickToken(key)
	expanded := false
	if opts.ExplorationExpanded != nil {
		expanded = opts.ExplorationExpanded(key)
	}
	header := "• Tasks"
	rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
	if expanded {
		rows = append(rows, taskStageExpandedRows(blockID, stage, width, ctx, token)...)
		return rows, end, true
	}
	for _, detail := range taskStageDetailRows(actions, width) {
		rows = append(rows, StyledPlainClickableRow(blockID, detail, styleTaskSummaryRow(detail, ctx), token))
	}
	return rows, end, true
}

func compactTaskStage(events []SubagentEvent, idx int) ([]SubagentEvent, int) {
	if idx < 0 || idx >= len(events) {
		return nil, idx
	}
	if !isGroupedTaskControlEvent(events, idx) && (!isTaskNarrativeEvent(events[idx]) || !hasLaterTaskControl(events, idx+1)) {
		return nil, idx
	}
	stage := make([]SubagentEvent, 0, 6)
	end := idx - 1
	for i := idx; i < len(events); i++ {
		ev := events[i]
		if isGroupedTaskControlEvent(events, i) {
			stage = append(stage, ev)
			end = i
			continue
		}
		if isTaskNarrativeEvent(ev) && hasLaterTaskControl(events, i+1) {
			stage = append(stage, ev)
			end = i
			continue
		}
		break
	}
	return stage, end
}

func taskControlEvents(events []SubagentEvent) []SubagentEvent {
	out := make([]SubagentEvent, 0, len(events))
	seen := map[string]struct{}{}
	for i, ev := range events {
		if !isTaskControlEvent(ev) {
			continue
		}
		key := strings.TrimSpace(ev.CallID)
		if key == "" {
			key = strconv.Itoa(i)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, ev)
	}
	return out
}

func taskStageDetailRows(events []SubagentEvent, width int) []string {
	rows := make([]string, 0, len(events))
	seen := map[string]struct{}{}
	for _, ev := range events {
		verb, detail := splitTaskAction(ev.Args)
		if verb == "" {
			continue
		}
		key := strings.TrimSpace(strings.ToLower(verb + " " + detail))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		prefix := "  "
		if len(rows) == 0 {
			prefix += "└ "
		} else {
			prefix += "  "
		}
		rows = append(rows, wrapExplorationSummaryDetail(prefix, verb, detail, width)...)
	}
	return rows
}

func taskStageExpandedRows(blockID string, events []SubagentEvent, width int, ctx BlockRenderContext, token string) []RenderedRow {
	rows := make([]RenderedRow, 0, len(events))
	for _, ev := range events {
		first := len(rows) == 0
		switch ev.Kind {
		case SEReasoning:
			rows = append(rows, renderExplorationNarrativeRows(blockID, ev.Text, width, ctx.Theme.ReasoningStyle(), token, first)...)
		case SEAssistant:
			rows = append(rows, renderExplorationNarrativeRows(blockID, ev.Text, width, ctx.Theme.TextStyle(), token, first)...)
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
	action = strings.TrimSpace(action)
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

func hasLaterTaskControl(events []SubagentEvent, start int) bool {
	for i := start; i < len(events); i++ {
		ev := events[i]
		if isGroupedTaskControlEvent(events, i) {
			return true
		}
		if !isTaskNarrativeEvent(ev) {
			return false
		}
	}
	return false
}

func styleTaskSummaryRow(row string, ctx BlockRenderContext) string {
	plainPrefix := ""
	content := row
	if strings.HasPrefix(row, "  └ ") {
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
		styled += " " + styleTaskDetail(detail, ctx)
	}
	return styled
}

func styleTaskDetail(detail string, ctx BlockRenderContext) string {
	first, rest, ok := strings.Cut(strings.TrimSpace(detail), " ")
	if !ok {
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
	return isTaskControlEvent(events[idx]) && !isSubagentTaskWriteEvent(events, idx)
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

func renderACPExplorationStageRows(blockID string, events []SubagentEvent, idx int, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	stage, end := compactExplorationStage(events, idx)
	if countExplorationTools(stage) == 0 || (countExplorationTools(stage) < 2 && !hasExplorationNarrative(stage)) {
		return nil, idx, false
	}
	key := explorationStageKey(stage)
	token := acpExplorationStageClickToken(key)
	expanded := false
	if opts.ExplorationExpanded != nil {
		expanded = opts.ExplorationExpanded(key)
	}
	header := "• Explored"
	if expanded {
		rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
		expandedRows := explorationStageExpandedRows(blockID, stage, width, ctx, token)
		rows = append(rows, expandedRows...)
		return rows, end, true
	}
	rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
	for _, detail := range explorationGroupDetailRows(explorationToolEvents(stage), width) {
		rows = append(rows, StyledPlainClickableRow(blockID, detail, styleExplorationSummaryRow(detail, ctx), token))
	}
	return rows, end, true
}

func compactExplorationStage(events []SubagentEvent, idx int) ([]SubagentEvent, int) {
	if idx < 0 || idx >= len(events) {
		return nil, idx
	}
	if !isCompactExplorationTool(events[idx]) && (!isExplorationNarrativeEvent(events[idx]) || !hasLaterExplorationTool(events, idx+1)) {
		return nil, idx
	}
	stage := make([]SubagentEvent, 0, 8)
	end := idx - 1
	for i := idx; i < len(events); i++ {
		ev := events[i]
		if isCompactExplorationTool(ev) {
			stage = append(stage, ev)
			end = i
			continue
		}
		if isExplorationNarrativeEvent(ev) && hasLaterExplorationTool(events, i+1) {
			stage = append(stage, ev)
			end = i
			continue
		}
		break
	}
	return stage, end
}

func isExplorationNarrativeEvent(ev SubagentEvent) bool {
	return ev.Kind == SEReasoning || ev.Kind == SEAssistant
}

func hasExplorationNarrative(events []SubagentEvent) bool {
	for _, ev := range events {
		if isExplorationNarrativeEvent(ev) {
			return true
		}
	}
	return false
}

func hasLaterExplorationTool(events []SubagentEvent, start int) bool {
	for i := start; i < len(events); i++ {
		ev := events[i]
		if isCompactExplorationTool(ev) {
			return true
		}
		if !isExplorationNarrativeEvent(ev) {
			return false
		}
	}
	return false
}

func countExplorationTools(events []SubagentEvent) int {
	count := 0
	for _, ev := range events {
		if isCompactExplorationTool(ev) {
			count++
		}
	}
	return count
}

func explorationToolEvents(events []SubagentEvent) []SubagentEvent {
	out := make([]SubagentEvent, 0, len(events))
	for _, ev := range events {
		if isCompactExplorationTool(ev) {
			out = append(out, ev)
		}
	}
	return out
}

func explorationStageExpandedRows(blockID string, events []SubagentEvent, width int, ctx BlockRenderContext, token string) []RenderedRow {
	rows := make([]RenderedRow, 0, len(events))
	for _, ev := range events {
		first := len(rows) == 0
		switch ev.Kind {
		case SEReasoning:
			rows = append(rows, renderExplorationNarrativeRows(blockID, ev.Text, width, ctx.Theme.ReasoningStyle(), token, first)...)
		case SEAssistant:
			rows = append(rows, renderExplorationNarrativeRows(blockID, ev.Text, width, ctx.Theme.TextStyle(), token, first)...)
		case SEToolCall:
			if isCompactExplorationTool(ev) {
				rows = append(rows, renderExplorationToolRow(blockID, ev, width, ctx, token, first))
			}
		}
	}
	return rows
}

func renderExplorationNarrativeRows(blockID string, text string, width int, style lipgloss.Style, token string, first bool) []RenderedRow {
	text = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"))
	if text == "" {
		return nil
	}
	prefix := explorationChildPrefix(first)
	continuation := strings.Repeat(" ", displayColumns(prefix))
	bodyWidth := maxInt(16, width-displayColumns(prefix))
	rows := make([]RenderedRow, 0, 2)
	firstLine := true
	for _, raw := range strings.Split(text, "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		for _, segment := range strings.Split(hardWrapDisplayLine(raw, bodyWidth), "\n") {
			linePrefix := continuation
			if firstLine {
				linePrefix = prefix
				firstLine = false
			}
			plain := linePrefix + segment
			rows = append(rows, StyledPlainClickableRow(blockID, plain, style.Width(width).Render(plain), token))
		}
	}
	return rows
}

func renderExplorationToolRow(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext, token string, first bool) RenderedRow {
	verb := explorationToolVerb(ev.Name)
	if verb == "" {
		verb = strings.ToUpper(strings.TrimSpace(ev.Name))
	}
	detail := explorationToolDetail(ev)
	prefix := explorationChildPrefix(first)
	detail = truncateTailDisplay(detail, maxInt(16, width-displayColumns(prefix)-displayColumns(verb)-1))
	plain := prefix + strings.TrimSpace(verb+" "+detail)
	styled := ctx.Theme.TranscriptMetaStyle().Render(prefix) +
		toolActionStyle(ctx, verb).Render(verb)
	if detail != "" {
		styled += " " + styleExplorationDetail(detail, ctx)
	}
	return StyledPlainClickableRow(blockID, plain, styled, token)
}

func explorationChildPrefix(first bool) string {
	if first {
		return "  └ "
	}
	return "    "
}

func explorationStageKey(events []SubagentEvent) string {
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.Kind != SEToolCall {
			continue
		}
		if id := strings.TrimSpace(ev.CallID); id != "" {
			ids = append(ids, id)
		}
	}
	return strings.Join(ids, ",")
}

func acpExplorationStageClickToken(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return "acp_exploration_stage:" + key
}

func renderACPExplorationGroupRows(blockID string, events []SubagentEvent, idx int, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	group, end := compactExplorationGroup(events, idx, opts)
	if len(group) < 2 {
		return nil, idx, false
	}
	summary := "• Explored"
	token := explorationGroupClickToken(group)
	rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, summary, width, ctx, token)}
	for _, detail := range explorationGroupDetailRows(group, width) {
		rows = append(rows, StyledPlainClickableRow(blockID, detail, styleExplorationSummaryRow(detail, ctx), token))
	}
	return rows, end, true
}

func compactExplorationGroup(events []SubagentEvent, idx int, opts acpTranscriptRenderOptions) ([]SubagentEvent, int) {
	if idx < 0 || idx >= len(events) {
		return nil, idx
	}
	group := make([]SubagentEvent, 0, 4)
	end := idx - 1
	for i := idx; i < len(events); i++ {
		ev := events[i]
		if !isCompactExplorationTool(ev) {
			break
		}
		callID := strings.TrimSpace(ev.CallID)
		if opts.ToolPanelExpanded != nil && opts.ToolPanelExpanded(callID) {
			break
		}
		group = append(group, ev)
		end = i
	}
	return group, end
}

func isCompactExplorationTool(ev SubagentEvent) bool {
	if ev.Kind != SEToolCall || !ev.Done {
		return false
	}
	if ev.DisableGrouping {
		return false
	}
	if strings.TrimSpace(ev.CallID) == "" {
		return false
	}
	return shouldDefaultCollapseToolPanel(ev.Name)
}

func explorationGroupDetailRows(events []SubagentEvent, width int) []string {
	grouped := map[string][]string{}
	order := make([]string, 0, 4)
	for _, ev := range events {
		verb := explorationToolVerb(ev.Name)
		if verb == "" {
			continue
		}
		if _, ok := grouped[verb]; !ok {
			order = append(order, verb)
		}
		item := explorationToolDetail(ev)
		if item != "" {
			grouped[verb] = append(grouped[verb], item)
		}
	}
	if len(order) == 0 {
		return nil
	}
	rows := make([]string, 0, len(order))
	for i, verb := range order {
		detail := strings.Join(grouped[verb], ", ")
		if strings.TrimSpace(verb+" "+detail) == "" {
			continue
		}
		prefix := "  "
		if i == 0 {
			prefix += "└ "
		} else {
			prefix += "  "
		}
		rows = append(rows, wrapExplorationSummaryDetail(prefix, verb, detail, width)...)
	}
	return rows
}

func explorationToolDetail(ev SubagentEvent) string {
	item := strings.TrimSpace(ev.Args)
	fromArgs := item != ""
	if item == "" {
		item = strings.TrimSpace(ev.Output)
	}
	fromOutput := !fromArgs && item != ""
	if item == "" {
		item = strings.ToUpper(strings.TrimSpace(ev.Name))
	}
	item = normalizeExplorationFailedDetail(item)
	if ev.Err && item != "" && !fromOutput && !hasExplorationFailedStatus(item) {
		item = strings.TrimSpace(item + " failed")
	}
	return item
}

func normalizeExplorationFailedDetail(detail string) string {
	trimmed := strings.TrimSpace(detail)
	lower := strings.ToLower(trimmed)
	if lower == "failed failed" {
		return "failed"
	}
	const duplicateSuffix = " failed failed"
	if strings.HasSuffix(lower, duplicateSuffix) {
		return strings.TrimSpace(trimmed[:len(trimmed)-len(duplicateSuffix)] + " failed")
	}
	return trimmed
}

func hasExplorationFailedStatus(detail string) bool {
	_, ok := splitExplorationFailedStatus(detail)
	return ok
}

func wrapExplorationSummaryDetail(prefix string, verb string, detail string, width int) []string {
	verb = strings.TrimSpace(verb)
	detail = strings.Join(strings.Fields(strings.TrimSpace(detail)), " ")
	if verb == "" {
		if detail == "" {
			return nil
		}
		available := maxInt(8, width-displayColumns(prefix))
		segments := wrapToolOutputText(detail, available)
		rows := make([]string, 0, len(segments))
		for i, segment := range segments {
			linePrefix := prefix
			if i > 0 {
				linePrefix = strings.Repeat(" ", displayColumns(prefix))
			}
			rows = append(rows, linePrefix+segment)
		}
		return rows
	}
	if detail == "" {
		return []string{prefix + verb}
	}
	continuation := strings.Repeat(" ", displayColumns(prefix)+displayColumns(verb)+1)
	available := maxInt(8, width-displayColumns(continuation))
	segments := wrapToolOutputText(detail, available)
	if len(segments) == 0 {
		return []string{prefix + verb}
	}
	rows := make([]string, 0, len(segments))
	rows = append(rows, prefix+verb+" "+segments[0])
	for _, segment := range segments[1:] {
		rows = append(rows, continuation+segment)
	}
	return rows
}

func styleExplorationSummaryRow(row string, ctx BlockRenderContext) string {
	plainPrefix := ""
	content := row
	if strings.HasPrefix(row, "  └ ") {
		plainPrefix = "  └ "
		content = strings.TrimPrefix(row, plainPrefix)
	} else if strings.HasPrefix(row, "    ") {
		plainPrefix = "    "
		content = strings.TrimPrefix(row, plainPrefix)
	} else if strings.HasPrefix(row, "  ") {
		plainPrefix = "  "
		content = strings.TrimPrefix(row, plainPrefix)
	}
	verb, detail, _ := strings.Cut(strings.TrimSpace(content), " ")
	styled := ctx.Theme.TranscriptMetaStyle().Render(plainPrefix)
	if verb != "" && !isExplorationSummaryVerb(verb) {
		return styled + ctx.Theme.SecondaryTextStyle().Render(content)
	}
	if verb != "" {
		styled += toolActionStyle(ctx, verb).Render(verb)
	}
	if detail != "" {
		styled += " " + styleExplorationDetail(detail, ctx)
	}
	return styled
}

func styleExplorationDetail(detail string, ctx BlockRenderContext) string {
	if !containsExplorationFailedWord(detail) {
		return ctx.Theme.SecondaryTextStyle().Render(detail)
	}
	var styled strings.Builder
	remaining := detail
	for len(remaining) > 0 {
		idx := nextExplorationFailedWordIndex(remaining)
		if idx < 0 {
			styled.WriteString(ctx.Theme.SecondaryTextStyle().Render(remaining))
			break
		}
		if idx > 0 {
			styled.WriteString(ctx.Theme.SecondaryTextStyle().Render(remaining[:idx]))
		}
		styled.WriteString(ctx.Theme.ToolErrorStyle().Render(remaining[idx : idx+len("failed")]))
		remaining = remaining[idx+len("failed"):]
	}
	return styled.String()
}

func splitExplorationFailedStatus(detail string) (string, bool) {
	trimmed := strings.TrimSpace(detail)
	lower := strings.ToLower(trimmed)
	if lower == "failed" {
		return "", true
	}
	const suffix = " failed"
	if strings.HasSuffix(lower, suffix) {
		return strings.TrimSpace(trimmed[:len(trimmed)-len(suffix)]), true
	}
	return "", false
}

func containsExplorationFailedWord(detail string) bool {
	return nextExplorationFailedWordIndex(detail) >= 0
}

func nextExplorationFailedWordIndex(detail string) int {
	lower := strings.ToLower(detail)
	const marker = "failed"
	for offset := 0; offset < len(lower); {
		idx := strings.Index(lower[offset:], marker)
		if idx < 0 {
			return -1
		}
		idx += offset
		beforeOK := idx == 0 || !isASCIIAlphaNum(lower[idx-1])
		after := idx + len(marker)
		afterOK := after >= len(lower) || !isASCIIAlphaNum(lower[after])
		if beforeOK && afterOK {
			return idx
		}
		offset = idx + len(marker)
	}
	return -1
}

func isASCIIAlphaNum(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
}

func isExplorationSummaryVerb(verb string) bool {
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "read", "list", "glob", "search":
		return true
	default:
		return false
	}
}

func explorationToolVerb(name string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ":
		return "Read"
	case "LIST":
		return "List"
	case "GLOB":
		return "Glob"
	case "RG", "SEARCH", "FIND":
		return "Search"
	default:
		return ""
	}
}

func explorationGroupClickToken(events []SubagentEvent) string {
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		if callID := strings.TrimSpace(ev.CallID); callID != "" {
			ids = append(ids, callID)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	return "acp_exploration_group:" + strings.Join(ids, ",")
}

func pluralizeUnit(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	switch unit {
	case "entry":
		return strconv.Itoa(n) + " entries"
	case "match":
		return strconv.Itoa(n) + " matches"
	case "search":
		return strconv.Itoa(n) + " searches"
	}
	return strconv.Itoa(n) + " " + unit + "s"
}

func renderACPTranscriptLines(blockID string, events []SubagentEvent, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []string {
	rows := renderACPTranscriptRows(blockID, events, status, width, ctx, opts)
	if len(rows) == 0 {
		return nil
	}
	lines := make([]string, 0, len(rows))
	for _, row := range rows {
		lines = append(lines, row.Styled)
	}
	return lines
}

func renderACPPlanRows(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext) []RenderedRow {
	if len(ev.PlanEntries) == 0 {
		return nil
	}
	rows := make([]RenderedRow, 0, len(ev.PlanEntries)+3)
	rows = append(rows, PlainRow(blockID, ""))
	header := "• Updated Plan"
	rows = append(rows, renderACPTranscriptHeaderRow(blockID, header, width, ctx, ""))
	for i, pe := range ev.PlanEntries {
		icon := "□"
		iconStyle := ctx.Theme.TranscriptMetaStyle()
		textStyle := ctx.Theme.TextStyle()
		switch strings.ToLower(pe.Status) {
		case "done", "completed":
			icon = "✔"
			iconStyle = ctx.Theme.NoteStyle()
			textStyle = ctx.Theme.NoteStyle()
		case "in_progress", "running":
			icon = "▸"
			iconStyle = lipgloss.NewStyle().Foreground(ctx.Theme.Focus).Bold(true)
			textStyle = lipgloss.NewStyle().Foreground(ctx.Theme.Focus).Bold(true)
		case "failed":
			icon = "✗"
			iconStyle = ctx.Theme.ErrorStyle()
			textStyle = ctx.Theme.ErrorStyle()
		case "blocked":
			icon = "⊘"
			iconStyle = ctx.Theme.WarnStyle()
			textStyle = ctx.Theme.WarnStyle()
		}
		prefix := "  "
		if i == 0 {
			prefix += "└ "
		} else {
			prefix += "  "
		}
		content := strings.TrimSpace(pe.Content)
		plain := prefix + icon + " " + content
		styled := ctx.Theme.TranscriptMetaStyle().Render(prefix) + iconStyle.Render(icon) + " " + textStyle.Render(content)
		rows = append(rows, StyledPlainRow(blockID, plain, styled))
	}
	rows = append(rows, PlainRow(blockID, ""))
	return rows
}

func renderACPToolLifecycleRows(blockID string, events []SubagentEvent, idx int, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int) {
	if idx < 0 || idx >= len(events) {
		return nil, idx
	}
	ev := events[idx]
	if ev.Kind != SEToolCall {
		return nil, idx
	}
	callID := strings.TrimSpace(ev.CallID)
	if callID == "" {
		if !shouldRenderToolEvent(ev) {
			return nil, idx
		}
		return renderParticipantTurnToolRows(blockID, ev, width, ctx), idx
	}

	end := idx
	for end+1 < len(events) {
		next := events[end+1]
		if next.Kind != SEToolCall || strings.TrimSpace(next.CallID) != callID {
			break
		}
		end++
	}

	group := events[idx : end+1]
	start := group[0]
	singleCompletedLifecycle := len(group) == 1 && start.Done && strings.TrimSpace(start.Args) != ""
	if start.Done && len(group) > 1 {
		start = SubagentEvent{}
		for _, item := range group {
			if !item.Done {
				start = item
				break
			}
		}
		if start.Kind == 0 && start.CallID == "" && start.Name == "" {
			start = group[0]
		}
	}

	var final SubagentEvent
	var preview string
	hasStart := (!start.Done || singleCompletedLifecycle) && strings.TrimSpace(start.Name) != ""
	hasFinal := false
	for _, item := range group {
		if !item.Done {
			if text := strings.TrimSpace(item.Output); text != "" {
				preview = text
			}
			continue
		}
		if !shouldRenderToolEvent(item) {
			continue
		}
		final = item
		hasFinal = true
	}
	if singleCompletedLifecycle {
		final = start
		hasFinal = shouldRenderToolEvent(final)
		start.Done = false
		start.Output = ""
	}

	if !hasStart {
		if hasFinal {
			return renderACPStandaloneFinalToolRows(blockID, final, width, ctx, opts), end
		}
		if shouldRenderToolEvent(ev) {
			return renderParticipantTurnToolRows(blockID, ev, width, ctx), end
		}
		return nil, end
	}

	if isTerminalPanelToolEvent(start) {
		start.Args = normalizeACPToolInline(start.Args)
	} else {
		start.Args = compactACPToolInline(start.Args, width)
	}
	panelExpanded := true
	if opts.ToolPanelExpanded != nil {
		panelExpanded = opts.ToolPanelExpanded(start.CallID)
	}
	fullOutput := false
	if opts.ToolPanelFullOutput != nil {
		fullOutput = opts.ToolPanelFullOutput(start.CallID)
	}
	rows := renderParticipantTurnToolRows(blockID, start, width, ctx)
	if opts.ToolOutputPanels {
		if isSubagentTaskWriteEvent(events, idx) {
			panelText, panelErr := acpToolPanelText(preview, final, hasFinal)
			return renderACPStandardToolLifecycleRows(blockID, toolLifecycleHeaderEvent(start, final, hasFinal), callID, panelText, width, ctx, panelErr, hasFinal, fullOutput), end
		}
		if start.DisableGrouping {
			panelText, panelErr := acpToolPanelText(preview, final, hasFinal)
			return renderACPStandardToolLifecycleRows(blockID, toolLifecycleHeaderEvent(start, final, hasFinal), callID, panelText, width, ctx, panelErr, hasFinal, fullOutput), end
		}
		if isTerminalPanelToolEvent(start) {
			panelText, panelErr := acpToolPanelText(preview, final, hasFinal)
			return renderACPTerminalLifecycleRows(blockID, toolLifecycleHeaderEvent(start, final, hasFinal), callID, panelText, width, ctx, panelErr, panelExpanded, hasFinal, fullOutput, opts), end
		}
		if isMutationPanelToolEvent(start) {
			panelText, panelErr := acpToolPanelText(preview, final, hasFinal)
			return renderACPMutationLifecycleRows(blockID, toolLifecycleHeaderEvent(start, final, hasFinal), callID, panelText, width, ctx, panelErr, panelExpanded, opts), end
		}
		if hasFinal && shouldDefaultCollapseToolEvent(final) && !panelExpanded {
			return renderParticipantTurnToolRows(blockID, final, width, ctx), end
		}
		rows = renderACPToolHeaderRows(blockID, start, width, ctx, panelExpanded)
		if !panelExpanded {
			return rows, end
		}
		panelText, panelErr := acpToolPanelText(preview, final, hasFinal)
		if shouldRenderACPToolPanel(panelText, panelErr) {
			rows = append(rows, renderACPToolPanelRows(blockID, callID, finalPanelToolName(start, final, hasFinal), panelText, width, ctx, panelErr, opts)...)
		}
		return rows, end
	}
	if text := strings.TrimSpace(preview); text != "" {
		rows = append(rows, renderACPToolDetailRows(blockID, "· ", text, width, ctx.Theme.HelpHintTextStyle())...)
	}
	if hasFinal {
		prefix := "✓ "
		style := ctx.Theme.HelpHintTextStyle()
		if final.Err {
			prefix = "✗ "
			style = ctx.Theme.ErrorStyle()
		}
		text := strings.TrimSpace(final.Output)
		if text == "" && !final.Err {
			text = "completed"
		}
		if text != "" {
			rows = append(rows, renderACPToolDetailRows(blockID, prefix, text, width, style)...)
		}
	}
	return rows, end
}

func renderACPStandaloneFinalToolRows(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []RenderedRow {
	output := strings.TrimSpace(ev.Output)
	if opts.ToolOutputPanels && isTaskWritePanelEvent(ev) {
		fullOutput := false
		if opts.ToolPanelFullOutput != nil {
			fullOutput = opts.ToolPanelFullOutput(ev.CallID)
		}
		return renderACPStandardToolLifecycleRows(blockID, ev, ev.CallID, output, width, ctx, ev.Err, true, fullOutput)
	}
	if opts.ToolOutputPanels && ev.DisableGrouping {
		fullOutput := false
		if opts.ToolPanelFullOutput != nil {
			fullOutput = opts.ToolPanelFullOutput(ev.CallID)
		}
		return renderACPStandardToolLifecycleRows(blockID, ev, ev.CallID, output, width, ctx, ev.Err, true, fullOutput)
	}
	if opts.ToolOutputPanels && shouldRenderACPToolPanel(output, ev.Err) {
		panelExpanded := true
		if opts.ToolPanelExpanded != nil {
			panelExpanded = opts.ToolPanelExpanded(ev.CallID)
		}
		fullOutput := false
		if opts.ToolPanelFullOutput != nil {
			fullOutput = opts.ToolPanelFullOutput(ev.CallID)
		}
		if isTerminalPanelToolEvent(ev) {
			return renderACPTerminalLifecycleRows(blockID, ev, ev.CallID, output, width, ctx, ev.Err, panelExpanded, true, fullOutput, opts)
		}
		if isMutationPanelToolEvent(ev) {
			return renderACPMutationLifecycleRows(blockID, ev, ev.CallID, output, width, ctx, ev.Err, panelExpanded, opts)
		}
		if shouldDefaultCollapseToolEvent(ev) && !panelExpanded {
			return renderParticipantTurnToolRows(blockID, ev, width, ctx)
		}
		rows := renderACPToolHeaderRows(blockID, ev, width, ctx, panelExpanded)
		if !panelExpanded {
			return rows
		}
		rows = append(rows, renderACPToolPanelRows(blockID, ev.CallID, ev.Name, output, width, ctx, ev.Err, opts)...)
		return rows
	}
	if output == "" || (!strings.Contains(output, "\n") && displayColumns(output) <= maxInt(24, width/2)) {
		return renderParticipantTurnToolRows(blockID, ev, width, ctx)
	}
	header := SubagentEvent{
		Kind: SEToolCall,
		Name: ev.Name,
		Done: true,
		Err:  ev.Err,
	}
	rows := renderParticipantTurnToolRows(blockID, header, width, ctx)
	prefix := "✓ "
	style := ctx.Theme.HelpHintTextStyle()
	if ev.Err {
		prefix = "✗ "
		style = ctx.Theme.ErrorStyle()
	}
	rows = append(rows, renderACPToolDetailRows(blockID, prefix, output, width, style)...)
	return rows
}

func acpToolPanelText(preview string, final SubagentEvent, hasFinal bool) (string, bool) {
	panelText := strings.TrimSpace(preview)
	panelErr := false
	if hasFinal {
		panelText = strings.TrimSpace(final.Output)
		panelErr = final.Err
		if panelText == "" && !panelErr {
			panelText = "completed"
		}
	}
	return panelText, panelErr
}

func toolLifecycleHeaderEvent(start SubagentEvent, final SubagentEvent, hasFinal bool) SubagentEvent {
	header := start
	if hasFinal {
		if name := strings.TrimSpace(final.Name); name != "" {
			header.Name = name
		}
		if toolKind := strings.TrimSpace(final.ToolKind); toolKind != "" {
			header.ToolKind = toolKind
		}
		if taskID := strings.TrimSpace(final.TaskID); taskID != "" {
			header.TaskID = taskID
		}
		if action := strings.TrimSpace(final.TaskAction); action != "" {
			header.TaskAction = action
		}
		if input := strings.TrimSpace(final.TaskInput); input != "" {
			header.TaskInput = input
		}
		if targetKind := strings.TrimSpace(final.TaskTargetKind); targetKind != "" {
			header.TaskTargetKind = targetKind
		}
		if args := strings.TrimSpace(final.Args); args != "" {
			if isTerminalPanelToolEvent(header) {
				header.Args = normalizeACPToolInline(args)
			} else {
				header.Args = compactACPToolInline(args, acpToolInlineArgsMaxWidth+12)
			}
		}
	}
	return header
}

func shouldRenderACPToolPanel(text string, err bool) bool {
	text = strings.TrimSpace(text)
	if text == "" {
		return err
	}
	if !err && strings.EqualFold(text, "completed") {
		return false
	}
	return true
}

func finalPanelToolName(start SubagentEvent, final SubagentEvent, hasFinal bool) string {
	if hasFinal && strings.TrimSpace(final.Name) != "" {
		return final.Name
	}
	return start.Name
}

func renderACPStandardToolLifecycleRows(blockID string, ev SubagentEvent, callID string, text string, width int, ctx BlockRenderContext, err bool, final bool, fullOutput bool) []RenderedRow {
	header := standardToolLifecycleHeader(ev, err)
	token := acpToolPanelClickToken(callID)
	rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
	if final && fullOutput {
		rows = append(rows, renderACPFullTerminalPanelRows(blockID, callID, text, width, ctx, err, token)...)
		return rows
	}
	text = summarizeACPToolPanelText(text, final)
	if strings.TrimSpace(text) == "" {
		if !final || err {
			return rows
		}
		text = "completed"
	}
	rows = append(rows, renderACPTerminalPanelRows(blockID, callID, text, width, ctx, err, token)...)
	return rows
}

func standardToolLifecycleHeader(ev SubagentEvent, err bool) string {
	semanticName := toolSemanticName(ev.Name, ev.ToolKind)
	switch strings.ToUpper(strings.TrimSpace(semanticName)) {
	case "BASH", "SPAWN":
		ev.Name = semanticName
		return terminalLifecycleHeader(ev)
	case "TASK":
		if taskEventAction(ev) == "write" {
			return taskWriteLifecycleHeader(ev, err)
		}
		return standardVerbLifecycleHeader("Task", ev.Args, err)
	case "WRITE", "PATCH":
		ev.Name = semanticName
		return mutationLifecycleHeader(ev, err)
	case "READ":
		return standardVerbLifecycleHeader("Read", ev.Args, err)
	case "LIST":
		return standardVerbLifecycleHeader("List", ev.Args, err)
	case "GLOB":
		return standardVerbLifecycleHeader("Glob", ev.Args, err)
	case "SEARCH", "RG", "FIND":
		return standardVerbLifecycleHeader("Search", ev.Args, err)
	default:
		return standardVerbLifecycleHeader(firstTrimmed(ev.Name, ev.ToolKind, "Tool"), ev.Args, err)
	}
}

func taskWriteLifecycleHeader(ev SubagentEvent, err bool) string {
	handle := taskHandleDisplay(ev.TaskID)
	input := normalizeTaskWriteDisplayInput(ev.TaskInput)
	if input == "" {
		_, detail := splitTaskAction(ev.Args)
		if before, after, ok := strings.Cut(detail, ":"); ok && taskHandleDisplay(before) != "" {
			handle = firstNonEmpty(handle, taskHandleDisplay(before))
			input = normalizeTaskWriteDisplayInput(after)
		} else {
			input = normalizeTaskWriteDisplayInput(detail)
		}
	}
	args := ""
	switch {
	case handle != "" && input != "":
		args = handle + ": " + input
	case handle != "":
		args = handle
	case input != "":
		args = input
	}
	return standardVerbLifecycleHeader("Write", args, err)
}

func standardVerbLifecycleHeader(verb string, args string, err bool) string {
	verb = strings.TrimSpace(verb)
	if verb == "" {
		verb = "Tool"
	}
	args = strings.TrimSpace(args)
	if err {
		if args != "" {
			return "• " + verb + " failed " + args
		}
		return "• " + verb + " failed"
	}
	if args != "" {
		return "• " + verb + " " + args
	}
	return "• " + verb
}

func renderACPToolPanelRows(blockID string, callID string, toolName string, text string, width int, ctx BlockRenderContext, err bool, opts acpTranscriptRenderOptions) []RenderedRow {
	request := toolPanelRenderRequest{
		BlockID:  blockID,
		CallID:   callID,
		ToolName: toolName,
		Text:     text,
		Width:    width,
		Ctx:      ctx,
		Err:      err,
	}
	if opts.ToolPanelRows != nil {
		return opts.ToolPanelRows(request)
	}
	return request.renderUncached()
}

func (r toolPanelRenderRequest) renderUncached() []RenderedRow {
	blockID := r.BlockID
	callID := r.CallID
	toolName := r.ToolName
	text := r.Text
	width := r.Width
	ctx := r.Ctx
	err := r.Err
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if isDiffPanelText(text) && !err {
		return renderACPDiffPanelRows(blockID, text, width, ctx)
	}
	if isTerminalPanelTool(toolName) {
		return renderACPTerminalPanelRows(blockID, callID, text, width, ctx, err, "")
	}
	boxWidth := maxInt(20, width)
	bodyWidth := maxInt(1, boxWidth-6)
	body := renderACPToolPanelBody(text, bodyWidth, ctx, err)
	if len(body) == 0 {
		return nil
	}
	lines := renderPanelViewModel(ctx.Theme, PanelViewModel{
		Variant: tuikit.PanelShellVariantDrawer,
		Width:   boxWidth,
		Body:    body,
	})
	rows := make([]RenderedRow, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, StyledPlainRow(blockID, ansi.Strip(line), line))
	}
	return rows
}

func isTerminalPanelTool(name string) bool {
	return isTerminalPanelToolKind(name, "")
}

func isTerminalPanelToolKind(name string, kind string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "BASH", "SPAWN":
		return true
	case "TASK":
		return false
	}
	return strings.EqualFold(strings.TrimSpace(kind), "execute")
}

func isTerminalPanelToolEvent(ev SubagentEvent) bool {
	return isTerminalPanelToolKind(ev.Name, ev.ToolKind)
}

func isMutationPanelTool(name string) bool {
	return isMutationPanelToolKind(name, "")
}

func isMutationPanelToolKind(name string, kind string) bool {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "WRITE", "PATCH":
		return true
	}
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "edit", "delete", "move":
		return true
	default:
		return false
	}
}

func isMutationPanelToolEvent(ev SubagentEvent) bool {
	return isMutationPanelToolKind(ev.Name, ev.ToolKind)
}

func toolSemanticName(name string, kind string) string {
	return displaypolicy.SemanticToolName(name, kind)
}

func isAttentionLoopTool(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" || name == "TASK" {
		return false
	}
	return !shouldDefaultCollapseToolPanel(name)
}

func renderACPTerminalLifecycleRows(blockID string, ev SubagentEvent, callID string, text string, width int, ctx BlockRenderContext, err bool, expanded bool, final bool, fullOutput bool, opts acpTranscriptRenderOptions) []RenderedRow {
	headerEvent := ev
	if fullOutput && strings.EqualFold(strings.TrimSpace(ev.Name), "SPAWN") {
		if fullArgs := strings.TrimSpace(ev.FullArgs); fullArgs != "" {
			headerEvent.Args = fullArgs
		}
	}
	header := terminalLifecycleHeader(headerEvent)
	token := acpToolPanelClickToken(callID)
	rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
	if strings.TrimSpace(text) == "" && !final && strings.EqualFold(strings.TrimSpace(ev.Name), "SPAWN") {
		text = "(wait subagent output)"
	}
	if !expanded || !shouldRenderACPToolPanel(text, err) {
		return rows
	}
	if final && fullOutput {
		rows = append(rows, renderACPFullTerminalPanelRows(blockID, callID, text, width, ctx, err, token)...)
		return rows
	}
	text = summarizeACPToolPanelText(text, final)
	rows = append(rows, renderACPToolPanelRows(blockID, callID, toolSemanticName(ev.Name, ev.ToolKind), text, width, ctx, err, opts)...)
	return rows
}

func terminalLifecycleHeader(ev SubagentEvent) string {
	rawName := firstTrimmed(ev.Name, "TOOL")
	name := strings.ToUpper(strings.TrimSpace(rawName))
	args := strings.TrimSpace(ev.Args)
	switch name {
	case "BASH":
		if args != "" {
			return "• Ran " + args
		}
		return "• Ran bash"
	case "SPAWN":
		args = displaypolicy.SanitizeSpawnHeaderArgs(args)
		if args != "" {
			return "• Spawned " + args
		}
		return "• Spawned"
	default:
		if isExecuteToolKind(ev.ToolKind) {
			if command := executeToolCommandDisplay(rawName, args); command != "" {
				return "• Ran " + command
			}
			return "• Ran bash"
		}
		if args != "" {
			return "• " + rawName + " " + args
		}
		return "• " + rawName
	}
}

func sanitizeSpawnHeaderArgs(args string) string {
	return displaypolicy.SanitizeSpawnHeaderArgs(args)
}

func isExecuteToolKind(kind string) bool {
	return strings.EqualFold(strings.TrimSpace(kind), "execute")
}

func executeToolCommandDisplay(rawName string, args string) string {
	rawName = strings.TrimSpace(rawName)
	args = strings.TrimSpace(args)
	if args == "" {
		return rawName
	}
	if shouldPrefixExecuteToolName(rawName, args) {
		return strings.TrimSpace(rawName + " " + args)
	}
	return args
}

func shouldPrefixExecuteToolName(rawName string, args string) bool {
	rawName = strings.TrimSpace(rawName)
	if rawName == "" {
		return false
	}
	if strings.ContainsAny(rawName, " \t\n\r") {
		return false
	}
	switch strings.ToLower(rawName) {
	case "bash", "sh", "zsh", "fish", "execute", "tool":
		return false
	}
	first := firstShellExecutableToken(args)
	return first == "" || !strings.EqualFold(first, rawName)
}

func firstShellExecutableToken(command string) string {
	for _, token := range shellCommandTokens(command) {
		if token.Class == shellTokenCommand {
			return strings.Trim(token.Text, `"'`)
		}
	}
	return ""
}

func renderACPMutationLifecycleRows(blockID string, ev SubagentEvent, callID string, text string, width int, ctx BlockRenderContext, err bool, expanded bool, opts acpTranscriptRenderOptions) []RenderedRow {
	header := mutationLifecycleHeader(ev, err)
	token := acpToolPanelClickToken(callID)
	rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
	if err {
		if msg := strings.TrimSpace(text); msg != "" && msg != strings.TrimSpace(ev.Args) {
			rows = append(rows, renderACPToolDetailRows(blockID, "  └ ", msg, width, ctx.Theme.ErrorStyle())...)
		}
		return rows
	}
	if !expanded || !shouldRenderACPToolPanel(text, err) {
		return rows
	}
	if mutationPanelTextIsHeaderOnly(ev, text) {
		return rows
	}
	rows = append(rows, renderACPToolPanelRows(blockID, callID, ev.Name, text, width, ctx, err, opts)...)
	return rows
}

func mutationPanelTextIsHeaderOnly(ev SubagentEvent, text string) bool {
	text = strings.TrimSpace(text)
	if text == "" || strings.Contains(text, "\n") {
		return false
	}
	return strings.EqualFold(text, strings.TrimSpace(ev.Args))
}

func mutationLifecycleHeader(ev SubagentEvent, err bool) string {
	name := strings.ToUpper(strings.TrimSpace(ev.Name))
	args := strings.TrimSpace(ev.Args)
	if args == "" {
		args = strings.ToLower(name)
	}
	switch name {
	case "WRITE":
		if err {
			return "• Write failed " + args
		}
		return "• Wrote " + args
	case "PATCH":
		if err {
			return "• Patch failed " + args
		}
		return "• Patched " + args
	default:
		return "• " + name + " " + args
	}
}

func renderACPTranscriptHeaderRow(blockID string, plain string, width int, ctx BlockRenderContext, token string) RenderedRow {
	styled := styleACPTranscriptHeader(ctx, plain)
	if token != "" {
		return StyledPlainClickableRow(blockID, plain, styled, token)
	}
	return StyledPlainRow(blockID, plain, styled)
}

func styleACPTranscriptHeader(ctx BlockRenderContext, plain string) string {
	trimmed := strings.TrimSpace(plain)
	if !strings.HasPrefix(trimmed, "• ") {
		return ctx.Theme.ToolStyle().Render(plain)
	}
	rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "•"))
	verb, detail, _ := strings.Cut(rest, " ")
	styled := ctx.Theme.ToolStyle().Bold(true).Render("•")
	if verb != "" {
		styled += " " + toolActionStyle(ctx, verb).Render(verb)
	}
	if detail != "" {
		styled += " " + styleACPTranscriptHeaderDetail(ctx, verb, detail)
	}
	return styled
}

func styleACPTranscriptHeaderDetail(ctx BlockRenderContext, verb string, detail string) string {
	if strings.EqualFold(strings.TrimSpace(verb), "Ran") {
		return styleShellCommandText(ctx, detail)
	}
	if strings.EqualFold(strings.TrimSpace(verb), "Spawned") {
		return styleSpawnedHeaderDetail(ctx, detail)
	}
	return ctx.Theme.ToolArgsStyle().Render(detail)
}

func styleSpawnedHeaderDetail(ctx BlockRenderContext, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	target := detail
	rest := ""
	if before, after, ok := strings.Cut(detail, ":"); ok {
		target = strings.TrimSpace(before)
		rest = ":" + after
	} else if before, after, ok := strings.Cut(detail, " "); ok {
		target = strings.TrimSpace(before)
		rest = " " + after
	}
	if target == "" {
		return ctx.Theme.ToolArgsStyle().Render(detail)
	}
	return lipgloss.NewStyle().Foreground(ctx.Theme.Focus).Bold(true).Render(target) +
		ctx.Theme.ToolArgsStyle().Render(rest)
}

type shellTokenClass int

const (
	shellTokenSpace shellTokenClass = iota
	shellTokenCommand
	shellTokenEnv
	shellTokenFlag
	shellTokenOperator
	shellTokenArg
	shellTokenQuoted
)

type shellCommandToken struct {
	Text  string
	Class shellTokenClass
}

func styleShellCommandText(ctx BlockRenderContext, text string) string {
	tokens := shellCommandTokens(text)
	if len(tokens) == 0 {
		return ctx.Theme.ToolArgsStyle().Render(text)
	}
	var styled strings.Builder
	for _, token := range tokens {
		if token.Text == "" {
			continue
		}
		if token.Class == shellTokenSpace {
			styled.WriteString(token.Text)
			continue
		}
		style := shellTokenStyle(ctx, token.Class)
		styled.WriteString(style.Render(tuikit.LinkifyText(token.Text, ctx.Theme.LinkStyle())))
	}
	return styled.String()
}

func shellTokenStyle(ctx BlockRenderContext, class shellTokenClass) lipgloss.Style {
	switch class {
	case shellTokenCommand:
		return ctx.Theme.ToolNameStyle()
	case shellTokenEnv:
		return ctx.Theme.NoteStyle()
	case shellTokenFlag:
		return ctx.Theme.TranscriptMetaStyle()
	case shellTokenOperator:
		return ctx.Theme.ToolStyle()
	case shellTokenQuoted:
		return ctx.Theme.ToolOutputStyle()
	default:
		return ctx.Theme.ToolArgsStyle()
	}
}

func shellCommandTokens(text string) []shellCommandToken {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if text == "" {
		return nil
	}
	tokens := make([]shellCommandToken, 0, len(strings.Fields(text))*2+1)
	expectCommand := true
	for i := 0; i < len(text); {
		if isShellWhitespace(text[i]) {
			start := i
			for i < len(text) && isShellWhitespace(text[i]) {
				i++
			}
			tokens = append(tokens, shellCommandToken{Text: text[start:i], Class: shellTokenSpace})
			continue
		}
		if op, n := shellOperatorAt(text[i:]); n > 0 {
			tokens = append(tokens, shellCommandToken{Text: op, Class: shellTokenOperator})
			i += n
			if shellOperatorStartsCommand(op) {
				expectCommand = true
			}
			continue
		}
		start := i
		quoted := false
		if text[i] == '\'' || text[i] == '"' || text[i] == '`' {
			quoted = true
			quote := text[i]
			i++
			for i < len(text) {
				if text[i] == '\\' && i+1 < len(text) {
					i += 2
					continue
				}
				i++
				if text[i-1] == quote {
					break
				}
			}
		} else {
			for i < len(text) && !isShellWhitespace(text[i]) {
				if _, n := shellOperatorAt(text[i:]); n > 0 {
					break
				}
				i++
			}
		}
		raw := text[start:i]
		class := classifyShellToken(raw, quoted, expectCommand)
		if class == shellTokenCommand {
			expectCommand = false
		}
		if class != shellTokenEnv && class != shellTokenSpace && expectCommand {
			expectCommand = false
		}
		tokens = append(tokens, shellCommandToken{Text: raw, Class: class})
	}
	return tokens
}

func classifyShellToken(token string, quoted bool, expectCommand bool) shellTokenClass {
	if quoted {
		return shellTokenQuoted
	}
	if isShellEnvAssignment(token) && expectCommand {
		return shellTokenEnv
	}
	if expectCommand {
		return shellTokenCommand
	}
	if strings.HasPrefix(token, "-") {
		return shellTokenFlag
	}
	return shellTokenArg
}

func isShellWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\n'
}

func shellOperatorAt(text string) (string, int) {
	for _, op := range []string{"&&", "||", ">>", "<<", "2>>", "2>", "|", ";", ">", "<", "(", ")"} {
		if strings.HasPrefix(text, op) {
			return op, len(op)
		}
	}
	return "", 0
}

func shellOperatorStartsCommand(op string) bool {
	switch op {
	case "&&", "||", "|", ";", "(":
		return true
	default:
		return false
	}
}

func isShellEnvAssignment(token string) bool {
	idx := strings.IndexByte(token, '=')
	if idx <= 0 {
		return false
	}
	for i := 0; i < idx; i++ {
		ch := token[i]
		if i == 0 {
			if !isShellEnvNameStart(ch) {
				return false
			}
			continue
		}
		if !isShellEnvNameChar(ch) {
			return false
		}
	}
	return true
}

func isShellEnvNameStart(ch byte) bool {
	return ch == '_' || ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z'
}

func isShellEnvNameChar(ch byte) bool {
	return isShellEnvNameStart(ch) || ch >= '0' && ch <= '9'
}

func toolActionStyle(ctx BlockRenderContext, action string) lipgloss.Style {
	style := lipgloss.NewStyle().Bold(true)
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "ran", "spawned", "task", "tasks", "wait", "write", "cancel", "explored", "read", "list", "glob", "search":
		return style.Foreground(ctx.Theme.Focus)
	case "wrote", "patched", "updated":
		return style.Foreground(ctx.Theme.Success)
	case "failed", "interrupted":
		return style.Foreground(ctx.Theme.Error)
	default:
		return ctx.Theme.ToolNameStyle()
	}
}

func renderACPTerminalPanelRows(blockID string, callID string, text string, width int, ctx BlockRenderContext, err bool, token string) []RenderedRow {
	bodyWidth := maxInt(1, width)
	lines := renderACPTerminalPanelBody(text, bodyWidth, ctx, err)
	rows := make([]RenderedRow, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, StyledPlainClickablePreWrappedRow(blockID, ansi.Strip(line), line, token))
	}
	return rows
}

func renderACPFullTerminalPanelRows(blockID string, callID string, text string, width int, ctx BlockRenderContext, err bool, token string) []RenderedRow {
	bodyWidth := maxInt(1, width)
	lines := renderACPFullTerminalPanelBody(text, bodyWidth, ctx, err)
	rows := make([]RenderedRow, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, StyledPlainClickablePreWrappedRow(blockID, ansi.Strip(line), line, token))
	}
	return rows
}

func renderACPFullTerminalPanelBody(text string, width int, ctx BlockRenderContext, err bool) []string {
	style := ctx.Theme.ToolOutputStyle()
	if err {
		style = ctx.Theme.ToolErrorStyle()
	}
	lines := nonEmptyToolOutputLines(text)
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		prefix := "    "
		if len(out) == 0 {
			prefix = "  └ "
		}
		bodyWidth := maxInt(1, width-displayColumns(prefix))
		segments := strings.Split(hardWrapDisplayLine(raw, bodyWidth), "\n")
		for i, segment := range segments {
			linePrefix := prefix
			if i > 0 {
				linePrefix = strings.Repeat(" ", displayColumns(prefix))
			}
			out = append(out, styleTerminalOutputLine(ctx, linePrefix, segment, style))
		}
	}
	return out
}

func renderACPTerminalPanelBody(text string, width int, ctx BlockRenderContext, err bool) []string {
	style := ctx.Theme.ToolOutputStyle()
	if err {
		style = ctx.Theme.ToolErrorStyle()
	}
	segments := tailWrappedTerminalSegments(text, width, acpTerminalPanelMaxLines)
	lines := make([]string, 0, len(segments))
	for i, segment := range segments {
		prefix := "    "
		if i == 0 {
			prefix = "  └ "
		}
		lines = append(lines, styleTerminalOutputLine(ctx, prefix, segment, style))
	}
	return lines
}

func summarizeACPToolPanelText(text string, final bool) string {
	lines := nonEmptyToolOutputLines(text)
	if len(lines) == 0 {
		return ""
	}
	if !final {
		if len(lines) > acpTerminalPanelMaxLines {
			lines = lines[len(lines)-acpTerminalPanelMaxLines:]
		}
		return strings.Join(lines, "\n")
	}
	if len(lines) <= acpTerminalPanelMaxLines {
		return strings.Join(lines, "\n")
	}
	hidden := len(lines) - 4
	out := make([]string, 0, acpTerminalPanelMaxLines)
	out = append(out, lines[0], lines[1])
	out = append(out, fmt.Sprintf("... +%d lines", hidden))
	out = append(out, lines[len(lines)-2], lines[len(lines)-1])
	return strings.Join(out, "\n")
}

func nonEmptyToolOutputLines(text string) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	parts := strings.Split(text, "\n")
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		lines = append(lines, part)
	}
	return lines
}

func tailWrappedTerminalSegments(text string, width int, limit int) []string {
	return tailWrappedTerminalSegmentsFromEnd(text, width, limit)
}

func tailWrappedTerminalSegmentsFromEnd(text string, width int, limit int) []string {
	if limit <= 0 {
		return nil
	}
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	parts := strings.Split(text, "\n")
	reversed := make([]string, 0, minInt(len(parts), limit))
	bodyWidth := maxInt(1, width-displayColumns("  └ "))
	for i := len(parts) - 1; i >= 0 && len(reversed) < limit; i-- {
		part := parts[i]
		if strings.TrimSpace(part) == "" {
			continue
		}
		wrapped := strings.Split(hardWrapDisplayLine(part, bodyWidth), "\n")
		for j := len(wrapped) - 1; j >= 0 && len(reversed) < limit; j-- {
			segment := wrapped[j]
			if strings.TrimSpace(segment) == "" {
				continue
			}
			reversed = append(reversed, segment)
		}
	}
	segments := make([]string, len(reversed))
	for i := range reversed {
		segments[len(reversed)-1-i] = reversed[i]
	}
	return segments
}

func styleTerminalOutputLine(ctx BlockRenderContext, prefix string, segment string, style lipgloss.Style) string {
	if strings.Contains(prefix, "└") {
		before, after, _ := strings.Cut(prefix, "└")
		return ctx.Theme.TranscriptMetaStyle().Render(before) +
			ctx.Theme.ToolStyle().Render("└") +
			ctx.Theme.TranscriptMetaStyle().Render(after) +
			style.Render(tuikit.LinkifyText(segment, ctx.Theme.LinkStyle()))
	}
	return ctx.Theme.TranscriptMetaStyle().Render(prefix) +
		style.Render(tuikit.LinkifyText(segment, ctx.Theme.LinkStyle()))
}

func isDiffPanelText(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.EqualFold(strings.TrimSpace(line), "diff / hunk") {
			return true
		}
	}
	return false
}

func renderACPDiffPanelRows(blockID string, text string, width int, ctx BlockRenderContext) []RenderedRow {
	bodyWidth := maxInt(1, width-2)
	lines := renderACPToolPanelBody(text, bodyWidth, ctx, false)
	rows := make([]RenderedRow, 0, len(lines))
	for _, line := range lines {
		rows = append(rows, StyledPlainRow(blockID, ansi.Strip(line), line))
	}
	return rows
}

func renderACPToolHeaderRows(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext, expanded bool) []RenderedRow {
	vm := buildToolEventViewModel(ev)
	vm.Done = false
	vm.Err = false
	vm.Output = ""
	vm.Expandable = true
	vm.Expanded = expanded
	vm.ClickToken = acpToolPanelClickToken(ev.CallID)
	return renderToolEventViewModelLines(blockID, vm, width, ctx.Theme)
}

func acpToolPanelClickToken(callID string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return ""
	}
	return "acp_tool_panel:" + callID
}

func acpReasoningClickToken(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return "acp_reasoning:" + key
}

func acpToolPanelScrollToken(callID string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return ""
	}
	return "acp_tool_panel_scroll:" + callID
}

func terminalToolPanelLineCount(events []SubagentEvent, callID string, ctx BlockRenderContext) int {
	toolName, text, err, ok := terminalToolPanelPayload(events, callID)
	if !ok || !shouldRenderACPToolPanel(text, err) || !isTerminalPanelTool(toolName) {
		return 0
	}
	return len(renderACPTerminalPanelBody(text, maxInt(1, ctx.Width-2), ctx, err))
}

func terminalToolPanelPayload(events []SubagentEvent, callID string) (toolName string, text string, err bool, ok bool) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return "", "", false, false
	}
	var start SubagentEvent
	var final SubagentEvent
	var preview string
	hasStart := false
	hasFinal := false
	for _, item := range events {
		if item.Kind != SEToolCall || strings.TrimSpace(item.CallID) != callID {
			continue
		}
		if !item.Done {
			if !hasStart {
				start = item
				hasStart = true
			}
			if out := strings.TrimSpace(item.Output); out != "" {
				preview = out
			}
			continue
		}
		if !hasStart {
			start = item
			hasStart = true
		}
		if shouldRenderToolEvent(item) {
			final = item
			hasFinal = true
		}
	}
	if !hasStart && !hasFinal {
		return "", "", false, false
	}
	toolName = toolSemanticName(finalPanelToolName(start, final, hasFinal), firstNonEmpty(final.ToolKind, start.ToolKind))
	text = strings.TrimSpace(preview)
	err = false
	if hasFinal {
		text = strings.TrimSpace(final.Output)
		err = final.Err
		if text == "" && !err {
			text = "completed"
		}
	}
	return toolName, text, err, true
}

func renderACPToolPanelBody(text string, width int, ctx BlockRenderContext, err bool) []string {
	prefix := "  "
	lines := make([]string, 0, 8)
	for _, raw := range strings.Split(text, "\n") {
		style := toolPanelLineStyle(raw, ctx, err)
		linePrefix := prefix
		if err {
			linePrefix = "! "
		}
		if raw == "" {
			lines = append(lines, style.Width(width).Render(linePrefix))
			continue
		}
		wrapped := strings.Split(hardWrapDisplayLine(raw, maxInt(1, width-displayColumns(linePrefix))), "\n")
		for i, segment := range wrapped {
			if i > 0 {
				linePrefix = strings.Repeat(" ", displayColumns(linePrefix))
			}
			styled := style.Width(width).Render(linePrefix + tuikit.LinkifyText(segment, ctx.Theme.LinkStyle()))
			lines = append(lines, styled)
		}
	}
	return lines
}

func toolPanelLineStyle(raw string, ctx BlockRenderContext, err bool) lipgloss.Style {
	if err {
		return ctx.Theme.ToolErrorStyle()
	}
	trimmed := strings.TrimSpace(raw)
	switch {
	case strings.HasPrefix(trimmed, "+++"), strings.HasPrefix(trimmed, "---"):
		return ctx.Theme.DiffHeaderStyle()
	case strings.HasPrefix(trimmed, "@@"):
		return ctx.Theme.DiffHunkStyle()
	case strings.HasPrefix(trimmed, "+"):
		return ctx.Theme.DiffAddStyle().Background(ctx.Theme.DiffAddBg)
	case strings.HasPrefix(trimmed, "-"):
		return ctx.Theme.DiffRemoveStyle().Background(ctx.Theme.DiffRemoveBg)
	case strings.EqualFold(trimmed, "diff / hunk"):
		return ctx.Theme.TranscriptMetaStyle()
	default:
		return ctx.Theme.ToolOutputStyle()
	}
}

func renderACPToolDetailRows(blockID string, prefix string, text string, width int, style lipgloss.Style) []RenderedRow {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	prefix = strings.TrimRight(prefix, " ") + " "
	available := maxInt(1, width-displayColumns(prefix))
	segments := wrapACPToolDetailText(text, available)
	rows := make([]RenderedRow, 0, len(segments))
	for i, segment := range segments {
		linePrefix := prefix
		if i > 0 {
			linePrefix = strings.Repeat(" ", displayColumns(prefix))
		}
		plain := linePrefix + segment
		rows = append(rows, StyledPlainRow(blockID, plain, style.Width(width).Render(plain)))
	}
	return rows
}

func compactACPToolInline(text string, width int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" {
		return ""
	}
	budget := minInt(acpToolInlineArgsMaxWidth, maxInt(16, width-12))
	if displayColumns(text) <= budget {
		return text
	}
	return truncateTailDisplay(text, budget)
}

func normalizeACPToolInline(text string) string {
	return strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"))
}

func wrapACPToolDetailText(text string, width int) []string {
	if width <= 0 {
		return []string{text}
	}
	lines := strings.Split(text, "\n")
	segments := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		wrapped := wrapToolOutputText(line, width)
		if len(wrapped) == 0 {
			wrapped = []string{line}
		}
		segments = append(segments, wrapped...)
	}
	if len(segments) == 0 {
		return []string{text}
	}
	if len(segments) <= acpToolDetailPreviewBudget {
		return segments
	}
	truncated := append([]string(nil), segments[:acpToolDetailPreviewBudget-1]...)
	truncated = append(truncated, "… "+pluralizeMoreLines(len(segments)-acpToolDetailPreviewBudget+1))
	return truncated
}

func pluralizeMoreLines(n int) string {
	if n <= 1 {
		return "1 more line"
	}
	return strconv.Itoa(n) + " more lines"
}

func renderACPStatusRows(blockID string, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []RenderedRow {
	label := ""
	style := ctx.Theme.HelpHintTextStyle().Width(width)
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "waiting_approval":
		if opts.HideWaitingApprovalRow {
			return nil
		}
		label = "waiting approval"
		style = ctx.Theme.WarnStyle().Width(width)
	case "completed":
		if opts.HideCompletedRow {
			return nil
		}
		label = "✓ completed"
	case "running", "initializing", "prompting":
		return nil
	case "failed":
		label = "✗ failed"
		style = ctx.Theme.ErrorStyle().Width(width)
	case "interrupted":
		label = "⊘ interrupted"
		style = ctx.Theme.WarnStyle().Width(width)
	case "timed_out":
		label = "⌛ timed out"
		style = ctx.Theme.WarnStyle().Width(width)
	}
	if label == "" {
		return nil
	}
	return []RenderedRow{StyledPlainRow(blockID, label, style.Render(label))}
}

func participantTurnEmptyPlaceholder(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "running":
		return "  · waiting for agent output"
	case "initializing":
		return "  · initializing session"
	case "prompting":
		return ""
	case "waiting_approval":
		return "  · waiting approval"
	default:
		return ""
	}
}
