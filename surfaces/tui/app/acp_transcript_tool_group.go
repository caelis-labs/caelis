package tuiapp

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// invocationToolParents is a disposable projection of direct canonical ancestry.
// Narratives and unrelated tools never become members of a script disclosure.
func invocationToolParents(events []SubagentEvent) map[string]string {
	roots := map[string]int{}
	for i, ev := range events {
		if isScriptToolEvent(ev) && ev.CallID != "" {
			roots[ev.CallID] = i
		}
	}
	parents := map[string]string{}
	for i, ev := range events {
		if root, ok := roots[ev.ParentToolCallID]; ok && root < i && ev.ParentToolName == "js_exec" {
			parents[ev.CallID] = ev.ParentToolCallID
		}
	}
	return parents
}

func renderInvocationToolGroupRows(blockID string, events []SubagentEvent, start, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	root := events[start]
	if !isScriptToolEvent(root) {
		return nil, start, false
	}
	parents := invocationToolParents(events)
	var children []SubagentEvent
	for _, event := range events {
		if event.Kind == SEToolCall && parents[event.CallID] == root.CallID {
			children = append(children, event)
		}
	}
	key := "tools:" + root.CallID
	foldToken := acpStableExplorationClickToken(key)
	tone, dim := acpToolHeaderMark(ctx, root.ScriptCompletion == "failed", root.Done)
	title := "• Running script"
	if root.Done {
		title = "• Ran script"
	}
	header := renderACPTranscriptHeaderRowMarked(blockID, title, width, ctx, foldToken, tone, dim)
	rows := []RenderedRow{header}
	expanded := opts.ExplorationExpanded != nil && opts.ExplorationExpanded(key)
	for i := 0; i < len(children); {
		end := i + 1
		if !expanded && (opts.ToolPanelFullOutput == nil || !opts.ToolPanelFullOutput(children[i].CallID)) {
			for end < len(children) && children[end].Name == children[i].Name && children[end].Err == children[i].Err && children[end].Done == children[i].Done && (opts.ToolPanelFullOutput == nil || !opts.ToolPanelFullOutput(children[end].CallID)) {
				end++
			}
		}
		display := append([]SubagentEvent(nil), children[i:end]...)
		for j := range display {
			display[j].Err = false
			// Script disclosure never includes result or diagnostic bodies.
			display[j].Output = ""
			for _, review := range events {
				if review.Kind == SEApproval && review.CallID == display[j].CallID {
					display[j].ApprovalStatus = review.ApprovalStatus
				}
			}
		}
		token := foldToken
		fullArgs := false
		if len(display) == 1 && strings.TrimSpace(firstNonEmpty(display[0].FullArgs, display[0].Args)) != "" {
			token = acpToolPanelClickToken(display[0].CallID)
			fullArgs = opts.ToolPanelFullOutput != nil && opts.ToolPanelFullOutput(display[0].CallID)
		}
		for _, row := range renderScriptInvocationRows(blockID, display, width, ctx, token, len(rows) == 1, fullArgs) {
			rows = append(rows, styleScriptToolState(row, children[i], ctx))
		}
		i = end
	}
	return rows, start, true
}

// Script calls share exploration columns, but fold their argument preview before
// layout so even long tool names occupy one physical row. Only a call click
// discloses its original arguments; the script header controls aggregation.
func renderScriptInvocationRows(blockID string, events []SubagentEvent, width int, ctx BlockRenderContext, token string, first, fullArgs bool) []RenderedRow {
	ev := events[0]
	var details []string
	for _, event := range events {
		detail := explorationToolDetailForDisplay(event, ctx.Workspace, explorationToolDetailLiveSummary)
		if event.ApprovalStatus != "" {
			tag, _ := approvalReviewStatusTag(ctx, event.ApprovalStatus)
			detail = strings.TrimSpace(detail + " " + tag)
		}
		details = append(details, detail)
	}
	prefix := truncateDisplayCells(explorationChildPrefix(first), maxInt(0, width-1))
	header := ev.Name
	if !fullArgs {
		header = strings.TrimSpace(header + " " + strings.Join(details, ", "))
	} else if ev.ApprovalStatus != "" {
		tag, _ := approvalReviewStatusTag(ctx, ev.ApprovalStatus)
		header += " " + tag
	}
	budget := maxInt(1, width-displayColumns(prefix))
	header = strings.Join(strings.Fields(header), " ")
	if displayColumns(header) > budget && displayColumns(ev.Name) <= budget {
		detail := strings.TrimSpace(strings.TrimPrefix(header, ev.Name))
		header = ev.Name
		if remaining := budget - displayColumns(header) - 1; remaining > 0 {
			header += " " + compactSingleLineHeader(detail, remaining)
		}
	} else {
		header = compactSingleLineHeader(header, budget)
	}
	header = truncateDisplayCells(header, budget)
	line := prefix + header
	rows := []RenderedRow{StyledPlainClickablePreWrappedRow(blockID, line, styleExplorationSummaryRow(line, ctx), token)}
	if fullArgs {
		args := firstNonEmpty(ev.FullArgs, ev.Args)
		details := renderACPToolOutputRowsWithToken(blockID, "", args, maxInt(1, width-4), ctx, ctx.Theme.ToolArgsStyle(), token)
		rows = append(rows, toolGroupChildRows(details, ctx, false)...)
	}
	return rows
}

func styleScriptToolState(row RenderedRow, event SubagentEvent, ctx BlockRenderContext) RenderedRow {
	// Accent the tool name while retaining the styled argument suffix.
	content := strings.TrimLeft(row.Plain, " └")
	if content == event.Name || strings.HasPrefix(content, event.Name+" ") {
		prefix := row.Plain[:len(row.Plain)-len(content)]
		end := displayColumns(prefix) + displayColumns(event.Name)
		row.Styled = ctx.Theme.TranscriptMetaStyle().Render(prefix) + ctx.Theme.Tokens().Accent.Bold(true).Render(event.Name) + ansi.Cut(row.Styled, end, ansi.StringWidth(row.Styled))
	}
	if event.Err {
		row.Styled = ctx.Theme.ErrorStyle().Render(row.Plain)
	} else if _, dim := acpToolHeaderMark(ctx, false, event.Done); dim {
		row.Styled = ctx.Theme.ToolStyle().Faint(true).Render(row.Styled)
	}
	return row
}
