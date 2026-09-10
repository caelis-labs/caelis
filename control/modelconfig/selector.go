package modelconfig

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ErrAmbiguousSelector reports a selection that names more than one configured
// model. Callers must not select a default or the first matching route.
var ErrAmbiguousSelector = errors.New("ambiguous model selector")

// PublicSelector returns a stable user-facing selector for a hydrated config.
// Standard aliases use provider[@endpoint]/model, omitting only @default.
// Custom aliases retain their fully qualified config ID so distinct configured
// variants of the same upstream model never collapse into one selection.
// This value is not a replacement for durable config or ModelProfile IDs.
func PublicSelector(cfg Config) string {
	if !strings.EqualFold(strings.TrimSpace(cfg.Alias), BuildAlias(cfg.Provider, cfg.Model)) {
		return cfg.ID
	}
	route := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(cfg.ProviderEndpointID)), "@default")
	if route == "" {
		return cfg.ID
	}
	return route + "/" + strings.ToLower(strings.TrimSpace(cfg.Model))
}

// ResolveSelector resolves an exact durable ID, public selector, or historical
// alias, in that order. Matches within each tier must identify one config;
// lower-priority aliases cannot shadow a public selector. In particular,
// provider/model selects the default endpoint when that public route exists.
// Historical aliases are input compatibility only, retained
// while existing client references are supported; removal requires an explicit
// breaking migration. Upstream model IDs are never parsed or rewritten.
func ResolveSelector(configs []Config, selector string) (Config, bool, error) {
	selector = strings.ToLower(strings.TrimSpace(selector))
	if selector == "" {
		return Config{}, false, nil
	}
	for _, cfg := range configs {
		if strings.EqualFold(cfg.ID, selector) {
			return cfg, true, nil
		}
	}
	matches := make(map[string]Config)
	for _, cfg := range configs {
		if strings.EqualFold(PublicSelector(cfg), selector) {
			matches[cfg.ID] = cfg
		}
	}
	if len(matches) == 0 {
		for _, cfg := range configs {
			if strings.EqualFold(strings.TrimSpace(cfg.Alias), selector) {
				matches[cfg.ID] = cfg
			}
		}
	}
	if len(matches) > 1 {
		ids := make([]string, 0, len(matches))
		for id := range matches {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return Config{}, false, fmt.Errorf("%w %q; use a fully qualified model ID: %s", ErrAmbiguousSelector, selector, strings.Join(ids, ", "))
	}
	for _, cfg := range matches {
		return cfg, true, nil
	}
	return Config{}, false, nil
}

// ValidatePublicSelectors checks that every published selector resolves back to
// its own config, including collisions with other public selectors or durable IDs.
// A conflicting catalog must not publish a value that can select another route.
func ValidatePublicSelectors(configs []Config) error {
	for _, cfg := range configs {
		selector := PublicSelector(cfg)
		resolved, ok, err := ResolveSelector(configs, selector)
		if err != nil {
			return err
		}
		if selector == "" || !ok || resolved.ID != cfg.ID {
			return fmt.Errorf("%w %q for model %q", ErrAmbiguousSelector, selector, cfg.ID)
		}
	}
	return nil
}
