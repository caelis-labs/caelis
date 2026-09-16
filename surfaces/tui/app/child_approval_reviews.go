package tuiapp

import (
	"maps"
	"slices"
	"strings"
)

// Parent-feed review facts outlive the disposable Task document. One cache is
// scoped to its Spawn anchor and joins by child call ID; it neither creates
// tools nor supplies permission or Task lifecycle authority.
type childApprovalReviews struct {
	byCall map[string]SubagentEvent
	order  []string
	bytes  int
}

func (c childApprovalReviews) clone() childApprovalReviews {
	c.byCall, c.order = maps.Clone(c.byCall), slices.Clone(c.order)
	return c
}

func (c *childApprovalReviews) remember(event TranscriptEvent) {
	c.rememberReview(SubagentEvent{Kind: SEApproval, CallID: event.ToolCallID,
		ApprovalTool: event.ApprovalTool, ApprovalCommand: event.ApprovalCommand,
		ApprovalStatus: event.ApprovalStatus, ApprovalRisk: event.ApprovalRisk,
		ApprovalAuth: event.ApprovalAuth, ApprovalText: event.ApprovalText})
}

func (c *childApprovalReviews) rememberReview(review SubagentEvent) {
	callID := strings.TrimSpace(review.CallID)
	if callID == "" || strings.TrimSpace(review.ApprovalText) == "" {
		return
	}
	review.CallID = callID
	if c.byCall == nil {
		c.byCall = make(map[string]SubagentEvent)
	}
	previous, exists := c.byCall[callID]
	if exists {
		c.bytes -= childReviewBytes(previous)
	} else {
		c.order = append(c.order, callID)
	}
	mergeApprovalReviewEvent(&previous, review)
	c.byCall[callID] = previous
	c.bytes += childReviewBytes(previous)
	for len(c.order) > childDisplayEvents || c.bytes > childDisplayBytes {
		oldest := c.order[0]
		c.bytes -= childReviewBytes(c.byCall[oldest])
		delete(c.byCall, oldest)
		c.order[0] = ""
		c.order = c.order[1:]
	}
}

func (c *childApprovalReviews) prepend(older childApprovalReviews) {
	combined := older.clone()
	for _, callID := range c.order {
		combined.rememberReview(c.byCall[callID])
	}
	*c = combined
}

func childReviewBytes(e SubagentEvent) int {
	return 256 + len(e.CallID) + len(e.ApprovalTool) + len(e.ApprovalCommand) + len(e.ApprovalStatus) + len(e.ApprovalRisk) + len(e.ApprovalAuth) + len(e.ApprovalText)
}

func (v *subagentOutputView) applyChildReview(block *ParticipantTurnBlock, callID string) bool {
	review, ok := v.approvalReviews.byCall[strings.TrimSpace(callID)]
	if !ok || block == nil {
		return false
	}
	block.AddApprovalReviewEvent(review.CallID, review.ApprovalTool, review.ApprovalCommand,
		review.ApprovalStatus, review.ApprovalRisk, review.ApprovalAuth, review.ApprovalText)
	return true
}

func (v *subagentOutputView) restoreChildReviews() {
	for _, callID := range v.approvalReviews.order {
		v.applyChildReview(v.blockForObservedChildTool(callID), callID)
	}
}
