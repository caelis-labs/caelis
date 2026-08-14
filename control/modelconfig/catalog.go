package modelconfig

import (
	"strings"

	"github.com/caelis-labs/caelis/control/modelcatalog"
)

// CatalogProviderFor resolves the capability-catalog namespace owned by one
// provider endpoint. Providers without an endpoint override use their normal
// product provider identity.
func CatalogProviderFor(provider string, baseURL string) string {
	provider = strings.TrimSpace(provider)
	template, ok := LookupProvider(provider)
	if !ok {
		return provider
	}
	if endpoint, found := EndpointForBaseURL(template, baseURL); found {
		if catalogProvider := strings.TrimSpace(endpoint.CatalogProvider); catalogProvider != "" {
			return catalogProvider
		}
	}
	return template.Provider
}

// ReasoningLevelsForConfig returns endpoint-scoped catalog reasoning choices.
func ReasoningLevelsForConfig(cfg Config) []string {
	return NormalizeReasoningLevels(modelcatalog.ReasoningLevelsForModel(
		CatalogProviderFor(cfg.Provider, cfg.BaseURL),
		cfg.Model,
	))
}

// DefaultReasoningEffortForConfig returns the endpoint-scoped catalog default.
func DefaultReasoningEffortForConfig(cfg Config) string {
	return modelcatalog.DefaultReasoningEffortForModel(
		CatalogProviderFor(cfg.Provider, cfg.BaseURL),
		cfg.Model,
	)
}

// ModelSupportsImages reports endpoint-scoped catalog image-input support.
// Maintained metadata is authoritative; an explicit config value is used only
// when the directory has no declaration for the selected model.
func ModelSupportsImages(cfg Config) bool {
	if defaults, err := ResolveModelDefaultsForEndpoint(cfg.Provider, cfg.BaseURL, cfg.Model); err == nil &&
		defaults.ImageInput != nil {
		return *defaults.ImageInput
	}
	if cfg.ImageInput != nil {
		return *cfg.ImageInput
	}
	return false
}

func selectableModelDetail(provider string, baseURL string, modelName string) string {
	template, ok := LookupProvider(provider)
	if !ok {
		return "suggested model"
	}
	catalogProvider := CatalogProviderFor(template.Provider, baseURL)
	caps, known := modelcatalog.LookupModelCapabilities(catalogProvider, modelName)
	if !known {
		return "suggested model"
	}
	prefix := "catalog preset"
	if template.UseModelDirectory {
		prefix = "model directory"
	} else if endpoint, found := EndpointForBaseURL(template, baseURL); found &&
		strings.TrimSpace(endpoint.CatalogProvider) != "" &&
		strings.TrimSpace(endpoint.Display) != "" {
		prefix = strings.TrimSpace(endpoint.Display)
	}
	parts := []string{prefix}
	if caps.SupportsReasoning {
		parts = append(parts, "reasoning")
	}
	if caps.SupportsToolCalls {
		parts = append(parts, "tools")
	}
	return strings.Join(parts, " · ")
}
