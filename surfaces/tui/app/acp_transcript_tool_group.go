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

func invocationToolChildren(events []SubagentEvent, parents map[string]string) map[string][]SubagentEvent {
	children := make(map[string][]SubagentEvent)
	for _, event := range events {
		if parent := parents[event.CallID]; event.Kind == SEToolCall && parent != "" {
			children[parent] = append(children[parent], event)
		}
	}
	return children
}

const scriptToolLabel = "js_exec"

func scriptExecutionFailed(root SubagentEvent, children []SubagentEvent) bool {
	// A parent's cumulative error can come from a child even after the
	// JavaScript itself finished successfully.
	return root.ScriptCompletion == "failed" || root.ScriptCompletion == "" && root.Err && len(children) == 0
}

func renderInvocationToolGroupRows(blockID string, root SubagentEvent, children []SubagentEvent, start, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	if !isScriptToolEvent(root) {
		return nil, start, false
	}
	key := "tools:" + root.CallID
	foldToken := acpStableExplorationClickToken(key)
	failed := scriptExecutionFailed(root, children)
	tone, dim := acpToolHeaderMark(ctx, failed, root.Done)
	title := "• " + scriptToolLabel
	source := sanitizeRenderableText(root.FullArgs)
	file := strings.Join(strings.Fields(sanitizeRenderableText(root.Title)), " ")
	sourceExpanded := opts.ToolPanelFullOutput != nil && opts.ToolPanelFullOutput(root.CallID)
	headerText, styled := title, ""
	switch {
	case sourceExpanded && source != "":
		headerText += " " + source
		if file != "" {
			headerText = title + " " + file + "\n" + source
		}
	case file != "":
		headerText += " " + compactSingleLineHeader(file, width-displayColumns(title)-1)
	case source != "" && width > displayColumns(title)+1:
		initialBudget := maxInt(1, compactSingleLineBudget(width)-displayColumns(title)-1)
		finalBudget := width - displayColumns(title) - 1
		preview := styleScriptSourcePreview(ctx, source, initialBudget, finalBudget)
		headerText += " " + ansi.Strip(preview)
		styled = styleACPTranscriptHeaderWithMark(ctx, title, tone, dim) + " " + preview
	case root.StartArgs != "":
		args := strings.Join(strings.Fields(sanitizeRenderableText(root.StartArgs)), " ")
		headerText += " " + compactSingleLineHeader(args, width-displayColumns(title)-1)
	}
	if !sourceExpanded {
		headerText = truncateDisplayCells(headerText, width)
	}
	header := renderACPTranscriptHeaderRowMarked(blockID, headerText, width, ctx, foldToken, tone, dim)
	if styled != "" {
		header.Styled = styled
		header.PreWrapped = true
	}
	showOutput := (failed || len(children) == 0) && root.Done && !root.OutputSynthetic && renderableTextHasContent(root.Output)
	if source != "" || showOutput {
		header.ClickTokenAlt = acpToolPanelClickToken(root.CallID)
		header.ClickStartCol = 2
		header.ClickEndCol = displayColumns(title)
	}
	rows := []RenderedRow{header}
	if showOutput {
		token := acpToolPanelClickToken(root.CallID)
		output := root.Output
		if !sourceExpanded {
			if failed {
				output = firstNonEmpty(structuredToolFailureMessage(output), output)
			}
			output = summarizeACPToolPanelText(output, true, maxInt(1, width-displayColumns("  └ ")))
		}
		rows = append(rows, renderACPFullTerminalPanelRows(blockID, root.CallID, output, width, ctx, failed, token)...)
	}
	expanded := opts.ExplorationExpanded != nil && opts.ExplorationExpanded(key)
	children = visibleScriptChildren(children)
	for i := 0; i < len(children); {
		end := i + 1
		child := children[i]
		if child.desktopWorld != nil && child.desktopWorld.visible(child.Err) {
			prefix := explorationChildPrefix(len(rows) == 1)
			rows = append(rows, renderDesktopWorldCallRows(blockID, child, prefix, width, ctx, opts)...)
			i++
			continue
		}
		fullArgs := opts.ToolPanelFullOutput != nil && opts.ToolPanelFullOutput(child.CallID)
		if verb := scriptExplorationGroupVerb(child); !expanded && !fullArgs && verb != "" {
			for end < len(children) {
				next := children[end]
				if next.desktopWorld != nil || next.Name != child.Name || next.Err != child.Err || next.Done != child.Done ||
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

func visibleScriptChildren(children []SubagentEvent) []SubagentEvent {
	var visible []SubagentEvent
	for _, child := range children {
		if !isTaskControlEvent(child) {
			visible = append(visible, child)
		}
	}
	return visible
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
