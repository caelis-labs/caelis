package policy

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
)

func TestPolicyModelFeedbackDistinguishesResolvedDenialFromPendingApproval(t *testing.T) {
	t.Parallel()

	decision := Decision{Action: ActionAskApproval, Reason: "host execution requested"}
	denied, deniedHint := policyModelFeedback(decision, approval.Response{
		Outcome:  "selected",
		OptionID: "reject_once",
	})
	if denied != "approval denied" || deniedHint != "" {
		t.Fatalf("resolved denial feedback = (%q, %q), want denial without pending hint", denied, deniedHint)
	}

	pending, pendingHint := policyModelFeedback(decision, approval.Response{})
	if pending != "host execution requested" || pendingHint == "" {
		t.Fatalf("pending approval feedback = (%q, %q), want request reason and retry hint", pending, pendingHint)
	}
}
