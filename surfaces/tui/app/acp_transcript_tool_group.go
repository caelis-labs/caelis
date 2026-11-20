package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
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
	hasChildren := false
	for _, event := range events {
		if event.Kind == SEToolCall && parents[event.CallID] == root.CallID {
			// Task controls count as invocations even when their rows are hidden.
			hasChildren = true
			if !isTaskControlEvent(event) {
				children = append(children, event)
			}
		}
	}
	key := "tools:" + root.CallID
	foldToken := acpStableExplorationClickToken(key)
	tone, dim := acpToolHeaderMark(ctx, root.ScriptCompletion == "failed", root.Done)
	title := "• Running script"
	if root.Done {
		title = "• Ran script"
	}
	source := sanitizeRenderableText(root.FullArgs)
	preview := root.Args
	if root.Title != "" {
		preview = root.Title
	} else if source != "" {
		preview, _ = longCommandDisplayPreview(source, maxInt(1, compactSingleLineBudget(width)-displayColumns(title)-1))
	}
	sourceExpanded := opts.ToolPanelFullOutput != nil && opts.ToolPanelFullOutput(root.CallID)
	if sourceExpanded && source != "" {
		preview = source
		if root.Title != "" {
			preview = root.Title + "\n" + source
		}
	}
	headerText := title
	if preview != "" {
		headerText += " " + preview
	}
	if !sourceExpanded {
		if preview != "" {
			headerText = title + " " + compactSingleLineHeader(preview, maxInt(1, width-displayColumns(title)-1))
		} else {
			headerText = title
		}
		if width <= displayColumns(title)+1 {
			headerText = truncateDisplayCells(title, width)
		}
		headerText = truncateDisplayCells(headerText, width)
	}
	header := renderACPTranscriptHeaderRowMarked(blockID, headerText, width, ctx, foldToken, tone, dim)
	if !sourceExpanded && source != "" && root.Title == "" && width > displayColumns(title)+1 {
		initialBudget := maxInt(1, compactSingleLineBudget(width)-displayColumns(title)-1)
		finalBudget := maxInt(1, width-displayColumns(title)-1)
		header.Styled = styleACPTranscriptHeaderWithMark(ctx, title, tone, dim) + " " + styleScriptSourcePreview(ctx, source, initialBudget, finalBudget)
		header.PreWrapped = true
	}
	if source != "" || !hasChildren && root.Done && root.Output != "" && !root.OutputSynthetic {
		header.ClickTokenAlt = acpToolPanelClickToken(root.CallID)
		header.ClickStartCol = 2
		header.ClickEndCol = displayColumns(title)
	}
	rows := []RenderedRow{header}
	if !hasChildren && root.Done && !root.OutputSynthetic && renderableTextHasContent(root.Output) {
		token := acpToolPanelClickToken(root.CallID)
		output := root.Output
		if !sourceExpanded {
			output = summarizeACPToolPanelText(output, true, maxInt(1, width-displayColumns("  └ ")))
		}
		rows = append(rows, renderACPFullTerminalPanelRows(blockID, root.CallID, output, width, ctx, root.ScriptCompletion == "failed", token)...)
	}
	expanded := opts.ExplorationExpanded != nil && opts.ExplorationExpanded(key)
	for i := 0; i < len(children); {
		end := i + 1
		child := children[i]
		fullArgs := opts.ToolPanelFullOutput != nil && opts.ToolPanelFullOutput(child.CallID)
		if verb := scriptExplorationGroupVerb(child); !expanded && !fullArgs && verb != "" {
			for end < len(children) {
				next := children[end]
				if next.Name != child.Name || next.Err != child.Err || next.Done != child.Done ||
					scriptExplorationGroupVerb(next) != verb ||
					(opts.ToolPanelFullOutput != nil && opts.ToolPanelFullOutput(next.CallID)) {
					break
				}
				end++
			}
		}
		token := foldToken
		if end == i+1 && strings.TrimSpace(firstNonEmpty(child.FullArgs, child.Args)) != "" {
			token = acpToolPanelClickToken(child.CallID)
		}
		rows = append(rows, renderScriptInvocationRows(blockID, children[i:end], width, ctx, token, len(rows) == 1, fullArgs)...)
		i = end
	}
	return rows, start, true
}

func scriptExplorationGroupVerb(ev SubagentEvent) string {
	switch ev.Name {
	case surfaceToolRunCommand, surfaceToolSpawn, surfaceToolSendMessage:
		return ""
	}
	return explorationInvocationVerb(ev)
}

