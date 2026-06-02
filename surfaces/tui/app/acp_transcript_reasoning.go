package tuiapp

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

func reasoningFoldKey(idx int) string {
	return strconv.Itoa(idx)
}

func reasoningExpanded(opts acpTranscriptRenderOptions, key string) bool {
	if opts.ReasoningExpanded == nil {
		return false
	}
	return opts.ReasoningExpanded(key)
}

const (
	acpLiveReasoningMinRows = 4
	acpLiveReasoningMaxRows = 8
)

func reasoningShouldFold(events []SubagentEvent, idx int, status string) bool {
	if idx < 0 || idx >= len(events) || events[idx].Kind != SEReasoning {
		return false
	}
	text, end := collectConsecutiveReasoning(events, idx)
	if strings.TrimSpace(text) == "" {
		return false
	}
	if liveTailHasPotentialDeferredCompactStage(events, idx, status) {
		return false
	}
	for i := end + 1; i < len(events); i++ {
		if reasoningFoldBoundaryEvent(events[i]) {
			return true
		}
	}
	return isTerminalACPTranscriptStatus(status)
}

func reasoningFoldBoundaryEvent(ev SubagentEvent) bool {
	switch ev.Kind {
	case SEReasoning:
		return strings.TrimSpace(ev.Text) != ""
	case SEAssistant:
		return strings.TrimSpace(ev.Text) != ""
	case SEToolCall:
		return strings.TrimSpace(ev.Name) != "" || strings.TrimSpace(ev.Args) != "" || strings.TrimSpace(ev.Output) != ""
	case SEPlan:
		return len(ev.PlanEntries) > 0
	case SEApproval:
		return strings.TrimSpace(ev.ApprovalTool) != "" || strings.TrimSpace(ev.ApprovalCommand) != ""
	default:
		return false
	}
}

func isTerminalACPTranscriptStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "interrupted", "cancelled", "canceled", "terminated", "timed_out":
		return true
	default:
		return false
	}
}

func liveTailHasPotentialDeferredCompactStage(events []SubagentEvent, idx int, status string) bool {
	if idx < 0 || idx >= len(events) || isTerminalACPTranscriptStatus(status) {
		return false
	}
	if stage, end := potentialTaskStage(events, idx, status); len(taskControlEvents(stage)) > 0 && hasTaskNarrative(stage) && shouldDeferLiveTailStageCompaction(events, end, status) {
		return true
	}
	if stage, end := potentialExplorationStage(events, idx, status); compactExplorationStageHasSummary(stage) && hasExplorationNarrative(stage) && shouldDeferLiveTailStageCompaction(events, end, status) {
		return true
	}
	return false
}

func shouldDeferLiveTailStageCompaction(events []SubagentEvent, end int, status string) bool {
	if end < 0 || end >= len(events) || isTerminalACPTranscriptStatus(status) {
		return false
	}
	return !hasLaterAssistantNarrative(events, end+1)
}

func hasLaterAssistantNarrative(events []SubagentEvent, start int) bool {
	for i := maxInt(0, start); i < len(events); i++ {
		ev := events[i]
		if (ev.Kind == SEReasoning || ev.Kind == SEAssistant) && strings.TrimSpace(ev.Text) != "" {
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

func renderACPReasoningNarrativeRows(blockID string, text string, width int, ctx BlockRenderContext, active bool) []RenderedRow {
	rows := renderParticipantTurnNarrativeRows(blockID, text, tuikit.LineStyleReasoning, width, ctx, active)
	if !active {
		return rows
	}
	return limitLiveReasoningRows(blockID, rows, ctx)
}

func renderACPReasoningSummaryRow(blockID string, ev SubagentEvent, idx int, width int, ctx BlockRenderContext, expanded bool) RenderedRow {
	marker := "›"
	preview := reasoningPreviewText(ev.Text, width)
	plain := strings.TrimSpace(marker + " " + preview)
	styled := ctx.Theme.ReasoningStyle().Render(marker)
	if preview != "" {
		styled += ctx.Theme.ReasoningStyle().Render(" " + preview)
	}
	return StyledPlainClickableRow(blockID, plain, styled, acpReasoningClickToken(reasoningFoldKey(idx)))
}

func limitLiveReasoningRows(blockID string, rows []RenderedRow, ctx BlockRenderContext) []RenderedRow {
	budget := liveReasoningRowBudget(ctx)
	if budget <= 0 || len(rows) <= budget {
		return rows
	}
	if budget == 1 {
		return []RenderedRow{liveReasoningOmittedRow(blockID, len(rows), ctx)}
	}
	tailRows := maxInt(1, budget-1)
	if tailRows >= len(rows) {
		return rows
	}
	omitted := len(rows) - tailRows
	marker := liveReasoningOmittedRow(blockID, omitted, ctx)
	out := make([]RenderedRow, 0, budget)
	out = append(out, marker)
	out = append(out, rows[len(rows)-tailRows:]...)
	return out
}

func liveReasoningRowBudget(ctx BlockRenderContext) int {
	if ctx.Height <= 0 {
		return 0
	}
	budget := minInt(acpLiveReasoningMaxRows, maxInt(acpLiveReasoningMinRows, ctx.Height/3))
	return minInt(ctx.Height, budget)
}

func hasHeightSensitiveLiveReasoning(events []SubagentEvent, status string) bool {
	if isTerminalACPTranscriptStatus(status) {
		return false
	}
	visible := visibleNarrativeEvents(events, status)
	for i := 0; i < len(visible); i++ {
		if visible[i].Kind != SEReasoning {
			continue
		}
		text, end := collectConsecutiveReasoning(visible, i)
		if strings.TrimSpace(text) == "" {
			i = end
			continue
		}
		if reasoningShouldFold(visible, i, status) {
			i = end
			continue
		}
		if participantNarrativeEventActive(visible, end, status) {
			return true
		}
		i = end
	}
	return false
}

func liveReasoningOmittedRow(blockID string, omitted int, ctx BlockRenderContext) RenderedRow {
	if omitted < 1 {
		omitted = 1
	}
	plain := fmt.Sprintf("› … %d earlier reasoning lines", omitted)
	styled := ctx.Theme.ReasoningStyle().Render("›") + ctx.Theme.HelpHintTextStyle().Render(fmt.Sprintf(" … %d earlier reasoning lines", omitted))
	return RenderedRow{
		Styled:     styled,
		Plain:      plain,
		BlockID:    blockID,
		PreWrapped: true,
	}
}

func reasoningPreviewText(text string, width int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if text == "" {
		return ""
	}
	budget := maxInt(12, width-displayColumns("› "))
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
	firstPlain := strings.TrimPrefix(rows[0].Plain, "› ")
	firstPlain = strings.TrimPrefix(firstPlain, "  ")
	plain := strings.TrimSpace("› " + firstPlain)
	styled := ctx.Theme.ReasoningStyle().Render("›")
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
