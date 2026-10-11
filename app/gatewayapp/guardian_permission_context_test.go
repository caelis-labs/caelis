package gatewayapp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestGuardianIndependentPermissionContextRetainsExactAction(t *testing.T) {
	req := guardianWindowRequest(t, "independent")
	req.Approval.ToolName, req.Approval.ToolTitle, req.Approval.ToolKind = "", "", ""
	req.Approval.RawInput = nil
	req.RuntimeRequest.Call.Name, req.RuntimeRequest.Tool.Name = "", ""
	req.RuntimeRequest.Call.Input = nil
	req.Approval.Context = &session.PermissionContext{Title: "Actual original permission title", Description: new("Actual original description"), RawSubject: json.RawMessage(`{"type":"_vendor","credential":"PRIVATE-EXACT-EVIDENCE","count":9007199254740991}`)}
	raw, large, err := guardianPlannedActionJSON(req)
	if err != nil || large || !strings.Contains(raw, "PRIVATE-EXACT-EVIDENCE") || !strings.Contains(raw, "9007199254740991") || !strings.Contains(raw, "Actual original permission title") || !strings.Contains(raw, "permission_context") {
		t.Fatalf("Guardian context was replaced or rounded: %s %v", raw, err)
	}
	req.Approval.Context.DisplaySubjectHint = &session.ApprovalSubject{Kind: "command", Operation: session.KnownApprovalField("FAKE-DISPLAY-ONLY-ACTION"), Scope: session.KnownApprovalField("FAKE-DISPLAY-GRANT")}
	withHint, _, err := guardianPlannedActionJSON(req)
	if err != nil || withHint != raw {
		t.Fatalf("remote display claims changed authoritative Guardian action: %s, %v", withHint, err)
	}
	req.Approval.Context.RawSubject = json.RawMessage(`{"large":"` + strings.Repeat("x", guardianMaxActionBytes+1) + `"}`)
	_, large, err = guardianPlannedActionJSON(req)
	if err != nil || !large {
		t.Fatal("independent permission bypassed Guardian action budget")
	}
}
