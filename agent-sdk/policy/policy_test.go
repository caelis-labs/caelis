package policy_test

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/policy"
	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestThirdPartyPolicyFailsClosedWithoutDecisionFunction(t *testing.T) {
	t.Parallel()

	_, err := (policy.NamedMode{ID: "third-party"}).DecideTool(context.Background(), policy.ToolContext{})
	var decisionErr *policy.DecisionError
	if !errors.As(err, &decisionErr) {
		t.Fatalf("DecideTool() error = %v, want *policy.DecisionError", err)
	}
}

func TestThirdPartyPolicyDecisionConformance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		decision policy.Decision
		wantErr  bool
	}{
		{name: "explicit allow", decision: policy.Decision{Action: policy.ActionAllow}},
		{name: "explicit deny", decision: policy.Decision{Action: policy.ActionDeny}},
		{name: "approval", decision: policy.Decision{Action: policy.ActionAskApproval}},
		{name: "empty action", decision: policy.Decision{}, wantErr: true},
		{name: "unknown action", decision: policy.Decision{Action: "continue"}, wantErr: true},
		{
			name: "invalid constraints",
			decision: policy.Decision{
				Action: policy.ActionAllow,
				Constraints: sandbox.Constraints{
					Network: "sometimes",
				},
			},
			wantErr: true,
		},
		{
			name: "unsupported read-only path constraint",
			decision: policy.Decision{
				Action: policy.ActionAllow,
				Constraints: sandbox.Constraints{
					PathRules: []sandbox.PathRule{{Path: "/toolchain", Access: sandbox.PathAccess("read_only")}},
				},
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := policy.NormalizeDecision("third-party", tt.decision)
			if (err != nil) != tt.wantErr {
				t.Fatalf("NormalizeDecision() error = %v, wantErr %v", err, tt.wantErr)
			}
			if tt.wantErr {
				var decisionErr *policy.DecisionError
				if !errors.As(err, &decisionErr) {
					t.Fatalf("NormalizeDecision() error = %T, want *policy.DecisionError", err)
				}
			}
		})
	}
}

func TestPolicyCloneContractsIsolateNestedValues(t *testing.T) {
	t.Parallel()

	input := policy.ToolContext{State: map[string]any{
		"nested": map[string]any{"mode": "chat"},
	}}
	cloned := policy.CloneToolContext(input)
	cloned.State["nested"].(map[string]any)["mode"] = "mutated"
	if got := input.State["nested"].(map[string]any)["mode"]; got != "chat" {
		t.Fatalf("CloneToolContext() leaked nested mutation: %v", got)
	}

	original := policy.Decision{Metadata: map[string]any{
		"nested": []any{map[string]any{"approved": true}},
	}}
	decision, err := policy.CloneDecision(original, nil)
	if err != nil {
		t.Fatalf("CloneDecision() error = %v", err)
	}
	decision.Metadata["nested"].([]any)[0].(map[string]any)["approved"] = false
	if got := original.Metadata["nested"].([]any)[0].(map[string]any)["approved"]; got != true {
		t.Fatalf("CloneDecision() leaked nested mutation: %v", got)
	}
}
