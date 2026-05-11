package approval

import (
	"encoding/json"
	"testing"

	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktool "github.com/OnslaughtSnail/caelis/sdk/tool"
)

func TestPayloadFromRuntimeRequestUsesProtocolApprovalFirst(t *testing.T) {
	req := sdkruntime.ApprovalRequest{
		Tool: sdktool.Definition{Name: "request_permissions"},
		Call: sdktool.Call{ID: "call-from-runtime", Name: "request_permissions"},
		Approval: &sdksession.ProtocolApproval{
			ToolCall: sdksession.ProtocolToolCall{
				ID:   "call-from-protocol",
				Name: "BASH",
				RawInput: map[string]any{
					"command":             "git restore hello.py",
					"approval_reason":     "destructive edit",
					"sandbox_permissions": "workspace_write",
					"additional_permissions": map[string]any{
						"write": "/tmp/cache",
					},
				},
			},
			Options: []sdksession.ProtocolApprovalOption{
				{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
				{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
			},
		},
	}

	payload := PayloadFromRuntimeRequest(req)
	if payload == nil {
		t.Fatal("PayloadFromRuntimeRequest() = nil, want payload")
	}
	if payload.ToolCallID != "call-from-protocol" || payload.ToolName != "BASH" {
		t.Fatalf("payload tool = %q/%q, want protocol call BASH", payload.ToolCallID, payload.ToolName)
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
	payload := PayloadFromRuntimeRequest(sdkruntime.ApprovalRequest{
		Tool: sdktool.Definition{Name: "BASH"},
		Call: sdktool.Call{ID: "call-1", Name: "BASH", Input: raw},
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
