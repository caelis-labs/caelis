// Package toolpolicy owns product tool authorization rules shared by ordinary
// and application Sessions. Runtime consumes its final Policy decision.
package toolpolicy

import (
	"errors"
	"fmt"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// Rule is an explicit persistent source-consent rule in AppConfig. It satisfies
// only this exact source requirement, never an independent per-call restriction.
// DecisionID preserves the original approval operation through uncertain writes.
type Rule struct {
	// Revoked retains the original rule identity so a late approval or uncertain
	// command cannot recreate a removed permission under that same identity.
	Revoked    bool                       `json:"revoked,omitempty"`
	ID         string                     `json:"id"`
	DecisionID string                     `json:"decision_id"`
	Scope      session.AuthorizationScope `json:"scope"`
}

// ValidateRules checks configuration without broadening or repairing a scope.
func ValidateRules(rules []Rule) error {
	seen := make(map[string]bool, len(rules))
	for _, rule := range rules {
		if rule.ID == "" || (!rule.Revoked && rule.DecisionID == "") || seen[rule.ID] {
			return errors.New("policy rule requires unique identity and original decision identity")
		}
		seen[rule.ID] = true
		if rule.Revoked {
			continue
		}
		if err := rule.Scope.Validate(); err != nil {
			return fmt.Errorf("policy rule %q: %w", rule.ID, err)
		}
		if rule.Scope.SessionID != "" || rule.Scope.Tool == "" {
			return errors.New("persistent policy rules require an exact tool scope without Session or resource authority")
		}
	}
	return nil
}

// SetRule applies one idempotent configuration mutation. An existing ID cannot
// be reused with changed decision or scope bytes. Removal is explicit.
func SetRule(rules []Rule, rule Rule, remove bool) ([]Rule, error) {
	out := slices.Clone(rules)
	for i, existing := range out {
		if existing.ID != rule.ID {
			continue
		}
		if remove {
			out[i].Revoked = true
			return out, nil
		}
		if existing != rule {
			return nil, errors.New("policy rule identity already has different content")
		}
		return out, nil
	}
	if remove {
		if rule.ID == "" {
			return nil, errors.New("policy rule identity is required")
		}
		return append(out, Rule{ID: rule.ID, Revoked: true}), nil
	}
	out = append(out, rule)
	if err := ValidateRules(out); err != nil {
		return nil, err
	}
	return out, nil
}
