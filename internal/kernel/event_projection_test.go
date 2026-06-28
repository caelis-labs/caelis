package kernel

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestCanonicalApprovalPayloadPreservesPromptFields(t *testing.T) {
	t.Parallel()

	payload := canonicalApprovalPayload(&agent.ApprovalRequest{
		Tool: tool.Definition{Name: "RUN_COMMAND"},
		Call: tool.Call{
			ID: "call-1",
			Input: []byte(`{
				"command":"go test ./...",
				"approval_reason":"needs execution",
				"justification":"requested by user",
				"sandbox_permissions":"workspace-write"
			}`),
		},
		Approval: &session.ProtocolApproval{
			Options: []session.ProtocolApprovalOption{{
				ID:   "allow_once",
				Name: "Allow once",
				Kind: "allow_once",
			}},
		},
	})
	if payload == nil {
		t.Fatal("canonicalApprovalPayload() = nil")
	}
	if payload.ToolCallID != "call-1" || payload.ToolName != "RUN_COMMAND" {
		t.Fatalf("approval payload identity = %#v", payload)
	}
	if payload.Reason != "needs execution" || payload.Justification != "requested by user" || payload.SandboxPermissions != "workspace-write" {
		t.Fatalf("approval prompt fields = %#v", payload)
	}
	if len(payload.Options) != 1 || payload.Options[0].ID != "allow_once" {
		t.Fatalf("approval options = %#v", payload.Options)
	}
	if payload.RawInput["command"] != "go test ./..." {
		t.Fatalf("approval raw input = %#v", payload.RawInput)
	}
}
