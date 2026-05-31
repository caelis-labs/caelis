package tuiapp

import (
	"context"
	"testing"

	coreruntime "github.com/OnslaughtSnail/caelis/core/runtime"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

func TestApprovalToPromptRequestIncludesSandboxDetails(t *testing.T) {
	t.Parallel()

	msg := approvalItemToPromptRequest(&appviewmodel.ApprovalItem{
		Tool:               "RUN_COMMAND",
		Command:            "git fetch",
		Reason:             "host execution requires user approval",
		Justification:      "Do you want to run git fetch on the host?",
		SandboxPermissions: "require_escalated",
		Options: []coresession.ApprovalOption{
			{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
		},
	}, make(chan PromptResponse, 1))

	for _, want := range []PromptDetail{
		{Label: "Action", Value: "execute"},
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
}

func TestAwaitApprovalPromptSubmitsCoreApprovalDecision(t *testing.T) {
	t.Parallel()

	turn := &bridgeTestTurn{}
	responses := make(chan PromptResponse, 1)
	responses <- PromptResponse{Line: "allow_once"}
	awaitApprovalItemPrompt(context.Background(), turn, &appviewmodel.ApprovalItem{
		Options: []coresession.ApprovalOption{
			{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
		},
	}, responses, nil)

	if len(turn.submissions) != 1 {
		t.Fatalf("submissions = %#v, want one approval submission", turn.submissions)
	}
	got := turn.submissions[0]
	if got.Kind != coreruntime.SubmissionApproval || got.Approval == nil {
		t.Fatalf("submission = %#v, want core approval submission", got)
	}
	if got.Approval.OptionID != "allow_once" || !got.Approval.Approved {
		t.Fatalf("approval = %#v, want selected allow_once", got.Approval)
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
