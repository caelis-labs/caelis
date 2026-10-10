package tuiapp

import (
	"testing"
)

func TestApprovalToPromptRequestIncludesSandboxDetails(t *testing.T) {
	t.Parallel()

	msg := approvalToPromptRequest(&approvalPayload{
		ToolName:           "RunCommand",
		RawInput:           map[string]any{"command": "git fetch"},
		Reason:             "host execution requires user approval",
		Justification:      "Do you want to run git fetch on the host?",
		SandboxPermissions: "require_escalated",
		Options: []approvalOption{
			{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
		},
	}, make(chan PromptResponse, 1))

	for _, want := range []PromptDetail{
		{Label: "Action", Value: "execute"},
		{Label: "Command", Value: "command: git fetch", Emphasis: true},
		{Label: "Reason", Value: "host execution requires user approval"},
		{Label: "Justification", Value: "Do you want to run git fetch on the host?"},
		{Label: "Sandbox", Value: "require_escalated"},
		{Label: "Default", Value: "Allow once"},
	} {
		if !hasPromptDetail(msg.Details, want) {
			t.Fatalf("Details = %#v, missing %#v", msg.Details, want)
		}
	}
	if msg.DefaultChoice != "allow_once" {
		t.Fatalf("DefaultChoice = %q, want allow_once", msg.DefaultChoice)
	}
	if msg.Prompt != "Ran" {
		t.Fatalf("Prompt = %q, want display label", msg.Prompt)
	}
}

func TestApprovalActionLabelDoesNotClassifyTaskAsExecute(t *testing.T) {
	t.Parallel()

	if got := approvalActionLabel(&approvalPayload{ToolName: surfaceToolTask}); got != surfaceToolTask {
		t.Fatalf("approvalActionLabel(Task) = %q, want exact Task label", got)
	}
}

func TestApprovalDecisionFromPromptPreservesRequestID(t *testing.T) {
	t.Parallel()

	req := &approvalPayload{
		RequestID: "approval-child-1",
		Options: []approvalOption{
			{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
			{ID: "reject_once", Name: "Reject", Kind: "reject_once"},
		},
	}
	allowed := approvalDecisionFromPrompt(req, PromptResponse{Line: "allow_once"})
	if allowed.RequestID != req.RequestID || !allowed.Approved || allowed.OptionID != "allow_once" {
		t.Fatalf("allowed decision = %#v, want request id and allow_once", allowed)
	}
	rejected := approvalDecisionFromPrompt(req, PromptResponse{Line: "reject_once"})
	if rejected.RequestID != req.RequestID || rejected.Approved || rejected.OptionID != "reject_once" {
		t.Fatalf("rejected decision = %#v, want request id and reject_once", rejected)
	}
}

func TestMCPApprovalFourChoicesRetainScopeInTUI(t *testing.T) {
	req := &approvalPayload{RequestID: "mcp-approval", ToolName: "docs__read", RawInput: map[string]any{"key": "fixture"}, Options: []approvalOption{
		{ID: "allow_once", Name: "Allow Once", Kind: "allow_once"},
		{ID: "allow_session", Name: "Allow this session", Kind: "allow_once"},
		{ID: "allow_always", Name: "Allow Always", Kind: "allow_always"},
		{ID: "cancel", Name: "Cancel", Kind: "reject_once"},
	}}
	prompt := approvalToPromptRequest(req, make(chan PromptResponse, 1))
	if len(prompt.Choices) != 4 || prompt.AllowFreeformInput {
		t.Fatalf("TUI choices = %#v", prompt.Choices)
	}
	for i, choice := range req.Options {
		if prompt.Choices[i].Value != choice.ID || prompt.Choices[i].Label != choice.Name || prompt.Choices[i].Detail != choice.Kind {
			t.Fatalf("TUI choice = %#v, want %#v", prompt.Choices[i], choice)
		}
		decision := approvalDecisionFromPrompt(req, PromptResponse{Line: choice.ID})
		if decision.RequestID != req.RequestID || decision.OptionID != choice.ID || decision.Approved != (i < 3) {
			t.Fatalf("TUI decision = %#v", decision)
		}
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
