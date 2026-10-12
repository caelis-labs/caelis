package modelconfig

import (
	"context"
	"fmt"
	"net/http"

	"github.com/caelis-labs/caelis/agent-sdk/model/providers"
	"github.com/caelis-labs/caelis/control/modelcatalog"
)

// Magpie is a connection/discovery preset. Its chat and judgment requests use
// existing SDK protocols; upstream IDs remain opaque to Caelis.
func selectableMagpieModels(ctx context.Context, template ProviderTemplate, baseURL string, client *http.Client, token string) ([]SelectableModel, error) {
	headers := map[string]string{}
	judgment := template.API == APISystemOne
	if judgment {
		headers["X-Magpie-Deciders"] = "1"
	}
	auth := providers.AuthConfig{Type: providers.AuthNone}
	if token != "" {
		auth = providers.AuthConfig{Type: providers.AuthAPIKey, Token: token}
	}
	remote, err := providers.DiscoverModels(ctx, providers.Config{Provider: "magpie", API: providers.APIOpenAIResponses, BaseURL: baseURL, HTTPClient: client, Auth: auth, Headers: headers})
	if err != nil {
		return nil, fmt.Errorf("modelconfig: discover Magpie models: %w", err)
	}
	out := make([]SelectableModel, 0, len(remote))
	for _, entry := range remote {
		if entry.Kind != "" && entry.Kind != "model" && entry.Kind != "chat" && entry.Kind != "decision" {
			continue
		}
		if (entry.Kind == "decision") != judgment {
			continue
		}
		defaults := ModelDefaults{ContextWindowTokens: entry.ContextWindowTokens, ImageInput: entry.ImageInput, ReasoningMode: modelcatalog.ReasoningModeNone}
		if defaults.ContextWindowTokens <= 0 {
			defaults.ContextWindowTokens = template.DefaultContextWindowTokens
		}
		if judgment {
			defaults.ContextWindowTokens, defaults.MaxOutputTokens, defaults.ImageInput = 32000, 4096, boolPointer(false)
		} else {
			defaults.ReasoningLevels = NormalizeReasoningLevels(entry.ReasoningLevels)
			if len(defaults.ReasoningLevels) > 0 {
				defaults.ReasoningMode = modelcatalog.ReasoningModeEffort
				defaults.DefaultReasoningEffort = modelcatalog.PreferredReasoningEffort(defaults.ReasoningLevels)
			}
			reasoning := entry.Reasoning != nil && *entry.Reasoning
			defaults.MaxOutputTokens = modelcatalog.RecommendedFallbackMaxOutputTokens(entry.ContextWindowTokens, entry.MaxOutputTokens, reasoning)
		}
		detail := "Magpie gateway"
		if judgment {
			detail += " · typed judgments"
		} else if len(defaults.ReasoningLevels) > 0 {
			detail += " · reasoning"
		}
		out = append(out, SelectableModel{Name: entry.Name, Detail: detail, MetadataComplete: judgment || entry.ContextWindowTokens > 0 && entry.MaxOutputTokens > 0 && entry.Reasoning != nil, ImageInputKnown: defaults.ImageInput != nil, Defaults: defaults})
	}
	return uniqueSelectableModels(out), nil
}

// JudgmentProvider selects the existing judgment protocol of a gateway or
// custom connection, while retaining the canonical provider identity.
func JudgmentProvider(provider string) string {
	switch provider {
	case "magpie":
		return "magpie-judgment"
	case "custom":
		return "custom-judgment"
	}
	return provider
}

// IsJudgmentProvider reports the configured protocol of an onboarding choice.
func IsJudgmentProvider(provider string) bool {
	template, ok := LookupProvider(provider)
	return ok && template.API == APISystemOne
}
