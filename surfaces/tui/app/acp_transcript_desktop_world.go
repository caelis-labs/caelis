package tuiapp

import (
	"slices"
	"strings"

	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
	"github.com/charmbracelet/x/ansi"
)

const desktopWorldSummaryColumns = 60
const desktopWorldVisibleCalls = 2

func computerActivityCalls(event SubagentEvent, children []SubagentEvent) ([]SubagentEvent, bool) {
	if event.Kind != SEToolCall {
		return nil, false
	}
	if event.desktopWorld != nil {
		return []SubagentEvent{event}, true
	}
	if !isScriptToolEvent(event) || len(children) == 0 || scriptExecutionFailed(event, children) {
		return nil, false
	}
	for _, child := range children {
		if child.desktopWorld == nil {
			return nil, false
		}
	}
	return children, true
}

func renderDesktopWorldGroupRows(blockID string, events []SubagentEvent, parents map[string]string, children map[string][]SubagentEvent, start, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	first := events[start]
	group, ok := computerActivityCalls(first, children[first.CallID])
	if !ok {
		return nil, start, false
	}
	group = slices.Clip(group)
	done := first.Done
	end, confirmed := start, len(group)
	firstReasoning := -1
	for i := start + 1; i < len(events); i++ {
		ev := events[i]
		switch {
		case ev.Kind == SEToolCall && parents[ev.CallID] != "",
			ev.Kind == SEApproval && approvalReviewHasTool(events, ev):
			continue
		case ev.Kind == SEReasoning:
			if firstReasoning < 0 {
				firstReasoning = i
			}
			group = append(group, ev)
			continue
		}
		additional, ok := computerActivityCalls(ev, children[ev.CallID])
		if !ok {
			break
		}
		group = append(group, additional...)
		done = done && ev.Done
		end, confirmed = i, len(group)
	}
	// Trailing reasoning stays standalone until another trusted DTW step
	// confirms it belongs between computer calls.
	group = group[:confirmed]

	visibleCalls, reasoning := 0, 0
	failed := false
	for _, ev := range group {
		if ev.Kind == SEReasoning {
			reasoning++
			continue
		}
		done = done && ev.Done
		failed = failed || ev.Err
		if ev.desktopWorld.visible(ev.Err) {
			visibleCalls++
		}
	}
	if visibleCalls == 0 {
		// An unrecognized action has no summary. Leave its surrounding
		// reasoning in the transcript if there is no group to disclose it.
		if reasoning != 0 {
			end = firstReasoning - 1
		}
		return nil, end, true
	}

	summaries := desktopWorldSummaryCalls(group)
	key := "computer:" + first.CallID
	token := ""
	if reasoning != 0 || visibleCalls > len(summaries) {
		token = acpStableExplorationClickToken(key)
	}
	expanded := opts.ExplorationExpanded != nil && opts.ExplorationExpanded(key)
	rows := []RenderedRow{renderACPToolHeaderRow(blockID, ansi.Truncate("• Using computer", width, "…"), width, ctx, token, failed, done)}
	for i, ev := range group {
		first := len(rows) == 1
		if ev.Kind == SEReasoning {
			if expanded {
				rows = append(rows, renderExplorationNarrativeRows(blockID, ev.Text, width, ctx, ctx.Theme.ReasoningStyle(), token, first)...)
			}
			continue
		}
		if !ev.desktopWorld.visible(ev.Err) {
			continue
		}
		prefix := explorationChildPrefix(first)
		switch {
		case expanded, slices.Contains(summaries, i), ev.Err && opts.ToolPanelFullOutput != nil && opts.ToolPanelFullOutput(ev.CallID):
			rows = append(rows, renderDesktopWorldCallRows(blockID, ev, prefix, width, ctx, opts, false)...)
		case ev.Err:
			rows = append(rows, renderDesktopWorldFailureRow(blockID, ev, prefix, width, ctx, opts))
		}
	}
	return rows, end, true
}

func desktopWorldSummaryCalls(group []SubagentEvent) []int {
	var operations, observations []int
	for i := len(group) - 1; i >= 0; i-- {
		ev := group[i]
		display := ev.desktopWorld
		if display == nil || ev.Err || !display.visible(false) {
			continue
		}
		if !display.observation() {
			operations = append(operations, i)
			if len(operations) == desktopWorldVisibleCalls {
				break
			}
		} else if len(operations) == 0 && len(observations) < desktopWorldVisibleCalls {
			duplicate := ev.Done && slices.ContainsFunc(observations, func(index int) bool {
				return group[index].Done && display.sameObservation(group[index].desktopWorld)
			})
			if !duplicate {
				observations = append(observations, i)
			}
		}
	}
	summaries := operations
	if len(summaries) == 0 {
		summaries = observations
	}
	// Failures outside the regular row budget still disclose a diagnostic.
	for i := len(group) - 1; i >= 0 && len(summaries) < desktopWorldVisibleCalls; i-- {
		if group[i].Err {
			summaries = append(summaries, i)
		}
	}
	return summaries
}

