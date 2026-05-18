package approval

import (
	"encoding/json"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestPayloadFromRuntimeRequestUsesProtocolApprovalFirst(t *testing.T) {
	req := agent.ApprovalRequest{
		Tool: tool.Definition{Name: "request_permissions"},
		Call: tool.Call{ID: "call-from-runtime", Name: "request_permissions"},
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{
				ID:   "call-from-protocol",
				Name: "RUN_COMMAND",
				RawInput: map[string]any{
					"command":             "git restore hello.py",
					"approval_reason":     "destructive edit",
					"sandbox_permissions": "workspace_write",
					"additional_permissions": map[string]any{
						"write": "/tmp/cache",
					},
				},
			},
			Options: []session.ProtocolApprovalOption{
				{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
				{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
			},
		},
	}

	payload := PayloadFromRuntimeRequest(req)
	if payload == nil {
		t.Fatal("PayloadFromRuntimeRequest() = nil, want payload")
	}
	if payload.ToolCallID != "call-from-protocol" || payload.ToolName != "RUN_COMMAND" {
		t.Fatalf("payload tool = %q/%q, want protocol call RUN_COMMAND", payload.ToolCallID, payload.ToolName)
	}
	if payload.RawInput["command"] != "git restore hello.py" {
		t.Fatalf("payload raw input = %#v, want command", payload.RawInput)
	}
	if payload.Reason != "destructive edit" || payload.SandboxPermissions != "workspace_write" {
		t.Fatalf("payload policy fields = %#v", payload)
	}
	if payload.AdditionalPermissions["write"] != "/tmp/cache" {
		t.Fatalf("payload additional permissions = %#v", payload.AdditionalPermissions)
	}
	if len(payload.Options) != 2 || payload.Options[0].ID != "allow_once" {
		t.Fatalf("payload options = %#v, want protocol options", payload.Options)
	}
}

func TestPayloadFromRuntimeRequestFallsBackToCallInput(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{
		"command": "git status --short",
	})
	payload := PayloadFromRuntimeRequest(agent.ApprovalRequest{
		Tool: tool.Definition{Name: "RUN_COMMAND"},
		Call: tool.Call{ID: "call-1", Name: "RUN_COMMAND", Input: raw},
	})

	if payload == nil {
		t.Fatal("PayloadFromRuntimeRequest() = nil, want payload")
	}
	if payload.RawInput["command"] != "git status --short" {
		t.Fatalf("payload raw input = %#v, want decoded call input", payload.RawInput)
	}
}

func TestRuntimeResponseFromReviewSelectsMatchingOption(t *testing.T) {
	payload := &Payload{
		Options: []Option{
			{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
			{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
		},
	}
	resp := RuntimeResponseFromReview(payload, ReviewResult{
		Approved:    true,
		Rationale:   "matches request",
		DisplayText: "Automatic approval review approved",
	})

	if !resp.Approved || resp.Outcome != string(StatusSelected) || resp.OptionID != "allow_once" {
		t.Fatalf("response = %#v, want selected allow_once", resp)
	}
	if resp.ReviewText != "Automatic approval review approved" || resp.Reason != "matches request" {
		t.Fatalf("response text = %#v", resp)
	}
}
