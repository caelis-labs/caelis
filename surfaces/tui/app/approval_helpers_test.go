package tuiapp

import (
	"strings"
	"testing"
)

func TestApprovalToPromptRequestIncludesSandboxDetails(t *testing.T) {
	t.Parallel()

	msg := approvalToPromptRequest(&approvalPayload{
		ToolName:           "RUN_COMMAND",
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

func TestApprovalReviewPendingHintPrefersCommandOverUnknownTool(t *testing.T) {
	t.Parallel()

	hint := approvalReviewPendingHint("UNKNOWN", map[string]any{
		"command": "git status --short",
	}, 80)

	if hint != "Reviewing approval request: command: git status --short" {
		t.Fatalf("approvalReviewPendingHint() = %q, want command detail", hint)
	}
	if strings.Contains(hint, "UNKNOWN") {
		t.Fatalf("approvalReviewPendingHint() = %q, should not expose UNKNOWN", hint)
	}
}

func TestApprovalReviewPendingHintMapsToolNameToDisplayLabel(t *testing.T) {
	t.Parallel()

	hint := approvalReviewPendingHint("RUN_COMMAND", nil, 80)

	if hint != "Reviewing approval request: Ran" {
		t.Fatalf("approvalReviewPendingHint() = %q, want display label", hint)
	}
	if strings.Contains(hint, "RUN_COMMAND") {
		t.Fatalf("approvalReviewPendingHint() = %q, should not expose raw tool name", hint)
	}
}

func TestApprovalReviewPendingHintTruncatesToSingleLineBudget(t *testing.T) {
	t.Parallel()

	hint := approvalReviewPendingHint("RUN_COMMAND", map[string]any{
		"command": "printf 'first line'\nprintf 'second line'\nprintf 'third line'",
	}, 42)

	if strings.ContainsAny(hint, "\r\n") {
		t.Fatalf("approvalReviewPendingHint() = %q, should stay single-line", hint)
	}
	if displayColumns(hint) > 42 {
		t.Fatalf("approvalReviewPendingHint() width = %d, want <= 42: %q", displayColumns(hint), hint)
	}
	if !strings.Contains(hint, "...") {
		t.Fatalf("approvalReviewPendingHint() = %q, want ellipsis for truncated command", hint)
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
