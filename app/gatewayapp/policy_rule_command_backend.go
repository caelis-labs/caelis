package gatewayapp

import (
	"context"
	"errors"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/toolpolicy"
)

func (s *controlCommandBackend) executePolicyRuleCommand(ctx context.Context, action appserver.Action, req appserver.PolicyRuleRequest) (appserver.CommandResult, error) {
	if action != appserver.ActionPolicyRule {
		return appserver.CommandResult{}, configurationRejectedError(errorcode.New(errorcode.InvalidArgument, "policy rule request/action mismatch"))
	}
	if s == nil || s.composition == nil || s.composition.authorities.store == nil {
		return appserver.CommandResult{}, errors.New("policy configuration is unavailable")
	}
	doc, err := s.composition.authorities.store.LoadContext(ctx)
	if err != nil {
		return appserver.CommandResult{}, classifyConfigurationPreEffectError(err)
	}
	expected := expectedConfigurationRevision(req.ExpectedRevision)
	if doc.ConfigurationRevision != expected {
		return configurationCommandResult(doc.ConfigurationRevision), classifyConfigurationPreEffectError(&configstore.ConfigurationRevisionConflict{Expected: expected, Actual: doc.ConfigurationRevision})
	}
	rules, err := toolpolicy.SetRule(doc.PolicyRules, req.Rule, req.Remove)
	if err != nil {
		return configurationCommandResult(doc.ConfigurationRevision), configurationRejectedError(errorcode.Wrap(errorcode.InvalidArgument, "invalid policy rule", err))
	}
	if slices.Equal(rules, doc.PolicyRules) {
		return configurationCommandResult(doc.ConfigurationRevision), nil
	}
	doc.PolicyRules = rules
	saved, err := s.composition.authorities.store.CompareAndSave(ctx, expected, doc)
	result := configurationCommandResult(configurationErrorRevision(err, saved.ConfigurationRevision))
	if err != nil {
		// Preserve the committed revision for exact RuleID/operation readback.
		// An uncertain write cannot authorize the pending tool invocation.
		return result, classifyConfigurationEffectError(err)
	}
	return result, nil
}
