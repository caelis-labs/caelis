package modelconfig

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/modelcatalog"
)

// Control reads these protocol/address defaults only for saved configurations.
// They are not onboarding templates or a model directory. Remove this adapter
// when the minimum supported configuration persists explicit endpoint protocols,
// authentication and capabilities. Exact historical model limits are not restored.
var savedProviderDefaults = map[string]ProviderTemplate{
	"typesafe":                    {API: APISystemOne, AuthType: model.AuthBearerToken, DefaultBaseURL: "https://api.typesafe.ai/v1"},
	"openai":                      {API: model.APIOpenAI, AuthType: model.AuthAPIKey, DefaultBaseURL: "https://api.openai.com/v1"},
	"openai-compatible":           {API: model.APIOpenAICompatible, AuthType: model.AuthAPIKey, DefaultBaseURL: "https://api.openai.com/v1"},
	"openai-responses-compatible": {API: model.APIOpenAIResponses, AuthType: model.AuthAPIKey, DefaultBaseURL: "https://api.openai.com/v1"},
	"openrouter":                  {API: model.APIOpenRouter, AuthType: model.AuthAPIKey, DefaultBaseURL: "https://openrouter.ai/api/v1"},
	"gemini":                      {API: model.APIGemini, AuthType: model.AuthAPIKey, DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta"},
	"anthropic":                   {API: model.APIAnthropic, AuthType: model.AuthAPIKey, DefaultBaseURL: "https://api.anthropic.com"},
	"anthropic-compatible":        {API: model.APIAnthropicCompatible, AuthType: model.AuthAPIKey, DefaultBaseURL: "https://api.anthropic.com"},
	"minimax":                     {API: model.APIMiniMax, AuthType: model.AuthBearerToken, DefaultBaseURL: "https://api.minimaxi.com/anthropic"},
	"volcengine": {API: model.APIVolcengine, AuthType: model.AuthAPIKey, DefaultEndpointID: "standard", DefaultBaseURL: "https://ark.cn-beijing.volces.com/api/v3", Endpoints: []EndpointTemplate{
		{ID: "standard", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", API: model.APIVolcengine},
		{ID: "coding-plan", BaseURL: "https://ark.cn-beijing.volces.com/api/coding/v3", API: model.APIVolcengineCoding},
	}},
	"ollama": {Endpoints: []EndpointTemplate{{ID: "cloud", BaseURL: "https://ollama.com", API: model.APIOllama, AuthType: model.AuthAPIKey}}},
}

// RestoreSavedCapabilities fills omitted saved fields with native metadata or
// lightweight protocol defaults. It preserves explicit capabilities, IDs and
// credentials; retired providers cannot be used to assemble new connections.
func RestoreSavedCapabilities(cfg Config) Config {
	api := FirstNonEmptyAPI(cfg.API, DefaultAPIForProvider(cfg.Provider))
	if api == "" {
		return cfg
	}
	caps, known := modelcatalog.LookupModelCapabilities(catalogProviderFor(cfg.Provider), cfg.Model)
	if cfg.Provider == "openai" {
		caps, known = modelcatalog.LookupModelCapabilities("openai-codex", cfg.Model)
	}
	if cfg.ContextWindowTokens <= 0 {
		cfg.ContextWindowTokens = 128000
		if template, ok := LookupProvider(cfg.Provider); ok && template.DefaultContextWindowTokens > 0 {
			cfg.ContextWindowTokens = template.DefaultContextWindowTokens
		}
		if known {
			cfg.ContextWindowTokens = caps.ContextWindowTokens
		}
	}
	// Older writers omitted 4096 because it was the configured default.
	if cfg.MaxOutputTok <= 0 {
		cfg.MaxOutputTok = 4096
	}
	if cfg.ImageInput == nil {
		if known {
			cfg.ImageInput = boolPointer(caps.SupportsImages)
		} else {
			switch api {
			case model.APIOpenAI, model.APIOpenAICodex:
				if strings.HasPrefix(strings.ToLower(cfg.Model), "gpt-") {
					cfg.ImageInput = boolPointer(true)
				}
			case model.APIGemini, model.APIAnthropic, model.APIXAIResponses:
				cfg.ImageInput = boolPointer(true)
			}
		}
	}
	if cfg.ReasoningMode == "" {
		cfg.ReasoningMode = modelcatalog.ReasoningModeNone
		if len(cfg.ReasoningLevels) > 0 || cfg.ReasoningEffort != "" && cfg.ReasoningEffort != "none" {
			cfg.ReasoningMode = modelcatalog.ReasoningModeEffort
		} else if known {
			cfg.ReasoningMode = caps.ReasoningMode
			cfg.ReasoningLevels = append([]string(nil), caps.ReasoningEfforts...)
		}
	}
	if cfg.DefaultReasoningEffort == "" && cfg.ReasoningMode != modelcatalog.ReasoningModeNone {
		cfg.DefaultReasoningEffort = cfg.ReasoningEffort
		if cfg.DefaultReasoningEffort == "" && known {
			cfg.DefaultReasoningEffort = caps.DefaultReasoningEffort
		}
	}
	if cfg.SpeedModes == nil && (api == model.APIOpenAI || api == model.APIOpenAICodex) {
		cfg.SpeedModes = append([]modelcatalog.SpeedMode(nil), caps.SpeedModes...)
		if !known && strings.HasPrefix(cfg.Model, "gpt-5.") && !strings.HasSuffix(cfg.Model, "-pro") {
			cfg.SpeedModes = []modelcatalog.SpeedMode{{Level: "fast", Description: "Faster responses, increased usage"}}
		}
	}
	return cfg
}
