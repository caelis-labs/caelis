// Package deny contains a conservative approval implementation for batch use.
package deny

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/approval"
)

type Approver struct{}

func (Approver) Decide(_ context.Context, req approval.Request) (approval.Decision, error) {
	reason := "approval denied by default policy"
	if req.Approval != nil {
		if tool := strings.TrimSpace(req.Approval.ToolName); tool != "" {
			reason = fmt.Sprintf("approval denied by default policy for %s", tool)
		}
	}
	return approval.Decision{
		Approved:       false,
		Outcome:        string(approval.StatusRejected),
		Risk:           "unknown",
		Authorization:  "unknown",
		Rationale:      reason,
		DisplayText:    approval.FormatReviewText(false, "unknown", "unknown", reason),
		DecisionSource: "deny",
	}, nil
}

var _ approval.Approver = Approver{}
