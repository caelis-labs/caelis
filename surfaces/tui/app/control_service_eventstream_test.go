package tuiapp

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func TestApprovalPayloadFromACPEventRestoresPromptFields(t *testing.T) {
	status := schema.ToolStatusPending
	kind := "RUN_COMMAND"
	req := approvalPayloadFromACPEvent(eventstream.Envelope{
		Kind: eventstream.KindRequestPermission,
		Permission: &schema.RequestPermissionRequest{
			SessionID: "session-1",
			ToolCall: schema.ToolCallUpdate{
				SessionUpdate: schema.UpdateToolCallInfo,
				ToolCallID:    "call-1",
				Kind:          &kind,
				Status:        &status,
				RawInput: map[string]any{
					"command":             "go test ./...",
					"approval_reason":     "needs execution",
					"justification":       "requested by user",
					"sandbox_permissions": "host",
				},
			},
			Options: []schema.PermissionOption{{
				OptionID: "allow_once",
				Name:     "Allow once",
				Kind:     "allow_once",
			}},
		},
	})
	if req == nil {
		t.Fatal("approvalPayloadFromACPEvent() = nil, want payload")
	}
	if req.ToolCallID != "call-1" || req.ToolName != "RUN_COMMAND" {
		t.Fatalf("tool = (%q, %q), want call-1 RUN_COMMAND", req.ToolCallID, req.ToolName)
	}
	if req.Reason != "needs execution" || req.Justification != "requested by user" || req.SandboxPermissions != "host" {
		t.Fatalf("prompt fields = (%q, %q, %q), want restored fields", req.Reason, req.Justification, req.SandboxPermissions)
	}
	if len(req.Options) != 1 || req.Options[0].ID != "allow_once" {
		t.Fatalf("options = %#v, want allow_once", req.Options)
	}
}
