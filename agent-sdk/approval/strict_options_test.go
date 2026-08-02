package approval

import "testing"

func TestStrictApprovalOptionsUseCanonicalKinds(t *testing.T) {
	options := []Option{
		{ID: "opaque-a", Name: "Proceed", Kind: "allow_once"},
		{ID: "opaque-b", Name: "Stop", Kind: "reject_once"},
	}

	if err := ValidateStrictOptions(options); err != nil {
		t.Fatalf("ValidateStrictOptions() error = %v", err)
	}
	selected, decision, err := ResolveStrictOption(options, "opaque-a")
	if err != nil {
		t.Fatalf("ResolveStrictOption() error = %v", err)
	}
	if selected.ID != "opaque-a" || decision != OptionDecisionAllow {
		t.Fatalf("strict selected option = %#v/%q, want opaque-a/allow", selected, decision)
	}
	rejectID, ok, err := StrictOptionIDForDecision(options, OptionDecisionDeny)
	if err != nil {
		t.Fatalf("StrictOptionIDForDecision() error = %v", err)
	}
	if !ok || rejectID != "opaque-b" {
		t.Fatalf("strict reject option = %q/%t, want opaque-b/true", rejectID, ok)
	}
}

func TestStrictApprovalOptionsAcceptCanonicalDenyKinds(t *testing.T) {
	for _, kind := range []string{"deny", "deny_once", "deny_always", "reject", "reject_once", "reject_always"} {
		t.Run(kind, func(t *testing.T) {
			options := []Option{{ID: "opaque", Name: "Stop", Kind: kind}}
			_, decision, err := ResolveStrictOption(options, "opaque")
			if err != nil {
				t.Fatalf("ResolveStrictOption() error = %v", err)
			}
			if decision != OptionDecisionDeny {
				t.Fatalf("decision = %q, want deny", decision)
			}
		})
	}
}

func TestStrictApprovalOptionsRejectAmbiguousOrInferredSemantics(t *testing.T) {
	tests := []struct {
		name    string
		options []Option
	}{
		{
			name: "duplicate id",
			options: []Option{
				{ID: "same", Kind: "allow_once"},
				{ID: "same", Kind: "reject_once"},
			},
		},
		{name: "missing id", options: []Option{{Name: "Allow once", Kind: "allow_once"}}},
		{name: "missing kind", options: []Option{{ID: "allow_once", Name: "Allow once"}}},
		{name: "id must not imply kind", options: []Option{{ID: "allow_once", Kind: "custom"}}},
		{name: "name must not imply kind", options: []Option{{ID: "opaque", Name: "Allow once", Kind: "custom"}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateStrictOptions(tc.options); err == nil {
				t.Fatal("ValidateStrictOptions() error = nil, want fail-closed validation")
			}
		})
	}
}
