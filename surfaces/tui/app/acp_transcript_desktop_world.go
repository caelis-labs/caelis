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
	calls, ok := computerActivityCalls(first, children[first.CallID])
	if !ok {
		return nil, start, false
	}
	calls = slices.Clip(calls)
	done := first.Done
	end := start
	for i := start + 1; i < len(events); i++ {
		ev := events[i]
		if ev.Kind == SESemanticBoundary || ev.Kind == SEToolCall && parents[ev.CallID] != "" ||
			ev.Kind == SEApproval && approvalReviewHasTool(events, ev) {
			continue
		}
		additional, ok := computerActivityCalls(ev, children[ev.CallID])
		if !ok {
			break
		}
		calls = append(calls, additional...)
		done = done && ev.Done
		end = i
	}

	var visible, collapsed []SubagentEvent
	var previous *desktopWorldDisplay
	failed := false
	for _, call := range calls {
		done = done && call.Done
		failed = failed || call.Err
		display := call.desktopWorld
		if !display.visible(call.Err) {
			previous = nil
			continue
		}
		visible = append(visible, call)
		if !call.Err && call.Done {
			if previous != nil && previous.sameObservation(display) {
				collapsed[len(collapsed)-1] = call
				continue
			}
			previous = display
		} else {
			previous = nil
		}
		collapsed = append(collapsed, call)
	}
	if len(visible) == 0 {
		return nil, end, true
	}
	key := "computer:" + first.CallID
	token := ""
	if len(visible) > desktopWorldVisibleCalls || len(visible) != len(collapsed) {
		token = acpStableExplorationClickToken(key)
	}
	rows := []RenderedRow{renderACPToolHeaderRow(blockID, ansi.Truncate("• Using computer", width, "…"), width, ctx, token, failed, done)}
	var earlier []SubagentEvent
	if opts.ExplorationExpanded != nil && opts.ExplorationExpanded(key) {
		collapsed = visible
	} else if skip := len(collapsed) - desktopWorldVisibleCalls; skip > 0 {
		earlier, collapsed = collapsed[:skip], collapsed[skip:]
	}
	for i, call := range collapsed {
		rows = append(rows, renderDesktopWorldCallRows(blockID, call, explorationChildPrefix(i == 0), width, ctx, opts)...)
	}
	for _, call := range earlier {
		if !call.Err {
			continue
		}
		if opts.ToolPanelFullOutput != nil && opts.ToolPanelFullOutput(call.CallID) {
			rows = append(rows, renderDesktopWorldCallRows(blockID, call, explorationChildPrefix(false), width, ctx, opts)...)
		} else {
			rows = append(rows, renderDesktopWorldFailureRow(blockID, call, width, ctx, opts))
		}
	}
	return rows, end, true
}

func renderDesktopWorldFailureRow(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) RenderedRow {
	detail := ev.desktopWorld.failure
	if detail == "" {
		detail = ev.desktopWorld.label(maxInt(1, width-4), true)
	}
	line := ansi.Truncate("    "+detail, width, "…")
	return StyledPlainClickablePreWrappedRow(blockID, line, ctx.Theme.ErrorStyle().Render(line), acpToolPanelClickTokenIf(ev.CallID, opts.ToolOutputPanels && toolPanelHasSourceOrOutput(ev)))
}

func renderDesktopWorldCallRows(blockID string, ev SubagentEvent, prefix string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []RenderedRow {
	budget := minInt(width, desktopWorldSummaryColumns)
	prefix = ansi.Truncate(prefix, maxInt(0, budget-1), "")
	header := ev.desktopWorld.label(maxInt(1, budget-displayColumns(prefix)), ev.Err)
	line := prefix + header
	tone, dim := acpToolHeaderMark(ctx, ev.Err, ev.Done)
	styled := styleACPTranscriptHeaderWithMark(ctx, "• "+header, tone, dim)
	styled = ctx.Theme.TranscriptMetaStyle().Render(prefix) + ansi.Cut(styled, 2, 2+displayColumns(header))
	if ev.Err {
		styled = ctx.Theme.ErrorStyle().Render(line)
	}
	token := acpToolPanelClickTokenIf(ev.CallID, opts.ToolOutputPanels && toolPanelHasSourceOrOutput(ev))
	rows := []RenderedRow{StyledPlainClickablePreWrappedRow(blockID, line, styled, token)}
	if ev.Err && ev.desktopWorld.failure != "" {
		rows = append(rows, renderDesktopWorldFailureRow(blockID, ev, width, ctx, opts))
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