// Script calls share exploration columns, but fold their argument preview before
// layout so even long tool names occupy one physical row. Only a call click
// discloses its original arguments; the script header controls aggregation.
func renderScriptInvocationRows(blockID string, events []SubagentEvent, width int, ctx BlockRenderContext, token string, first, fullArgs bool) []RenderedRow {
	ev := events[0]
	prefix := truncateDisplayCells(explorationChildPrefix(first), maxInt(0, width-1))
	budget := maxInt(1, width-displayColumns(prefix))
	tag, styledTag := approvalReviewStatusTag(ctx, sanitizeRenderableText(ev.ApprovalStatus))
	if tagWidth := displayColumns(tag) + 1; tag != "" && tagWidth < budget {
		budget -= tagWidth
		tag, styledTag = " "+tag, " "+styledTag
	} else {
		tag, styledTag = "", ""
	}
	header := scriptInvocationHeader(events, ctx, budget, fullArgs)
	tone, dim := acpToolHeaderMark(ctx, ev.Err, ev.Done)
	var styled string
	if ev.Name == surfaceToolSendMessage && !fullArgs && strings.TrimSpace(firstNonEmpty(ev.StartArgs, ev.Args)) != "" {
		row := renderSendMessageHeaderRow(blockID, header, ctx, token, "", tone, dim)
		header = truncateDisplayCells(strings.TrimPrefix(row.Plain, "• "), budget)
		styled = row.Styled
	} else {
		styled = styleACPTranscriptHeaderWithMark(ctx, "• "+header, tone, dim)
	}
	styled = ctx.Theme.TranscriptMetaStyle().Render(prefix) + ansi.Cut(styled, 2, 2+displayColumns(header)) + styledTag
	line := prefix + header + tag
	if ev.Err {
		styled = ctx.Theme.ErrorStyle().Render(line)
	} else if dim {
		styled = ctx.Theme.ToolStyle().Faint(true).Render(styled)
	}
	rows := []RenderedRow{StyledPlainClickablePreWrappedRow(blockID, line, styled, token)}
	if fullArgs {
		args := firstNonEmpty(ev.FullArgs, ev.Args)
		details := renderACPToolOutputRowsWithToken(blockID, "", args, maxInt(1, width-4), ctx, ctx.Theme.ToolArgsStyle(), token)
		rows = append(rows, toolGroupChildRows(details, ctx, false)...)
	}
	return rows
}

func scriptInvocationHeader(events []SubagentEvent, ctx BlockRenderContext, budget int, fullArgs bool) string {
	ev := events[0]
	labelEvent := ev
	labelEvent.Args, labelEvent.DiffSummary = "", ""
	label := strings.TrimPrefix(standardToolLifecycleHeader(labelEvent, false), "• ")
	header := label
	if !fullArgs {
		if verb := surfaceExplorationVerb(ev.Name, ev.ToolKind, ev.ExplorationVerb); verb != "" && label == verb {
			var details []string
			for _, event := range events {
				// Only use invocation arguments in this compact disclosure.
				event.Err, event.Output = false, ""
				if detail := explorationToolDetailForDisplay(event, ctx.Workspace, explorationToolDetailLiveSummary); detail != "" {
					details = append(details, detail)
				}
			}
			header = strings.TrimSpace(label + " " + strings.Join(details, ", "))
		} else {
			ev.Args = firstNonEmpty(ev.StartArgs, ev.Args)
			if ev.Done && !ev.Err && isMutationPanelToolEvent(ev) && ev.DiffSummary == "" {
				summary := toolDisplayResultHeader(ev.Name, ev.Output)
				if _, _, _, ok := tuikit.SplitDiffCountTokens(summary); ok {
					ev.DiffSummary = summary
				}
			}
			if ev.Name == surfaceToolRunCommand {
				command := firstNonEmpty(ev.FullArgs, ev.Args)
				ev.Args, _ = longCommandDisplayPreview(command, maxInt(1, budget-displayColumns(label)-1))
			}
			header = strings.TrimPrefix(standardToolLifecycleHeader(ev, false), "• ")
		}
	}
	header = sanitizeRenderableText(header)
	if ev.Name != surfaceToolRunCommand {
		header = strings.Join(strings.Fields(header), " ")
	}
	if detail, ok := strings.CutPrefix(header, label+" "); ok && displayColumns(header) > budget && displayColumns(label) <= budget {
		header = label
		if remaining := budget - displayColumns(label) - 1; remaining > 0 {
			if path, add, remove, ok := tuikit.SplitDiffCountTokens(detail); label == "Edit" && ok && remaining > displayColumns(add+" "+remove)+1 {
				counts := add + " " + remove
				detail = compactSingleLineHeader(path, remaining-displayColumns(counts)-1) + " " + counts
			}
			header += " " + compactSingleLineHeader(detail, remaining)
		}
	}
	return truncateDisplayCells(compactSingleLineHeader(header, budget), budget)
}
