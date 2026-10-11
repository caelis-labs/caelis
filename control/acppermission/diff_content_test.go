package acppermission_test

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/acppermission"
)

func TestPermissionRoundTripPreservesStructuredDiffFacts(t *testing.T) {
	t.Parallel()
	changes := []session.ProtocolDiffChange{{Operation: "modify", Path: " exact file.txt "}}
	approval := &session.ProtocolApproval{ToolCall: session.ProtocolToolCall{ID: "edit", Content: []session.ProtocolToolCallContent{{Type: "diff", Changes: &changes, Patch: &session.ProtocolDiffPatch{Format: "git_patch", Text: "-old\r\n+new\r\n"}, Meta: map[string]any{"display": "exact"}}}}, Options: []session.ProtocolApprovalOption{{ID: "allow", Name: "Allow once", Kind: "allow_once"}, {ID: "deny", Name: "Reject", Kind: "reject_once"}}}
	wire, err := acppermission.EncodePermissionRequest(session.SessionRef{SessionID: "session"}, approval, nil)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := acppermission.DecodePermissionRequest(wire)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(restored.ToolCall.Content, approval.ToolCall.Content) || !reflect.DeepEqual(restored.Options, approval.Options) {
		t.Fatalf("permission facts/options changed: %#v", restored)
	}
}
