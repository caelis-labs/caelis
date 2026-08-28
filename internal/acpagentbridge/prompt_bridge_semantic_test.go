package acpagentbridge

import (
	"testing"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestApprovalDecisionUsesSDKPermissionOutcome(t *testing.T) {
	t.Parallel()

	options := []acpsdk.PermissionOption{
		{OptionId: "allow_once", Name: "Allow once", Kind: acpsdk.PermissionOptionKindAllowOnce},
		{OptionId: "reject_once", Name: "Reject", Kind: acpsdk.PermissionOptionKindRejectOnce},
	}
	approval := &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{
		{ID: string(options[0].OptionId), Name: options[0].Name, Kind: string(options[0].Kind)},
		{ID: string(options[1].OptionId), Name: options[1].Name, Kind: string(options[1].Kind)},
	}}
	allowed := approvalDecisionFromACPResponse("approval-1", approval, acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.NewRequestPermissionOutcomeSelected("allow_once"),
	})
	if !allowed.Approved || allowed.OptionID != "allow_once" || allowed.Outcome != "selected" || allowed.RequestID != "approval-1" {
		t.Fatalf("allow decision = %#v, want selected approval", allowed)
	}
	rejected := approvalDecisionFromACPResponse("approval-1", approval, acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.NewRequestPermissionOutcomeSelected("reject_once"),
	})
	if rejected.Approved || rejected.OptionID != "reject_once" {
		t.Fatalf("reject decision = %#v, want selected rejection", rejected)
	}
	cancelled := approvalDecisionFromACPResponse("approval-1", approval, acpsdk.RequestPermissionResponse{
		Outcome: acpsdk.NewRequestPermissionOutcomeCancelled(),
	})
	if cancelled.Approved {
		t.Fatalf("cancelled decision = %#v, must not approve allow-looking option", cancelled)
	}
	invalid := approvalDecisionFromACPResponse("approval-1", approval, acpsdk.RequestPermissionResponse{})
	if invalid.Approved || invalid.Outcome != "" || invalid.OptionID != "" {
		t.Fatalf("invalid decision = %#v, want fail-closed empty decision", invalid)
	}
}
