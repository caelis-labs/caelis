package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/toolpolicy"
)

func (s *runtimeComposition) policyRules(ctx context.Context) ([]toolpolicy.Rule, func(), error) {
	store := s.authorities.store
	if err := store.inner.SetPathContext(ctx, store.path); err != nil {
		return nil, nil, err
	}
	doc, release, err := store.inner.ReadForAdmission(ctx)
	if err != nil {
		return nil, nil, err
	}
	return slices.Clone(doc.PolicyRules), release, nil
}

func (s *runtimeComposition) persistPolicyConsent(ctx context.Context, decisionID string, scope session.AuthorizationScope) (string, uint64, error) {
	if scope.Authority != s.authorities.userID {
		return "", 0, errors.New("policy rule has a different configuration owner")
	}
	commands := s.authorities.policyRuleCommands
	if commands == nil {
		return "", 0, errors.New("policy configuration command owner is unavailable")
	}
	doc, err := s.authorities.store.LoadContext(ctx)
	if err != nil {
		return "", 0, err
	}
	rule := toolpolicy.Rule{ID: "approval:" + decisionID, DecisionID: decisionID, Scope: scope}
	for _, existing := range doc.PolicyRules {
		if existing.ID == rule.ID {
			if existing.Revoked || existing != rule {
				return "", doc.ConfigurationRevision, errors.New("original Policy rule identity has different content")
			}
			return rule.ID, doc.ConfigurationRevision, nil
		}
	}
	result, err := commands.SetPolicyRule(ctx, appserver.Principal{ID: s.authorities.userID}, appserver.PolicyRuleRequest{WriteBase: appserver.WriteBase{OperationID: "approval-policy:" + decisionID, ExpectedRevision: &doc.ConfigurationRevision}, Rule: rule})
	if err != nil {
		if result.Outcome == appserver.OutcomeCommitted {
			return rule.ID, result.Revision, fmt.Errorf("policy rule %s committed at configuration revision %d, operation approval-policy:%s; post-commit settlement failed: %w", rule.ID, result.Revision, decisionID, err)
		}
		return rule.ID, result.Revision, fmt.Errorf("rule %s operation approval-policy:%s outcome %s configuration revision %d: %w", rule.ID, decisionID, result.Outcome, result.Revision, err)
	}
	if result.Outcome != appserver.OutcomeCommitted {
		return rule.ID, result.Revision, errors.New("policy rule commit is unconfirmed")
	}
	return rule.ID, result.Revision, nil
}
