package approval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

func TestAskToolsOnlyReviewsSelectedTools(t *testing.T) {
	policy := AskTools("write_file")

	selected, err := policy.ReviewToolCall(context.Background(), Request{
		Call: model.ToolCall{Name: "WRITE_FILE"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Verdict != VerdictAsk {
		t.Fatalf("selected verdict = %q, want ask", selected.Verdict)
	}

	ignored, err := policy.ReviewToolCall(context.Background(), Request{
		Call: model.ToolCall{Name: "run_command"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if ignored.Verdict != "" {
		t.Fatalf("ignored verdict = %q, want no review", ignored.Verdict)
	}
}

func TestWithSessionModeManualAsksForEveryTool(t *testing.T) {
	policy := WithSessionMode(AskTools("write_file"))

	manual, err := policy.ReviewToolCall(context.Background(), Request{
		Mode: ModeManual,
		Call: model.ToolCall{Name: "read_file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if manual.Verdict != VerdictAsk {
		t.Fatalf("manual verdict = %q, want ask", manual.Verdict)
	}

	auto, err := policy.ReviewToolCall(context.Background(), Request{
		Mode: ModeAutoReview,
		Call: model.ToolCall{Name: "read_file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if auto.Verdict != "" {
		t.Fatalf("auto-review verdict = %q, want base policy to ignore read_file", auto.Verdict)
	}
}

func TestWithSessionModeUsesRememberedToolDecisions(t *testing.T) {
	state := session.State{}
	if !RememberToolDecision(state, "write_file", OptionAllowAlways, "trusted for this session") {
		t.Fatal("RememberToolDecision allow_always = false")
	}
	policy := WithSessionMode(AskTools("write_file"))

	remembered, err := policy.ReviewToolCall(context.Background(), Request{
		Mode:  ModeManual,
		State: state,
		Call:  model.ToolCall{Name: "WRITE_FILE"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if remembered.Verdict != VerdictAllow || remembered.Reason != "trusted for this session" {
		t.Fatalf("remembered allow = %#v, want allow decision", remembered)
	}

	if !RememberToolDecision(state, "patch_file", OptionRejectAlways, "do not patch") {
		t.Fatal("RememberToolDecision reject_always = false")
	}
	rejected, err := policy.ReviewToolCall(context.Background(), Request{
		Mode:  ModeAutoReview,
		State: state,
		Call:  model.ToolCall{Name: "patch_file"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if rejected.Verdict != VerdictDeny || rejected.Reason != "do not patch" {
		t.Fatalf("remembered reject = %#v, want deny decision", rejected)
	}
}

func TestWithSandboxEscalationRequiresApprovalForEscalatedTool(t *testing.T) {
	raw := mustApprovalJSON(t, map[string]any{
		"command":             "git commit -m test",
		"sandbox_permissions": "require_escalated",
		"justification":       "create the requested commit",
	})
	policy := WithSandboxEscalation(AllowAll())

	decision, err := policy.ReviewToolCall(context.Background(), Request{
		Call: model.ToolCall{Name: "run_command", Input: raw},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != VerdictAsk {
		t.Fatalf("decision = %#v, want ask", decision)
	}
	if !strings.Contains(decision.Reason, "create the requested commit") {
		t.Fatalf("reason = %q, want justification", decision.Reason)
	}
	for _, option := range decision.Options {
		if option.ID == OptionAllowAlways || option.ID == OptionRejectAlways {
			t.Fatalf("options = %#v, want one-shot escalation choices", decision.Options)
		}
	}
}

func TestWithSandboxEscalationOverridesRememberedAllow(t *testing.T) {
	state := session.State{}
	if !RememberToolDecision(state, "run_command", OptionAllowAlways, "trusted command") {
		t.Fatal("RememberToolDecision allow_always = false")
	}
	raw := mustApprovalJSON(t, map[string]any{
		"command":             "git tag v0.0.0",
		"sandbox_permissions": "require_escalated",
		"justification":       "tag the exact commit",
	})
	policy := WithSandboxEscalation(WithSessionMode(AllowAll()))

	decision, err := policy.ReviewToolCall(context.Background(), Request{
		Mode:  ModeAutoReview,
		State: state,
		Call:  model.ToolCall{Name: "run_command", Input: raw},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != VerdictAsk {
		t.Fatalf("decision = %#v, want escalation to ask despite remembered allow", decision)
	}
	if strings.Contains(decision.Reason, "trusted command") || !strings.Contains(decision.Reason, "host execution") {
		t.Fatalf("reason = %q, want escalation reason instead of remembered allow reason", decision.Reason)
	}
}

func TestWithSandboxEscalationDeniesMissingJustification(t *testing.T) {
	raw := mustApprovalJSON(t, map[string]any{
		"command":             "git commit -m test",
		"sandbox_permissions": "require_escalated",
	})
	policy := WithSandboxEscalation(AllowAll())

	decision, err := policy.ReviewToolCall(context.Background(), Request{
		Call: model.ToolCall{Name: "run_command", Input: raw},
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Verdict != VerdictDeny || !strings.Contains(decision.Reason, "justification") {
		t.Fatalf("decision = %#v, want denial for missing justification", decision)
	}
}

func mustApprovalJSON(t *testing.T, value map[string]any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
