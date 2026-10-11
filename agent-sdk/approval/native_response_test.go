package approval

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestNativeResponseUsesExactOfferedKindWithoutDisplayAuthority(t *testing.T) {
	permission := &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{
		{ID: "native-reject", Kind: "reject_once", Name: "Allow everything"},
		{ID: "native-allow", Kind: "allow_once", Name: "Reject"},
	}, Subject: &session.ApprovalSubject{Grants: []session.ApprovalGrant{{OptionID: "invented", Scope: session.KnownApprovalField("unlimited")}}}}
	for _, test := range []struct {
		name            string
		in              agent.ApprovalResponse
		valid, approved bool
	}{
		{"forged", agent.ApprovalResponse{Outcome: "selected", OptionID: "invented", Approved: true}, false, false},
		{"missing", agent.ApprovalResponse{Outcome: "selected", Approved: true}, false, false},
		{"normalized_alias", agent.ApprovalResponse{Outcome: "selected", OptionID: " native-allow ", Approved: true}, false, false},
		{"allow_without_boolean", agent.ApprovalResponse{Outcome: "selected", OptionID: "native-allow"}, true, true},
		{"deny_with_boolean", agent.ApprovalResponse{Outcome: "selected", OptionID: "native-reject", Approved: true}, true, false},
		{"cancel_with_boolean", agent.ApprovalResponse{Outcome: "cancelled", Approved: true}, true, false},
		{"cancel_with_option", agent.ApprovalResponse{Outcome: "cancelled", OptionID: "native-allow"}, false, false},
		{"unknown_outcome", agent.ApprovalResponse{Outcome: "mystery", Approved: true}, false, false},
		{"legacy_approve", agent.ApprovalResponse{Outcome: "approved", Approved: true}, true, true},
		{"legacy_deny", agent.ApprovalResponse{Outcome: "rejected"}, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			out, err := ResolveNativeResponse(permission, test.in)
			if (err == nil) != test.valid || err == nil && out.Approved != test.approved {
				t.Fatalf("decision = %+v, %v", out, err)
			}
			if err == nil && (out.OptionID != test.in.OptionID || out.Outcome != test.in.Outcome) {
				t.Fatal("resolution changed native identity")
			}
		})
	}
	permission.Options = permission.Options[:1]
	if _, err := ResolveNativeResponse(permission, agent.ApprovalResponse{Outcome: "approved", Approved: true}); err == nil {
		t.Fatal("outcome-only decision manufactured an unavailable grant")
	}
	legacy := agent.ApprovalResponse{Outcome: "approved", Approved: true}
	if got, err := ResolveNativeResponse(nil, legacy); err != nil || got != legacy {
		t.Fatalf("legacy optionless response = %+v, %v", got, err)
	}
}
