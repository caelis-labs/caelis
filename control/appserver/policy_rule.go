package appserver

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/control/toolpolicy"
)

// ActionPolicyRule changes one explicit persistent source-consent rule.
const ActionPolicyRule Action = "configuration.policy.rule"

// PolicyRuleRequest uses the existing configuration CAS and operation ledger.
// Remove tombstones Rule.ID; creation retains the original approval DecisionID.
type PolicyRuleRequest struct {
	WriteBase
	Rule   toolpolicy.Rule `json:"rule"`
	Remove bool            `json:"remove,omitempty"`
}

// PolicyRuleCommandService is the focused persistent Policy configuration owner.
type PolicyRuleCommandService interface {
	SetPolicyRule(context.Context, Principal, PolicyRuleRequest) (CommandResult, error)
}

// SetPolicyRule persists an explicit rule under the principal-bound ledger.
func (s *CommandService) SetPolicyRule(ctx context.Context, p Principal, req PolicyRuleRequest) (CommandResult, error) {
	return s.execute(ctx, p, ActionPolicyRule, req.WriteBase, "host/configuration/policy/"+req.Rule.ID, req)
}

func validatePolicyRuleRequest(req PolicyRuleRequest) error {
	if err := validateHostModelWrite(req.WriteBase); err != nil {
		return err
	}
	if req.Rule.ID == "" {
		return errors.New("policy rule identity is required")
	}
	if req.Remove {
		return nil
	}
	return toolpolicy.ValidateRules([]toolpolicy.Rule{req.Rule})
}