func renderDesktopWorldFailureRow(blockID string, ev SubagentEvent, prefix string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) RenderedRow {
	detail := ev.desktopWorld.failure
	if detail == "" {
		detail = ev.desktopWorld.label(maxInt(1, width-displayColumns(prefix)), true)
	}
	line := ansi.Truncate(prefix+detail, width, "…")
	return StyledPlainClickablePreWrappedRow(blockID, line, ctx.Theme.ErrorStyle().Render(line), acpToolPanelClickTokenIf(ev.CallID, opts.ToolOutputPanels && toolPanelHasSourceOrOutput(ev)))
}

func renderDesktopWorldCallRows(blockID string, ev SubagentEvent, prefix string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions, showToolName bool) []RenderedRow {
	budget := width
	if !showToolName {
		budget = minInt(budget, desktopWorldSummaryColumns)
	}
	prefix = ansi.Truncate(prefix, maxInt(0, budget-1), "")
	available := maxInt(1, budget-displayColumns(prefix))
	header := ev.desktopWorld.label(available, ev.Err)
	token := acpToolPanelClickTokenIf(ev.CallID, opts.ToolOutputPanels && toolPanelHasSourceOrOutput(ev))
	var rows []RenderedRow
	if showToolName {
		lead := ev.Name + " · "
		remaining := available - displayColumns(lead)
		action := ev.desktopWorld.label(maxInt(1, remaining), ev.Err)
		if remaining > 0 && action != "" && !strings.HasSuffix(action, "…") {
			header = lead + action
		} else {
			// A continuation leaves room for the action when the name uses most of the row.
			namePrefix := prefix
			if extra := displayColumns(ev.Name) - available; extra > 0 {
				padding := len(prefix) - len(strings.TrimLeft(prefix, " "))
				namePrefix = prefix[minInt(extra, padding):]
			}
			name := ansi.Truncate(ev.Name, budget-displayColumns(namePrefix), "…")
			rows = append(rows, renderDesktopWorldSummaryRow(blockID, ev, namePrefix, name, ctx, token))
			prefix = strings.Repeat(" ", displayColumns(prefix))
		}
	}
	rows = append(rows, renderDesktopWorldSummaryRow(blockID, ev, prefix, header, ctx, token))
	if ev.Err && ev.desktopWorld.failure != "" {
		rows = append(rows, renderDesktopWorldFailureRow(blockID, ev, "    ", width, ctx, opts))
	}
	if token == "" || opts.ToolPanelFullOutput == nil || !opts.ToolPanelFullOutput(ev.CallID) {
		return rows
	}
	const detailPrefix = "    "
	if renderableTextHasContent(ev.FullArgs) {
		rows = append(rows, renderACPToolOutputRowsWithToken(blockID, detailPrefix, "Arguments\n"+ev.FullArgs, width, ctx, ctx.Theme.ToolArgsStyle(), token)...)
	}
	if !ev.OutputSynthetic && renderableTextHasContent(ev.Output) {
		label, style := "Result", ctx.Theme.ToolOutputStyle()
		if ev.Err {
			label, style = "Error details", ctx.Theme.ToolErrorStyle()
		}
		rows = append(rows, renderACPToolOutputRowsWithToken(blockID, detailPrefix, label+"\n"+ev.Output, width, ctx, style, token)...)
	}
	review := transcript.ApprovalReviewDisplayParts(ev.ApprovalStatus, ev.ApprovalText)
	if review.Status != "" || review.Rationale != "" {
		tag, _ := approvalReviewStatusTag(ctx, review.Status)
		text := strings.TrimSpace("Approval " + tag + "\n" + review.Rationale)
		rows = append(rows, renderACPToolOutputRowsWithToken(blockID, detailPrefix, text, width, ctx, ctx.Theme.TranscriptMetaStyle(), token)...)
	}
	return rows
}

func renderDesktopWorldSummaryRow(blockID string, ev SubagentEvent, prefix, header string, ctx BlockRenderContext, token string) RenderedRow {
	line := prefix + header
	tone, dim := acpToolHeaderMark(ctx, ev.Err, ev.Done)
	styled := styleACPTranscriptHeaderWithMark(ctx, "• "+header, tone, dim)
	styled = ctx.Theme.TranscriptMetaStyle().Render(prefix) + ansi.Cut(styled, 2, 2+displayColumns(header))
	if ev.Err {
		styled = ctx.Theme.ErrorStyle().Render(line)
	}
	return StyledPlainClickablePreWrappedRow(blockID, line, styled, token)
}
