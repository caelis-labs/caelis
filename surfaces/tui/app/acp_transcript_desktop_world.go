package tuiapp

import "strings"

func desktopWorldHeader(ev SubagentEvent, width int) string {
	title := strings.Join(strings.Fields(sanitizeRenderableText(ev.Title)), " ")
	// The projector orders dispatch uncertainty and coverage ahead of values
	// such as app paths and search queries, which may take up the whole row.
	if displayColumns(title) <= width {
		return title
	}
	action, facts, _ := strings.Cut(title, " · ")
	first, rest, _ := strings.Cut(facts, " · ")
	if count, ok := strings.CutSuffix(first, " unknown dispatch"); ok {
		title = action + " · " + count + " unknown"
		if rest != "" {
			title += " · " + rest
		}
	}
	if displayColumns(title) <= width {
		return title
	}
	return strings.TrimRight(sliceByDisplayColumns(title, 0, width-1), " ·") + "…"
}

func renderDesktopWorldToolRows(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []RenderedRow {
	token := acpToolPanelClickTokenIf(ev.CallID, opts.ToolOutputPanels && toolPanelHasSourceOrOutput(ev))
	header := "• " + desktopWorldHeader(ev, maxInt(1, width-2))
	rows := []RenderedRow{renderACPToolHeaderRow(blockID, header, width, ctx, token, ev.Err, ev.Done)}
	if token == "" || opts.ToolPanelFullOutput == nil || !opts.ToolPanelFullOutput(ev.CallID) {
		return rows
	}
	if renderableTextHasContent(ev.FullArgs) {
		label := "Source"
		if strings.HasPrefix(ev.Title, "Apps") {
			label = "Filters"
		}
		rows = append(rows, renderACPToolOutputRowsWithToken(blockID, "  └ ", label+"\n"+ev.FullArgs, width, ctx, ctx.Theme.ToolArgsStyle(), token)...)
	}
	if !ev.OutputSynthetic && renderableTextHasContent(ev.Output) {
		label := "Result"
		style := ctx.Theme.ToolOutputStyle()
		if ev.Err {
			label = "Error details"
			style = ctx.Theme.ToolErrorStyle()
		}
		rows = append(rows, renderACPToolOutputRowsWithToken(blockID, "  └ ", label+"\n"+ev.Output, width, ctx, style, token)...)
	}
	return rows
}
