package policy

import "testing"

func TestRequestValidate(t *testing.T) {
	valid := Request{ToolName: "READ"}
	if err := valid.Validate(); err != nil {
		t.Errorf("expected valid, got %v", err)
	}

	noTool := Request{}
	if err := noTool.Validate(); err == nil {
		t.Error("expected error for missing tool name")
	}
}

func TestDecisionHelpers(t *testing.T) {
	allow := Decision{Outcome: OutcomeAllow}
	if !allow.IsAllow() || allow.IsDeny() || allow.IsApprovalNeeded() {
		t.Error("allow decision helpers incorrect")
	}

	deny := Decision{Outcome: OutcomeDeny}
	if deny.IsAllow() || !deny.IsDeny() || deny.IsApprovalNeeded() {
		t.Error("deny decision helpers incorrect")
	}

	approval := Decision{Outcome: OutcomeApprovalNeeded}
	if approval.IsAllow() || approval.IsDeny() || !approval.IsApprovalNeeded() {
		t.Error("approval decision helpers incorrect")
	}
}
