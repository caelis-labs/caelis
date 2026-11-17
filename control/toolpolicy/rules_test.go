package toolpolicy

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestPolicyRuleMutationPreservesExactOriginalDecision(t *testing.T) {
	rule := Rule{ID: "rule-approval-1", DecisionID: "approval-1", Scope: session.AuthorizationScope{Authority: "host", Workspace: "/a", SourceID: "source", SourceRevision: "v1", PolicyRevision: "p1", Tool: "docs/read"}}
	rules, err := SetRule(nil, rule, false)
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := SetRule(rules, rule, false)
	if err != nil || !reflect.DeepEqual(rules, repeated) {
		t.Fatalf("repeat: %v %v", repeated, err)
	}
	changed := rule
	changed.Scope.SourceRevision = "v2"
	if _, err := SetRule(rules, changed, false); err == nil {
		t.Fatal("same ID changed source")
	}
	changed = rule
	changed.Scope.SessionID, changed.Scope.SessionIncarnation = "s", "i"
	if err := ValidateRules([]Rule{changed}); err == nil {
		t.Fatal("Session authority became permanent")
	}
	changed = rule
	changed.Scope.Tool = ""
	changed.Scope.ResourceKind, changed.Scope.Resource = "application", "app"
	if err := ValidateRules([]Rule{changed}); err == nil {
		t.Fatal("APP authority became permanent")
	}
	removed, err := SetRule(rules, Rule{ID: rule.ID}, true)
	if err != nil || len(removed) != 1 || !removed[0].Revoked {
		t.Fatalf("remove: %v %v", removed, err)
	}
	if len(rules) != 1 {
		t.Fatal("mutation aliased input")
	}
}
