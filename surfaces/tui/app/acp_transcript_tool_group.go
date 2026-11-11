package tuiapp

import "strings"

// invocationToolParents is a disposable projection of direct canonical ancestry.
// Narratives and unrelated tools never become members of a script disclosure.
func invocationToolParents(events []SubagentEvent) map[string]string {
	roots := map[string]int{}
	for i, ev := range events {
		if isResultOnlyToolEvent(ev) && ev.CallID != "" {
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
	if !isResultOnlyToolEvent(root) {
		return nil, start, false
	}
	parents := invocationToolParents(events)
	var children []SubagentEvent
	for _, ev := range events {
		if ev.Kind == SEToolCall && parents[ev.CallID] == root.CallID {
			children = append(children, ev)
		}
	}
	key := "tools:" + root.CallID
	token := acpStableExplorationClickToken(key)
	expanded := opts.ExplorationExpanded != nil && opts.ExplorationExpanded(key)
	if len(children) == 0 {
		return renderACPToolResultOnlyRows(blockID, root, width, ctx, opts), start, true
	}
	tone, dim := acpToolHeaderMark(ctx, false, root.Done)
	rows := []RenderedRow{renderACPTranscriptHeaderRowMarked(blockID, "• Called tools", width, ctx, token, tone, dim)}
	if !expanded {
		// StartArgs remain the operation summary while results stream into Output.
		display := append([]SubagentEvent(nil), children...)
		for i := range display {
			display[i].Err = false
		}
		for _, line := range toolGroupDetailRowsWithWorkspaceMode(display, width, ctx.Workspace, explorationToolDetailLiveSummary, true) {
			rows = append(rows, StyledPlainClickableRow(blockID, line, styleExplorationSummaryRow(line, ctx), token))
		}
		return rows, start, true
	}
	for _, ev := range children {
		summary := ev
		summary.Err = false // Failure belongs to the stored result, not a label.
		resultToken := acpToolPanelClickTokenIf(ev.CallID, toolPanelCanExpandCollapsed(events, ev.CallID))
		for _, review := range events {
			if review.Kind == SEApproval && review.CallID == ev.CallID {
				summary.ApprovalStatus = review.ApprovalStatus
			}
		}
		toolRows := renderExplorationToolRowsWithMode(blockID, summary, width, ctx, resultToken, len(rows) == 1, explorationToolDetailLiveSummary)
		if ev.Err {
			for i := range toolRows {
				toolRows[i].Styled = ctx.Theme.ToolErrorStyle().Render(toolRows[i].Plain)
			}
		}
		rows = append(rows, toolRows...)
		if opts.ToolPanelExpanded != nil && opts.ToolPanelExpanded(ev.CallID) || opts.ToolPanelFullOutput != nil && opts.ToolPanelFullOutput(ev.CallID) {
			text := ev.Output
			for _, review := range events {
				if review.Kind == SEApproval && review.CallID == ev.CallID && review.ApprovalText != "" {
					text = review.ApprovalText
					ev.OutputSynthetic = false
				}
			}
			if !ev.OutputSynthetic {
				rows = append(rows, renderInvocationOutputRows(blockID, ev, text, width, ctx, opts, false)...)
			}
		}
	}
	if root.Done {
		rows = append(rows, renderInvocationOutputRows(blockID, root, root.Output, width, ctx, opts, false)...)
	}
	return rows, start, true
}

func renderInvocationOutputRows(blockID string, ev SubagentEvent, text string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions, first bool) []RenderedRow {
	if ev.OutputSynthetic || strings.TrimSpace(text) == "null" || !renderableTextHasContent(text) {
		return nil
	}
	token := acpToolPanelClickToken(ev.CallID)
	full := opts.ToolPanelFullOutput != nil && opts.ToolPanelFullOutput(ev.CallID)
	if !full {
		text = summarizeACPToolPanelText(text, true, maxInt(1, width-4))
	}
	style := ctx.Theme.ToolOutputStyle()
	if ev.Err {
		style = ctx.Theme.ToolErrorStyle()
	}
	return renderExplorationNarrativeRows(blockID, text, width, ctx, style, token, first)
}
