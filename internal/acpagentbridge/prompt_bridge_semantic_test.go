package acpagentbridge

import (
	"testing"

	"github.com/caelis-labs/caelis/protocol/acp"
)

func TestApprovalDecisionUsesSharedSemanticCodec(t *testing.T) {
	t.Parallel()

	options := []acp.PermissionOption{
		{OptionID: "allow_once", Name: "Allow once", Kind: "allow_once"},
		{OptionID: "reject_once", Name: "Reject", Kind: "reject_once"},
	}
	allowed := approvalDecisionFromACPResponse("approval-1", options, acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: "allow_once"},
	})
	if !allowed.Approved || allowed.OptionID != "allow_once" || allowed.Outcome != "selected" || allowed.RequestID != "approval-1" {
		t.Fatalf("allow decision = %#v, want selected approval", allowed)
	}
	rejected := approvalDecisionFromACPResponse("approval-1", options, acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "selected", OptionID: "reject_once"},
	})
	if rejected.Approved || rejected.OptionID != "reject_once" {
		t.Fatalf("reject decision = %#v, want selected rejection", rejected)
	}
	cancelled := approvalDecisionFromACPResponse("approval-1", options, acp.RequestPermissionResponse{
		Outcome: acp.PermissionOutcome{Outcome: "cancelled", OptionID: "allow_once"},
	})
	if cancelled.Approved {
		t.Fatalf("cancelled decision = %#v, must not approve allow-looking option", cancelled)
	}
}
