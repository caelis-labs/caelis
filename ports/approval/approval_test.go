package approval

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/ports/agent"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/tool"
)

func TestPayloadFromRuntimeRequestUsesProtocolApprovalFirst(t *testing.T) {
	req := agent.ApprovalRequest{
		Tool: tool.Definition{Name: "custom_tool"},
		Call: tool.Call{ID: "call-from-runtime", Name: "custom_tool"},
		Approval: &session.ProtocolApproval{
			ToolCall: session.ProtocolToolCall{
				ID:   "call-from-protocol",
				Name: "RUN_COMMAND",
				RawInput: map[string]any{
					"command":             "git restore hello.py",
					"approval_reason":     "destructive edit",
					"sandbox_permissions": "workspace_write",
				},
			},
			Options: []session.ProtocolApprovalOption{
				{ID: " allow_once ", Name: " Allow once ", Kind: " allow_once "},
				{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
			},
		},
	}

	payload := PayloadFromRuntimeRequest(req)
	if payload == nil {
		t.Fatal("PayloadFromRuntimeRequest() = nil, want payload")
		return
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
	if len(payload.Options) != 2 || payload.Options[0].ID != "allow_once" {
		t.Fatalf("payload options = %#v, want protocol options", payload.Options)
	}
}

func TestNormalizeOptionsAndOptionIDs(t *testing.T) {
	options := NormalizeOptions([]Option{
		{ID: " allow_once ", Name: " Allow once ", Kind: " allow_once "},
		{ID: "allow_once", Name: "Duplicate", Kind: "allow"},
		{ID: " reject_once ", Name: " Reject once ", Kind: " reject_once "},
		{},
	})

	if len(options) != 3 {
		t.Fatalf("NormalizeOptions() len = %d, want 3: %#v", len(options), options)
	}
	if options[0] != (Option{ID: "allow_once", Name: "Allow once", Kind: "allow_once"}) {
		t.Fatalf("NormalizeOptions()[0] = %#v, want trimmed option", options[0])
	}
	ids := OptionIDs(options)
	if len(ids) != 2 || ids[0] != "allow_once" || ids[1] != "reject_once" {
		t.Fatalf("OptionIDs() = %#v, want de-duplicated ids", ids)
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
		return
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

func TestReviewErrorOutcome(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus ReviewStatus
		wantText   string
		wantOK     bool
	}{
		{name: "nil", wantOK: false},
		{name: "deadline", err: context.DeadlineExceeded, wantStatus: ReviewStatusTimedOut, wantText: "timed out", wantOK: true},
		{name: "cancelled", err: context.Canceled, wantStatus: ReviewStatusFailed, wantText: "cancelled", wantOK: true},
		{name: "failed", err: errors.New("model failed"), wantStatus: ReviewStatusFailed, wantText: "model failed", wantOK: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, rationale, ok := ReviewErrorOutcome(tc.err)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", status, tc.wantStatus)
			}
			if tc.wantText != "" && !strings.Contains(rationale, tc.wantText) {
				t.Fatalf("rationale = %q, want %q", rationale, tc.wantText)
			}
		})
	}
}

func TestRuntimeResponseFromReviewValidOptionOverridesOutcome(t *testing.T) {
	payload := &Payload{
		Options: []Option{
			{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
			{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
		},
	}
	resp := RuntimeResponseFromReview(payload, ReviewResult{
		Approved:  true,
		Outcome:   string(StatusApproved),
		OptionID:  "reject_once",
		Rationale: "selected reject option",
	})

	if resp.Approved || resp.Outcome != string(StatusSelected) || resp.OptionID != "reject_once" {
		t.Fatalf("response = %#v, want selected reject_once denial", resp)
	}
}

func TestRuntimeResponseFromReviewInvalidOptionFallsBackToOutcome(t *testing.T) {
	payload := &Payload{
		Options: []Option{
			{ID: "allow_once", Name: "Allow once", Kind: "allow_once"},
			{ID: "reject_once", Name: "Reject once", Kind: "reject_once"},
		},
	}
	resp := RuntimeResponseFromReview(payload, ReviewResult{
		Outcome:  "allow",
		OptionID: "not-real",
	})

	if !resp.Approved || resp.Outcome != string(StatusSelected) || resp.OptionID != "allow_once" {
		t.Fatalf("response = %#v, want fallback selected allow_once", resp)
	}
}
