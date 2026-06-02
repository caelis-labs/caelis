package tuiapp

import (
	"fmt"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
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
	BlockID    string
	CallID     string
	ToolName   string
	Text       string
	Width      int
	Ctx        BlockRenderContext
	Err        bool
	ClickToken string
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
			if taskRows, consumed, ok := renderACPTaskStageRows(blockID, visible, i, status, width, ctx, opts); ok {
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupTask, false)
				rows = append(rows, taskRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupTask
				i = consumed
				continue
			}
			if explorationRows, consumed, ok := renderACPExplorationStageRows(blockID, visible, i, status, width, ctx, opts); ok {
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
					active := participantNarrativeEventActive(visible, reasoningEnd, status)
					rows = append(rows, renderACPReasoningNarrativeRows(blockID, text, width, ctx, active)...)
				} else {
					rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupNarrative, false)
					rows = append(rows, renderACPReasoningExpandedRows(blockID, text, i, width, ctx, participantNarrativeEventActive(visible, reasoningEnd, status))...)
				}
				hasContent = true
				lastGroup = acpTranscriptGroupNarrative
				i = reasoningEnd
			}
		case SEAssistant:
			if taskRows, consumed, ok := renderACPTaskStageRows(blockID, visible, i, status, width, ctx, opts); ok {
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupTask, false)
				rows = append(rows, taskRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupTask
				i = consumed
				continue
			}
			if explorationRows, consumed, ok := renderACPExplorationStageRows(blockID, visible, i, status, width, ctx, opts); ok {
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
			if taskRows, consumed, ok := renderACPTaskStageRows(blockID, visible, i, status, width, ctx, opts); ok {
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupTask, false)
				rows = append(rows, taskRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupTask
				i = consumed
				continue
			}
			if explorationRows, consumed, ok := renderACPExplorationStageRows(blockID, visible, i, status, width, ctx, opts); ok {
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupExploration, false)
				rows = append(rows, explorationRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupExploration
				i = consumed
				continue
			}
			if groupRows, consumed, ok := renderACPExplorationGroupRows(blockID, visible, i, status, width, ctx, opts); ok {
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
			approvalRows := renderACPApprovalReviewRows(blockID, ev, width, ctx)
			if len(approvalRows) > 0 {
				rows = appendACPTranscriptGroupGap(rows, blockID, lastGroup, acpTranscriptGroupApproval, false)
				rows = append(rows, approvalRows...)
				hasContent = true
				lastGroup = acpTranscriptGroupApproval
			}
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
	acpTranscriptGroupApproval
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

func renderACPTranscriptHeaderRow(blockID string, plain string, width int, ctx BlockRenderContext, token string) RenderedRow {
	plain = sanitizeRenderableText(plain)
	styled := styleACPTranscriptHeader(ctx, plain)
	row := StyledPlainRow(blockID, plain, styled)
	if token != "" {
		row.ClickToken = token
	}
	row.ACPHeader = true
	return row
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
	return styleSpawnedHeaderTarget(ctx, target) +
		ctx.Theme.ToolArgsStyle().Render(rest)
}

func styleSpawnedHeaderTarget(ctx BlockRenderContext, target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	handle := target
	annotation := ""
	if before, after, ok := strings.Cut(target, "["); ok && strings.HasSuffix(after, "]") {
		handle = strings.TrimSpace(before)
		annotation = "[" + after
	}
	if handle == "" {
		return ctx.Theme.SecondaryTextStyle().Render(target)
	}
	styled := lipgloss.NewStyle().Foreground(ctx.Theme.Focus).Bold(true).Render(handle)
	if annotation != "" {
		styled += ctx.Theme.SecondaryTextStyle().Render(annotation)
	}
	return styled
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
	text = sanitizeRenderableText(text)
	parts := splitRenderableLines(text)
	lines := make([]string, 0, len(parts))
	for _, part := range parts {
		if !renderableLineHasContent(part) {
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
	text = sanitizeRenderableText(text)
	parts := splitRenderableLines(text)
	reversed := make([]string, 0, minInt(len(parts), limit))
	bodyWidth := maxInt(1, width-displayColumns("  └ "))
	for i := len(parts) - 1; i >= 0 && len(reversed) < limit; i-- {
		part := parts[i]
		if !renderableLineHasContent(part) {
			continue
		}
		wrapped := strings.Split(hardWrapDisplayLine(part, bodyWidth), "\n")
		for j := len(wrapped) - 1; j >= 0 && len(reversed) < limit; j-- {
			segment := wrapped[j]
			if !renderableLineHasContent(segment) {
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
	return stylePrefixedContentLine(ctx, prefix, segment, maxInt(displayColumns(prefix)+displayColumns(segment), ctx.Width), style)
}

func isDiffPanelText(text string) bool {
	hasHunk := false
	hasChange := false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.EqualFold(trimmed, "diff / hunk") {
			return true
		}
		if isDiffHunkHeader(trimmed) {
			hasHunk = true
			continue
		}
		if strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "-") {
			hasChange = true
		}
	}
	return hasHunk && hasChange
}

func renderACPDiffPanelRows(blockID string, text string, width int, ctx BlockRenderContext) []RenderedRow {
	return renderNumberedACPDiffPanelRows(blockID, text, width, ctx)
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

func acpToolPanelClickTokenIf(callID string, enabled bool) string {
	if !enabled {
		return ""
	}
	return acpToolPanelClickToken(callID)
}

func toolPanelCanExpandHiddenDetails(ev SubagentEvent, outputText string, final bool, err bool) bool {
	if toolPanelEventHasHiddenToolArgs(ev) {
		return true
	}
	if final && shouldDefaultCollapseToolEvent(ev) && shouldRenderACPToolPanel(outputText, err) {
		return true
	}
	return final && finalToolOutputSummaryHidesLines(outputText)
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
			if out := item.Output; renderableTextHasContent(out) {
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
	text = preview
	err = false
	if hasFinal {
		text = final.Output
		err = final.Err
		if !renderableTextHasContent(text) && !err {
			text = "completed"
		}
	}
	return toolName, text, err, true
}

func renderACPToolPanelBody(text string, width int, ctx BlockRenderContext, err bool) []string {
	prefix := "  "
	lines := make([]string, 0, 8)
	for _, raw := range splitRenderableLines(sanitizeRenderableText(text)) {
		if !renderableLineHasContent(raw) {
			continue
		}
		style := toolPanelLineStyle(raw, ctx, err)
		linePrefix := prefix
		if err {
			linePrefix = "! "
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

func renderACPToolDetailRows(blockID string, prefix string, text string, width int, ctx BlockRenderContext, style lipgloss.Style) []RenderedRow {
	return renderACPToolDetailRowsWithToken(blockID, prefix, text, width, ctx, style, "")
}

func renderACPToolDetailRowsWithToken(blockID string, prefix string, text string, width int, ctx BlockRenderContext, style lipgloss.Style, token string) []RenderedRow {
	text = sanitizeRenderableText(text)
	if !renderableTextHasContent(text) {
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
		styled := stylePrefixedContentLine(ctx, linePrefix, segment, width, style)
		rows = append(rows, StyledPlainClickablePreWrappedRow(blockID, plain, styled, token))
	}
	return rows
}

func renderACPToolOutputRowsWithToken(blockID string, prefix string, text string, width int, ctx BlockRenderContext, style lipgloss.Style, token string) []RenderedRow {
	text = sanitizeRenderableText(text)
	if !renderableTextHasContent(text) {
		return nil
	}
	prefix = strings.TrimRight(prefix, " ") + " "
	available := maxInt(1, width-displayColumns(prefix))
	rawLines := splitRenderableLines(text)
	segments := make([]string, 0, len(rawLines))
	for _, raw := range rawLines {
		if !renderableLineHasContent(raw) {
			continue
		}
		wrapped := wrapToolOutputText(raw, available)
		if len(wrapped) == 0 {
			wrapped = []string{raw}
		}
		segments = append(segments, wrapped...)
	}
	rows := make([]RenderedRow, 0, len(segments))
	for i, segment := range segments {
		linePrefix := prefix
		if i > 0 {
			linePrefix = strings.Repeat(" ", displayColumns(prefix))
		}
		plain := linePrefix + segment
		styled := stylePrefixedContentLine(ctx, linePrefix, segment, width, style)
		rows = append(rows, StyledPlainClickablePreWrappedRow(blockID, plain, styled, token))
	}
	return rows
}

func sanitizeRenderableText(text string) string {
	return tuikit.SanitizeLogText(text)
}

func splitRenderableLines(text string) []string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	return strings.Split(text, "\n")
}

func renderableTextHasContent(text string) bool {
	for _, line := range splitRenderableLines(sanitizeRenderableText(text)) {
		if renderableLineHasContent(line) {
			return true
		}
	}
	return false
}

func renderableLineHasContent(line string) bool {
	return strings.TrimSpace(line) != ""
}

func stylePrefixedContentLine(ctx BlockRenderContext, prefix string, segment string, width int, contentStyle lipgloss.Style) string {
	segment = tuikit.SanitizeLogText(segment)
	prefixWidth := displayColumns(prefix)
	contentWidth := maxInt(1, width-prefixWidth)
	return styleRenderPrefix(ctx, prefix, contentStyle) +
		contentStyle.Width(contentWidth).Render(tuikit.LinkifyText(segment, ctx.Theme.LinkStyle()))
}

func styleRenderPrefix(ctx BlockRenderContext, prefix string, contentStyle lipgloss.Style) string {
	if prefix == "" {
		return ""
	}
	if strings.TrimSpace(prefix) == "" || containsStructuralGlyph(prefix) {
		return ctx.Theme.TranscriptMetaStyle().Render(prefix)
	}
	return contentStyle.Render(prefix)
}

func containsStructuralGlyph(text string) bool {
	return strings.ContainsAny(text, "└├│╭╰")
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
	lines := splitRenderableLines(text)
	segments := make([]string, 0, len(lines))
	for _, line := range lines {
		if !renderableLineHasContent(line) {
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

func renderACPApprovalReviewRows(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext) []RenderedRow {
	if strings.TrimSpace(ev.ApprovalText) == "" && strings.TrimSpace(ev.ApprovalStatus) == "" {
		return nil
	}
	display := approvalReviewDisplayParts(ev)
	if display.Status == "" && display.Rationale == "" {
		return nil
	}
	prefixPlain, prefixStyled := approvalReviewPrefix(display, ctx)
	if display.Rationale == "" {
		return []RenderedRow{StyledPlainRow(blockID, prefixPlain, prefixStyled)}
	}
	bodyPrefix := "  └ "
	bodyContinuation := "    "
	bodyWidth := maxInt(1, width-displayColumns(bodyPrefix))
	rationale := sanitizeRenderableText(display.Rationale)
	segments := wrapToolOutputText(rationale, bodyWidth)
	if len(segments) == 0 {
		segments = []string{rationale}
	}
	bodyStyle := ctx.Theme.TranscriptMetaStyle()
	rows := make([]RenderedRow, 0, len(segments)+1)
	rows = append(rows, RenderedRow{Styled: prefixStyled, Plain: prefixPlain, BlockID: blockID, PreWrapped: true})
	for i, segment := range segments {
		linePrefix := bodyContinuation
		if i == 0 {
			linePrefix = bodyPrefix
		}
		plain := linePrefix + segment
		styled := ctx.Theme.TranscriptMetaStyle().Render(linePrefix) + bodyStyle.Render(segment)
		rows = append(rows, StyledPlainRow(blockID, plain, styled))
	}
	return rows
}

type approvalReviewDisplay struct {
	Status        string
	Risk          string
	Authorization string
	Rationale     string
}

func approvalReviewDisplayParts(ev SubagentEvent) approvalReviewDisplay {
	text := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ev.ApprovalText), "⚠"))
	status := firstNonEmpty(strings.TrimSpace(ev.ApprovalStatus), approvalReviewStatusFromText(text), "reviewed")
	return approvalReviewDisplay{
		Status:        status,
		Risk:          firstNonEmpty(strings.TrimSpace(ev.ApprovalRisk), approvalReviewValueFromText(text, "risk")),
		Authorization: firstNonEmpty(strings.TrimSpace(ev.ApprovalAuth), approvalReviewValueFromText(text, "authorization")),
		Rationale:     approvalReviewRationaleFromText(text),
	}
}

func approvalReviewPrefix(display approvalReviewDisplay, ctx BlockRenderContext) (string, string) {
	status := strings.TrimSpace(display.Status)
	plain := "• Automatic approval review"
	styled := ctx.Theme.ToolStyle().Render("•") + " " + ctx.Theme.TranscriptMetaStyle().Render("Automatic approval review")
	if status != "" {
		plain += " " + status
		styled += " " + approvalReviewStatusStyle(ctx, status).Render(status)
	}
	meta := make([]string, 0, 2)
	if risk := strings.TrimSpace(display.Risk); risk != "" {
		meta = append(meta, "risk: "+risk)
	}
	if authorization := strings.TrimSpace(display.Authorization); authorization != "" {
		meta = append(meta, "authorization: "+authorization)
	}
	if len(meta) == 0 {
		return plain, styled
	}
	plain += " (" + strings.Join(meta, ", ") + ")"
	styled += ctx.Theme.TranscriptMetaStyle().Render(" (")
	if risk := strings.TrimSpace(display.Risk); risk != "" {
		styled += ctx.Theme.TranscriptMetaStyle().Render("risk: ") + approvalReviewValueStyle(ctx, risk).Render(risk)
		if strings.TrimSpace(display.Authorization) != "" {
			styled += ctx.Theme.TranscriptMetaStyle().Render(", ")
		}
	}
	if authorization := strings.TrimSpace(display.Authorization); authorization != "" {
		styled += ctx.Theme.TranscriptMetaStyle().Render("authorization: ") + approvalReviewValueStyle(ctx, authorization).Render(authorization)
	}
	styled += ctx.Theme.TranscriptMetaStyle().Render(")")
	return plain, styled
}

func approvalReviewStatusStyle(ctx BlockRenderContext, status string) lipgloss.Style {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "approved":
		return lipgloss.NewStyle().Foreground(ctx.Theme.Success).Bold(true)
	case "denied", "failed":
		return ctx.Theme.ErrorStyle().Bold(true)
	case "timed_out", "timed out", "needs_user", "needs user", "needs-user":
		return ctx.Theme.WarnStyle().Bold(true)
	default:
		return ctx.Theme.TranscriptLabelStyle()
	}
}

func approvalReviewValueStyle(ctx BlockRenderContext, value string) lipgloss.Style {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "low":
		return lipgloss.NewStyle().Foreground(ctx.Theme.Success)
	case "medium":
		return lipgloss.NewStyle().Foreground(ctx.Theme.Accent).Bold(true)
	case "high":
		return lipgloss.NewStyle().Foreground(ctx.Theme.Warning).Bold(true)
	case "critical":
		return ctx.Theme.ErrorStyle().Bold(true)
	default:
		return ctx.Theme.TranscriptMetaStyle()
	}
}

func approvalReviewStatusFromText(text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	for _, status := range []string{"approved", "denied", "failed", "timed_out"} {
		if strings.Contains(lower, "approval review "+status) {
			return status
		}
	}
	return ""
}

func approvalReviewValueFromText(text string, key string) string {
	key = strings.ToLower(strings.TrimSpace(key))
	if key == "" {
		return ""
	}
	lower := strings.ToLower(text)
	needle := key + ":"
	idx := strings.Index(lower, needle)
	if idx < 0 {
		return ""
	}
	valueStart := idx + len(needle)
	value := strings.TrimSpace(text[valueStart:])
	for _, sep := range []string{",", ")"} {
		if cut := strings.Index(value, sep); cut >= 0 {
			value = value[:cut]
		}
	}
	return strings.TrimSpace(value)
}

func approvalReviewRationaleFromText(text string) string {
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "⚠"))
	if text == "" {
		return ""
	}
	if before, after, ok := strings.Cut(text, "):"); ok && strings.Contains(strings.ToLower(before), "approval review") {
		return strings.TrimSpace(after)
	}
	if before, after, ok := strings.Cut(text, ":"); ok && strings.Contains(strings.ToLower(before), "approval review") {
		return strings.TrimSpace(after)
	}
	if strings.Contains(strings.ToLower(text), "approval review") {
		return ""
	}
	return text
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
