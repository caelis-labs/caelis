package policy

import "fmt"

// Validate checks that the request is well-formed.
func (r Request) Validate() error {
	if r.ToolName == "" {
		return fmt.Errorf("policy request: ToolName is required")
	}
	return nil
}

// IsAllow reports whether the decision allows the action.
func (d Decision) IsAllow() bool { return d.Outcome == OutcomeAllow }

// IsDeny reports whether the decision denies the action.
func (d Decision) IsDeny() bool { return d.Outcome == OutcomeDeny }

// IsApprovalNeeded reports whether the decision requires approval.
func (d Decision) IsApprovalNeeded() bool { return d.Outcome == OutcomeApprovalNeeded }
