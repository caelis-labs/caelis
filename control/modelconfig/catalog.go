package modelconfig

import (
	"strings"

	"github.com/caelis-labs/caelis/control/modelcatalog"
)

// catalogProviderFor returns the native connection's capability namespace.
// Gateway and custom connections keep their own identity.
func catalogProviderFor(provider string) string {
	if template, ok := LookupProvider(provider); ok {
		return template.Provider
	}
	return strings.TrimSpace(provider)
}

// ReasoningLevelsForConfig returns endpoint-scoped catalog reasoning choices.
func ReasoningLevelsForConfig(cfg Config) []string {
	if cfg.ReasoningMode == modelcatalog.ReasoningModeNone {
		return nil
	}
	if cfg.ReasoningLevels != nil {
		return NormalizeReasoningLevels(cfg.ReasoningLevels)
	}
	return NormalizeReasoningLevels(modelcatalog.ReasoningLevelsForModel(
		catalogProviderFor(cfg.Provider),
		cfg.Model,
	))
}

// DefaultReasoningEffortForConfig returns the endpoint-scoped catalog default.
func DefaultReasoningEffortForConfig(cfg Config) string {
	if cfg.DefaultReasoningEffort != "" || cfg.ReasoningEffort != "" {
		return modelcatalog.NormalizeReasoningEffort(firstNonEmpty(cfg.DefaultReasoningEffort, cfg.ReasoningEffort))
	}
	return modelcatalog.DefaultReasoningEffortForModel(
		catalogProviderFor(cfg.Provider),
		cfg.Model,
	)
}

// ModelSupportsImages reports endpoint-scoped catalog image-input support.
// Accepted configuration capabilities remain stable across directory updates.
func ModelSupportsImages(cfg Config) bool {
	capability := ModelImageInput(cfg)
	return capability != nil && *capability
}

// ModelImageInput returns the same endpoint-scoped declaration used for model
// assembly. Nil means unknown, distinct from an explicit text-only declaration.
// Saved capabilities take precedence over current onboarding defaults.
func ModelImageInput(cfg Config) *bool {
	if cfg.ImageInput != nil {
		value := *cfg.ImageInput
		return &value
	}
	if IsJudgment(cfg) {
		return boolPointer(false)
	}
	if defaults, err := ResolveModelDefaultsForEndpoint(cfg.Provider, cfg.BaseURL, cfg.Model); err == nil {
		if defaults.ImageInput != nil {
			return defaults.ImageInput
		}
	}
	return RestoreSavedCapabilities(cfg).ImageInput
}

func selectableModelDetail(provider string, baseURL string, modelName string) string {
	template, ok := LookupProvider(provider)
	if !ok {
		return "suggested model"
	}
	catalogProvider := catalogProviderFor(template.Provider)
	caps, known := modelcatalog.LookupModelCapabilities(catalogProvider, modelName)
	if !known {
		return "suggested model"
	}
	prefix := "native preset"
	parts := []string{prefix}
	if caps.SupportsReasoning {
		parts = append(parts, "reasoning")
	}
	if caps.SupportsToolCalls {
		parts = append(parts, "tools")
	}
	return strings.Join(parts, " · ")
}
