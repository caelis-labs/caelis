package tuiapp

import (
	"testing"

	"github.com/OnslaughtSnail/caelis/kernel"
)

func TestApprovalToPromptRequestIncludesSandboxDetails(t *testing.T) {
	t.Parallel()

	msg := approvalToPromptRequest(&kernel.ApprovalPayload{
		ToolName:           "BASH",
		RawInput:           map[string]any{"command": "make generate"},
		Reason:             "additional sandbox permissions require user approval",
		Justification:      "Do you want to grant a cache path?",
		SandboxPermissions: "with_additional_permissions",
		AdditionalPermissions: map[string]any{
			"network": map[string]any{"enabled": true},
			"file_system": map[string]any{
				"write": []any{"/tmp/cache"},
			},
		},
		Options: []kernel.ApprovalOption{
			{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
		},
	}, make(chan PromptResponse, 1))

	for _, want := range []PromptDetail{
		{Label: "Action", Value: "execute"},
		{Label: "Command", Value: "command: make generate", Emphasis: true},
		{Label: "Risk", Value: "network: enabled; write: /tmp/cache", Emphasis: true},
		{Label: "Reason", Value: "additional sandbox permissions require user approval"},
		{Label: "Justification", Value: "Do you want to grant a cache path?"},
		{Label: "Sandbox", Value: "with_additional_permissions"},
		{Label: "Permissions", Value: "network: enabled; write: /tmp/cache"},
		{Label: "Default", Value: "Allow once"},
	} {
		if !hasPromptDetail(msg.Details, want) {
			t.Fatalf("Details = %#v, missing %#v", msg.Details, want)
		}
	}
	if msg.DefaultChoice != "allow_once" {
		t.Fatalf("DefaultChoice = %q, want allow_once", msg.DefaultChoice)
	}
}

func hasPromptDetail(details []PromptDetail, want PromptDetail) bool {
	for _, detail := range details {
		if detail.Label == want.Label && detail.Value == want.Value && detail.Emphasis == want.Emphasis {
			return true
		}
	}
	return false
}
