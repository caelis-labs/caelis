package approval

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestNativeResponseUsesExactOfferedKindWithoutDisplayAuthority(t *testing.T) {
	permission := &session.ProtocolApproval{Options: []session.ProtocolApprovalOption{
		{ID: "native-reject", Kind: "reject_once", Name: "Allow everything"},
		{ID: "native-allow", Kind: "allow_once", Name: "Reject"},
	}, Subject: &session.ApprovalSubject{Grants: []session.ApprovalGrant{{OptionID: "invented", Scope: session.KnownApprovalField("unlimited")}}}}
	for _, test := range []struct {
		name            string
		in              Response
		valid, approved bool
	}{
		{"forged", Response{Outcome: "selected", OptionID: "invented", Approved: true}, false, false},
		{"missing", Response{Outcome: "selected", Approved: true}, false, false},
		{"normalized_alias", Response{Outcome: "selected", OptionID: " native-allow ", Approved: true}, false, false},
		{"allow_without_boolean", Response{Outcome: "selected", OptionID: "native-allow"}, true, true},
		{"deny_with_boolean", Response{Outcome: "selected", OptionID: "native-reject", Approved: true}, true, false},
		{"cancel_with_boolean", Response{Outcome: "cancelled", Approved: true}, true, false},
		{"cancel_with_option", Response{Outcome: "cancelled", OptionID: "native-allow"}, false, false},
		{"unknown_outcome", Response{Outcome: "mystery", Approved: true}, false, false},
		{"legacy_approve", Response{Outcome: "approved", Approved: true}, true, true},
		{"legacy_deny", Response{Outcome: "rejected"}, true, false},
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
	if _, err := ResolveNativeResponse(permission, Response{Outcome: "approved", Approved: true}); err == nil {
		t.Fatal("outcome-only decision manufactured an unavailable grant")
	}
}

func TestNativeResponseWithoutOptionsStillValidatesOutcome(t *testing.T) {
	for _, tc := range []struct {
		name            string
		response        Response
		valid, approved bool
	}{
		{"boolean", Response{Approved: true}, true, true},
		{"approved", Response{Outcome: "approved"}, true, true},
		{"rejected", Response{Outcome: "rejected", Approved: true}, true, false},
		{"cancelled", Response{Outcome: "cancelled", Approved: true}, true, false},
		{"unsupported", Response{Outcome: "unknown", Approved: true}, false, false},
		{"invented_option", Response{Outcome: "approved", OptionID: "invented", Approved: true}, false, false},
		{"legacy_selected_allow", Response{Outcome: "selected", OptionID: "deny", Approved: true}, true, true},
		{"legacy_selected_deny", Response{Outcome: "selected", OptionID: "allow"}, true, false},
		{"missing_selection", Response{Outcome: "selected", Approved: true}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, permission := range []*session.ProtocolApproval{nil, {}} {
				got, err := ResolveNativeResponse(permission, tc.response)
				if (err == nil) != tc.valid || err == nil && (got.Approved != tc.approved || got.Outcome != tc.response.Outcome || got.OptionID != tc.response.OptionID) {
					t.Fatalf("optionless response = %+v, %v", got, err)
				}
			}
		})
	}
}
