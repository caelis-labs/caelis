package providers

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

var supportedAPITypes = map[model.APIType]struct{}{
	model.APIOpenAI:              {},
	model.APIOpenAIResponses:     {},
	model.APIOpenAICodex:         {},
	model.APIOpenAICompatible:    {},
	model.APIOpenRouter:          {},
	model.APIGemini:              {},
	model.APIAnthropic:           {},
	model.APIAnthropicCompatible: {},
	model.APIDeepSeek:            {},
	model.APIMiniMax:             {},
	model.APIMimo:                {},
	model.APIVolcengine:          {},
	model.APIVolcengineCoding:    {},
	model.APIXAIResponses:        {},
	model.APIOllama:              {},
}

func isSupportedAPIType(api model.APIType) bool {
	_, ok := supportedAPITypes[api]
	return ok
}

func defaultAuthType(api model.APIType) model.AuthType {
	switch api {
	case model.APIOllama:
		return model.AuthNone
	case model.APIOpenAICodex, model.APIXAIResponses:
		return model.AuthOAuthToken
	case model.APIDeepSeek, model.APIMiniMax:
		return model.AuthBearerToken
	default:
		return model.AuthAPIKey
	}
}

// New constructs a model from resolved provider configuration. Model alias
// lookup and credential selection belong to the caller.
func New(cfg Config) (model.LLM, error) {
	if !isSupportedAPIType(cfg.API) {
		return nil, fmt.Errorf("providers: unsupported api type %q", cfg.API)
	}
	switch cfg.Auth.Type {
	case "":
		cfg.Auth.Type = defaultAuthType(cfg.API)
	case model.AuthAPIKey, model.AuthBearerToken, model.AuthOAuthToken, model.AuthNone:
	default:
		if strings.TrimSpace(string(cfg.Auth.Type)) != "" {
			return nil, fmt.Errorf("providers: unsupported auth type %q", cfg.Auth.Type)
		}
	}
	if cfg.API == model.APIOpenAICodex || cfg.API == model.APIXAIResponses {
		if cfg.Auth.Type != model.AuthOAuthToken {
			return nil, fmt.Errorf("providers: %s requires oauth authentication", cfg.API)
		}
		if cfg.HTTPClient == nil {
			return nil, fmt.Errorf("providers: %s requires an authenticated http client", cfg.API)
		}
		if cfg.API == model.APIXAIResponses {
			return model.WithRetry(newXAIResponses(cfg), cfg.Retry), nil
		}
		return model.WithRetry(newOpenAICodex(cfg), cfg.Retry), nil
	}

	token, err := resolveToken(cfg.Auth)
	if err != nil {
		return nil, err
	}

	var llm model.LLM
	switch cfg.API {
	case model.APIDeepSeek:
		llm = newDeepSeek(cfg, token)
	case model.APIMiniMax:
		llm = newMiniMax(cfg, token)
	case model.APIMimo:
		llm = newMimo(cfg, token)
	case model.APIVolcengine:
		llm = newVolcengine(cfg, token)
	case model.APIVolcengineCoding:
		llm = newVolcengineCodingPlan(cfg, token)
	case model.APIOpenAICompatible:
		llm = newOpenAICompat(cfg, token)
	case model.APIOpenRouter:
		llm = newOpenRouter(cfg, token)
	case model.APIOpenAI, model.APIOpenAIResponses:
		llm = newOpenAIResponses(cfg, token)
	case model.APIAnthropic:
		llm = newAnthropic(cfg, token)
	case model.APIAnthropicCompatible:
		llm = newAnthropicWithDefaults(cfg, token, anthropicProviderDefaults{baseURL: defaultAnthropicBaseURL})
	case model.APIGemini:
		llm = newGemini(cfg, token)
	case model.APIOllama:
		llm = newOllama(cfg, token)
	default:
		return nil, fmt.Errorf("providers: unsupported api type %q", cfg.API)
	}
	return model.WithRetry(llm, cfg.Retry), nil
}

func resolveToken(cfg AuthConfig) (string, error) {
	if cfg.Type == model.AuthNone {
		return strings.TrimSpace(cfg.Token), nil
	}
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return "", fmt.Errorf("providers: auth token is empty")
	}
	return token, nil
}
