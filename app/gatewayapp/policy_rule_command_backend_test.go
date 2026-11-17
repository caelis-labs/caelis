package gatewayapp

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/toolpolicy"
)

func TestPolicyRuleCommandsUseConfigurationCASAndOriginalOperation(t *testing.T) {
	ctx := t.Context()
	stack, _ := newLocalStateTestStack(t)
	commands, ok := stack.ConfigurationCommands().(appserver.PolicyRuleCommandService)
	if !ok {
		t.Fatal("configuration command owner lacks PolicyRule capability")
	}
	principal := appserver.Principal{ID: stack.composition.authorities.userID}
	expected, err := stack.ControlStatus().ConfigurationRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	rule := toolpolicy.Rule{ID: "approval-rule", DecisionID: "approval-original", Scope: session.AuthorizationScope{Authority: principal.ID, Workspace: t.TempDir(), SourceID: "fixture/source", SourceRevision: "source-v1", PolicyRevision: "policy-v1", Tool: "docs/read"}}
	req := appserver.PolicyRuleRequest{WriteBase: appserver.WriteBase{OperationID: "approval-original-policy", ExpectedRevision: &expected}, Rule: rule}
	committed, err := commands.SetPolicyRule(ctx, principal, req)
	if err != nil || committed.Outcome != appserver.OutcomeCommitted || committed.Revision != expected+1 {
		t.Fatalf("commit: %+v %v", committed, err)
	}
	replayed, err := commands.SetPolicyRule(ctx, principal, req)
	if err != nil || replayed != committed {
		t.Fatalf("replay: %+v %v", replayed, err)
	}
	changed := req
	changed.Rule.Scope.SourceRevision = "changed"
	if _, err := commands.SetPolicyRule(ctx, principal, changed); !errors.Is(err, appserver.ErrOperationConflict) {
		t.Fatalf("changed original operation: %v", err)
	}
	req.OperationID = "stale-rule-command"
	req.Remove = true
	if result, err := commands.SetPolicyRule(ctx, principal, req); err == nil || result.Outcome != appserver.OutcomeConflicted {
		t.Fatalf("stale CAS: %+v %v", result, err)
	}
	doc, err := stack.composition.authorities.store.LoadContext(ctx)
	if err != nil || len(doc.PolicyRules) != 1 || doc.PolicyRules[0] != rule {
		t.Fatalf("readback: %+v %v", doc.PolicyRules, err)
	}
	current := doc.ConfigurationRevision
	req.OperationID = "remove-rule"
	req.ExpectedRevision = &current
	if _, err := commands.SetPolicyRule(ctx, principal, req); err != nil {
		t.Fatal(err)
	}
	doc, err = stack.composition.authorities.store.LoadContext(ctx)
	if err != nil || len(doc.PolicyRules) != 1 || !doc.PolicyRules[0].Revoked {
		t.Fatalf("remove readback: %+v %v", doc.PolicyRules, err)
	}
}
