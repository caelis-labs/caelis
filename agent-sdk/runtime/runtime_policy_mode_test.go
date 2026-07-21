package runtime

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestPolicyModelFeedbackDistinguishesResolvedDenialFromPendingApproval(t *testing.T) {
	t.Parallel()

	decision := policy.Decision{Action: policy.ActionAskApproval, Reason: "host execution requested"}
	denied, deniedHint := policyModelFeedback(decision, agent.ApprovalResponse{
		Outcome:  "selected",
		OptionID: "reject_once",
	})
	if denied != "approval denied" || deniedHint != "" {
		t.Fatalf("resolved denial feedback = (%q, %q), want denial without pending hint", denied, deniedHint)
	}

	pending, pendingHint := policyModelFeedback(decision, agent.ApprovalResponse{})
	if pending != "host execution requested" || pendingHint == "" {
		t.Fatalf("pending approval feedback = (%q, %q), want request reason and retry hint", pending, pendingHint)
	}
}

func TestModeOptionsFromSessionReadsPolicyNetworkEnabledMetadata(t *testing.T) {
	t.Parallel()

	opts := modeOptionsFromSession(session.Session{}, agent.AgentSpec{
		Metadata: map[string]any{"policy_network_enabled": false},
	})
	if opts.NetworkEnabled == nil || *opts.NetworkEnabled {
		t.Fatalf("NetworkEnabled = %#v, want false from metadata", opts.NetworkEnabled)
	}

	opts = modeOptionsFromSession(session.Session{}, agent.AgentSpec{
		Metadata: map[string]any{"policy_network_enabled": "on"},
	})
	if opts.NetworkEnabled == nil || !*opts.NetworkEnabled {
		t.Fatalf("NetworkEnabled = %#v, want true from string metadata", opts.NetworkEnabled)
	}
}
