package approval

import (
	"fmt"
	"strings"
)

// OptionDecision is the normalized executable meaning of one approval option.
// Strict consumers must derive it from the canonical option kind, never from
// human-facing names or identifier spelling.
type OptionDecision string

const (
	OptionDecisionAllow OptionDecision = "allow"
	OptionDecisionDeny  OptionDecision = "deny"
)

// ValidateStrictOptions verifies that every option has one unique identifier
// and one canonical allow/deny kind. An empty option set is valid for
// approval protocols that return an outcome without selecting an option.
func ValidateStrictOptions(options []Option) error {
	_, err := normalizeStrictOptions(options)
	return err
}

// StrictOptionIDs returns validated option identifiers in encounter order.
func StrictOptionIDs(options []Option) ([]string, error) {
	normalized, err := normalizeStrictOptions(options)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(normalized))
	for _, option := range normalized {
		ids = append(ids, option.ID)
	}
	return ids, nil
}

// ResolveStrictOption resolves one exact option identifier and returns its
// canonical executable decision. It does not infer semantics from option names
// or identifiers.
func ResolveStrictOption(options []Option, optionID string) (Option, OptionDecision, error) {
	normalized, err := normalizeStrictOptions(options)
	if err != nil {
		return Option{}, "", err
	}
	optionID = strings.TrimSpace(optionID)
	if optionID == "" {
		return Option{}, "", fmt.Errorf("approval: selected option id is required")
	}
	for _, option := range normalized {
		if option.ID == optionID {
			decision, _ := strictOptionDecision(option.Kind)
			return option, decision, nil
		}
	}
	return Option{}, "", fmt.Errorf("approval: selected option %q is not available", optionID)
}

// StrictOptionIDForDecision returns the first validated option with the given
// executable meaning. The boolean is false when the option set is valid but
// does not contain that decision.
func StrictOptionIDForDecision(options []Option, decision OptionDecision) (string, bool, error) {
	if decision != OptionDecisionAllow && decision != OptionDecisionDeny {
		return "", false, fmt.Errorf("approval: unsupported option decision %q", decision)
	}
	normalized, err := normalizeStrictOptions(options)
	if err != nil {
		return "", false, err
	}
	for _, option := range normalized {
		resolved, _ := strictOptionDecision(option.Kind)
		if resolved == decision {
			return option.ID, true, nil
		}
	}
	return "", false, nil
}

func normalizeStrictOptions(options []Option) ([]Option, error) {
	if len(options) == 0 {
		return nil, nil
	}
	out := make([]Option, 0, len(options))
	seen := make(map[string]struct{}, len(options))
	for index, option := range options {
		normalized := Option{
			ID:   strings.TrimSpace(option.ID),
			Name: strings.TrimSpace(option.Name),
			Kind: strings.ToLower(strings.TrimSpace(option.Kind)),
		}
		if normalized.ID == "" {
			return nil, fmt.Errorf("approval: option %d has no id", index)
		}
		if _, exists := seen[normalized.ID]; exists {
			return nil, fmt.Errorf("approval: option id %q is duplicated", normalized.ID)
		}
		seen[normalized.ID] = struct{}{}
		if _, ok := strictOptionDecision(normalized.Kind); !ok {
			return nil, fmt.Errorf("approval: option %q has unsupported kind %q", normalized.ID, normalized.Kind)
		}
		out = append(out, normalized)
	}
	return out, nil
}

func strictOptionDecision(kind string) (OptionDecision, bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "allow", "allow_once", "allow_always":
		return OptionDecisionAllow, true
	case "deny", "deny_once", "deny_always", "reject", "reject_once", "reject_always":
		return OptionDecisionDeny, true
	default:
		return "", false
	}
}
