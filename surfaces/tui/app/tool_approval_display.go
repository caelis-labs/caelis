package tuiapp

import (
	"slices"
	"strings"

	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
	"github.com/charmbracelet/x/ansi"
)

// Review records remain separate presentation input. Join by call identity only
// while rendering so approval never changes the tool's execution lifecycle.
func renderACPToolLifecycleRows(blockID string, events []SubagentEvent, idx int, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int) {
	if idx < 0 || idx >= len(events) {
		return nil, idx
	}
	callID := events[idx].CallID
	var review *SubagentEvent
	if callID != "" {
		for i := range events {
			if events[i].Kind == SEApproval && events[i].CallID == callID {
				review = &events[i]
			}
		}
	}
	if review == nil {
		return renderACPToolLifecycleRowsWithoutReview(blockID, events, idx, width, ctx, opts)
	}
	display := transcript.ApprovalReviewDisplayParts(review.ApprovalStatus, review.ApprovalRisk, review.ApprovalAuth, review.ApprovalText)
	if display.Status == "denied" {
		events = slices.Clone(events)
		for i := idx; i < len(events) && events[i].Kind == SEToolCall && events[i].CallID == callID; i++ {
			events[i].Output = display.Rationale
			events[i].OutputSynthetic = false
			events[i].Done = true
			events[i].Err = true
		}
	}
	rows, end := renderACPToolLifecycleRowsWithoutReview(blockID, events, idx, width, ctx, opts)
	if len(rows) > 0 {
		// Replace the generic failure suffix only for an explicit denial. An
		// approved tool may still fail during execution and retains that status.
		if display.Status == "denied" && strings.HasSuffix(rows[0].Plain, " failed") {
			rows[0].Plain = strings.TrimSuffix(rows[0].Plain, " failed")
			rows[0].Styled = ansi.Truncate(rows[0].Styled, ansi.StringWidth(rows[0].Styled)-len(" failed"), "")
		}
		rows[0].Plain += " " + display.Status
		rows[0].Styled += " " + approvalReviewStatusStyle(ctx, display.Status).Render(display.Status)
	}
	if len(rows) > 0 {
		headerRows := wrapAgentMessageRows(rows[0], width)
		rows = append(headerRows, rows[1:]...)
	}
	return rows, end
}

func approvalReviewHasTool(events []SubagentEvent, review SubagentEvent) bool {
	if review.CallID == "" {
		return false
	}
	for _, event := range events {
		if event.Kind == SEToolCall && event.CallID == review.CallID {
			return true
		}
	}
	return false
}
