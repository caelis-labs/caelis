package tuiapp

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
	"github.com/charmbracelet/x/ansi"
)

const desktopWorldSummaryColumns = 60
const desktopWorldVisibleCalls = 3

func renderDesktopWorldGroupRows(blockID string, events []SubagentEvent, start, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	first := events[start]
	if first.Kind != SEToolCall || first.desktopWorld == nil {
		return nil, start, false
	}
	// A normally routed Code Mode child is rendered by its script's group.
	// An orphaned child can still disclose its own invocation.
	if first.ParentToolName == "js_exec" {
		return renderDesktopWorldCallRows(blockID, first, "• ", width, ctx, opts), start, true
	}
	var calls []SubagentEvent
	done, failed := true, false
	end := start
	for i := start; i < len(events); i++ {
		ev := events[i]
		if ev.Kind == SEApproval && approvalReviewHasTool(events, ev) {
			continue
		}
		if ev.Kind != SEToolCall || ev.desktopWorld == nil || ev.ParentToolName == "js_exec" {
			break
		}
		calls = append(calls, ev)
		done = done && ev.Done
		failed = failed || ev.Err
		end = i
	}
	label := "• Using computer"
	if done {
		label = "• Used computer"
	}
	key := "computer:" + first.CallID
	expanded := opts.ExplorationExpanded != nil && opts.ExplorationExpanded(key)
	token := ""
	if len(calls) > desktopWorldVisibleCalls {
		token = acpStableExplorationClickToken(key)
	}
	rows := []RenderedRow{renderACPToolHeaderRow(blockID, ansi.Truncate(label, width, "…"), width, ctx, token, failed, done)}
	hidden := desktopWorldHiddenCalls(calls, expanded)
	for index, call := range calls {
		if !hidden[index] {
			prefix := explorationChildPrefix(len(rows) == 1)
			rows = append(rows, renderDesktopWorldCallRows(blockID, call, prefix, width, ctx, opts)...)
		}
	}
	if len(hidden) > 0 {
		rows = append(rows, renderDesktopWorldMoreRow(blockID, len(hidden), width, ctx, token))
	}
	return rows, end, true
}

// Leave room for the newest running call even after the group has filled its
// preview. The selected rows retain their chronological order.
func desktopWorldHiddenCalls(calls []SubagentEvent, expanded bool) map[int]bool {
	if expanded {
		return nil
	}
	var indices []int
	running := -1
	for i, call := range calls {
		if call.desktopWorld != nil {
			if !call.Done {
				running = len(indices)
			}
			indices = append(indices, i)
		}
	}
	if len(indices) <= desktopWorldVisibleCalls {
		return nil
	}
	if running >= desktopWorldVisibleCalls {
		indices[desktopWorldVisibleCalls-1], indices[running] = indices[running], indices[desktopWorldVisibleCalls-1]
	}
	hidden := make(map[int]bool, len(indices)-desktopWorldVisibleCalls)
	for _, i := range indices[desktopWorldVisibleCalls:] {
		hidden[i] = true
	}
	return hidden
}

func renderDesktopWorldMoreRow(blockID string, count, width int, ctx BlockRenderContext, token string) RenderedRow {
	line := ansi.Truncate(fmt.Sprintf("    +%d more", count), width, "…")
	return StyledPlainClickablePreWrappedRow(blockID, line, ctx.Theme.TranscriptMetaStyle().Render(line), token)
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
	if token == "" || opts.ToolPanelFullOutput == nil || !opts.ToolPanelFullOutput(ev.CallID) {
		return rows
	}
	const detailPrefix = "    └ "
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
