package tuiapp

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestApprovalToPromptRequestIncludesSandboxDetails(t *testing.T) {
	t.Parallel()

	msg := approvalToPromptRequest(&approvalPayload{
		Subject:            &session.ApprovalSubject{Kind: "command", Tool: session.KnownApprovalField("RunCommand"), Operation: session.KnownApprovalField("execute"), Targets: []session.ApprovalField{session.KnownApprovalField("git fetch"), session.KnownApprovalField("/workspace")}, Effect: session.KnownApprovalField("run the supplied command"), Scope: session.KnownApprovalField("host execution"), Grants: []session.ApprovalGrant{{OptionID: "allow_once", Scope: session.KnownApprovalField("this invocation")}}},
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
		{Label: "Operation", Value: "execute"},
		{Label: "Command", Value: "git fetch", Emphasis: true},
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
	if msg.Prompt != "RunCommand" {
		t.Fatalf("Prompt = %q, want display label", msg.Prompt)
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
		if prompt.Choices[i].Value != choice.ID || prompt.Choices[i].Label != choice.Name || prompt.Choices[i].Detail != choice.Kind+"; scope: [unavailable]" {
			t.Fatalf("TUI choice = %#v, want %#v", prompt.Choices[i], choice)
		}
		decision := approvalDecisionFromPrompt(req, PromptResponse{Line: choice.ID})
		if decision.RequestID != req.RequestID || decision.OptionID != choice.ID || decision.Approved != (i < 3) {
			t.Fatalf("TUI decision = %#v", decision)
		}
	}
}

func TestApprovalDecisionUsesStrictOptionKinds(t *testing.T) {
	for _, kind := range []string{"allow_once", "allow_always", "deny", "deny_once", "deny_always", "reject", "reject_once", "reject_always"} {
		t.Run(kind, func(t *testing.T) {
			req := &approvalPayload{RequestID: "kind-owner", Options: []approvalOption{
				{ID: "approve:opaque/ID", Name: "Allow everything", Kind: kind},
			}}
			decision := approvalDecisionFromPrompt(req, PromptResponse{Line: req.Options[0].ID})
			wantApproved := kind == "allow_once" || kind == "allow_always"
			if decision.RequestID != req.RequestID || decision.Outcome != approvalStatusSelected || decision.OptionID != req.Options[0].ID || decision.Approved != wantApproved {
				t.Fatalf("decision = %+v, want original ID and approved=%t for kind %q", decision, wantApproved, kind)
			}
		})
	}
}

func TestApprovalDecisionInvalidOptionsFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options []approvalOption
	}{
		{name: "missing kind", options: []approvalOption{{ID: "allow_once", Name: "Approve"}}},
		{name: "unknown kind", options: []approvalOption{{ID: "allow_once", Name: "Approve", Kind: "allow_custom"}}},
		{name: "missing id", options: []approvalOption{{Name: "Approve", Kind: "allow_once"}}},
		{name: "duplicate id", options: []approvalOption{{ID: "allow_once", Kind: "allow_once"}, {ID: "allow_once", Kind: "reject_once"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := &approvalPayload{RequestID: "invalid-options", Options: tc.options}
			for _, response := range []PromptResponse{{Line: "allow_once"}, {Line: "yes"}, {Err: errors.New(PromptErrInterrupt)}} {
				decision := approvalDecisionFromPrompt(req, response)
				if decision.RequestID != req.RequestID || decision.Approved || decision.OptionID != "" || decision.Outcome != approvalStatusRejected {
					t.Fatalf("invalid options produced decision %+v", decision)
				}
			}
		})
	}
}

func TestApprovalDecisionUnknownSelectionDoesNotUseOptionlessCompatibility(t *testing.T) {
	req := &approvalPayload{RequestID: "with-options", Options: []approvalOption{
		{ID: "opaque-allow", Kind: "allow_once"},
		{ID: "opaque-deny", Kind: "reject_once"},
	}}
	for _, selected := range []string{"approve", "allow", "yes", "y", "unknown"} {
		decision := approvalDecisionFromPrompt(req, PromptResponse{Line: selected})
		if decision.RequestID != req.RequestID || decision.Approved || decision.OptionID != "opaque-deny" || decision.Outcome != approvalStatusSelected {
			t.Fatalf("unknown selection %q produced decision %+v", selected, decision)
		}
	}
}

func TestApprovalDecisionOptionlessCompatibility(t *testing.T) {
	req := &approvalPayload{RequestID: "optionless"}
	for _, selected := range []string{"approve", "allow", "yes", "y"} {
		decision := approvalDecisionFromPrompt(req, PromptResponse{Line: selected})
		if decision.RequestID != req.RequestID || !decision.Approved || decision.OptionID != "" || decision.Outcome != approvalStatusApproved {
			t.Fatalf("optionless selection %q produced decision %+v", selected, decision)
		}
	}
	for _, response := range []PromptResponse{{}, {Line: "no"}, {Err: errors.New(PromptErrInterrupt)}} {
		decision := approvalDecisionFromPrompt(req, response)
		if decision.RequestID != req.RequestID || decision.Approved || decision.OptionID != "" || decision.Outcome != approvalStatusRejected {
			t.Fatalf("optionless rejection produced decision %+v", decision)
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
