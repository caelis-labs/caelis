package tuiapp

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
	title := "• JS details"
	header := renderACPTranscriptHeaderRowMarked(blockID, title, width, ctx, toolDetailsClickToken(root.CallID), tone, dim)
	header.ClickStartCol = displayColumns("• JS ")
	header.ClickEndCol = displayColumns(title)
	header.ClickTokenAlt = foldToken
	rows := []RenderedRow{header}
	expanded := opts.ExplorationExpanded != nil && opts.ExplorationExpanded(key)
	if !expanded {
		for i := 0; i < len(children); {
			end := i + 1
			for end < len(children) && children[end].Name == children[i].Name && children[end].Err == children[i].Err && children[end].Done == children[i].Done {
				end++
			}
			display := append([]SubagentEvent(nil), children[i:end]...)
			for j := range display {
				display[j].Err = false
			}
			lines := toolGroupSummaryRowsWithPrefix(display, width, ctx.Workspace, explorationToolDetailLiveSummary, true, len(rows) == 1)
			for _, line := range lines {
				row := StyledPlainClickableRow(blockID, line, styleExplorationSummaryRow(line, ctx), foldToken)
				rows = append(rows, styleScriptToolState(row, children[i], ctx))
			}
			i = end
		}
		return rows, start, true
	}
	for _, event := range children {
		summary := event
		summary.Err = false
		for _, review := range events {
			if review.Kind == SEApproval && review.CallID == event.CallID {
				summary.ApprovalStatus = review.ApprovalStatus
			}
		}
		for _, row := range renderExplorationToolRowsWithMode(blockID, summary, width, ctx, toolDetailsClickToken(event.CallID), len(rows) == 1, explorationToolDetailLiveSummary) {
			rows = append(rows, styleScriptToolState(row, event, ctx))
		}
	}
	return rows, start, true
}
func styleScriptToolState(row RenderedRow, event SubagentEvent, ctx BlockRenderContext) RenderedRow {
	if event.Err {
		row.Styled = ctx.Theme.ErrorStyle().Render(row.Plain)
	} else if _, dim := acpToolHeaderMark(ctx, false, event.Done); dim {
		row.Styled = ctx.Theme.ToolStyle().Faint(true).Render(row.Styled)
	}
	return row
}
