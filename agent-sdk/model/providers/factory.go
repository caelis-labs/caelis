package providers

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

var supportedAPITypes = map[APIType]struct{}{
	APIOpenAI:              {},
	APIOpenAIResponses:     {},
	APIOpenAICodex:         {},
	APIOpenAICompatible:    {},
	APIOpenRouter:          {},
	APIGemini:              {},
	APIAnthropic:           {},
	APIAnthropicCompatible: {},
	APIDeepSeek:            {},
	APIMiniMax:             {},
	APIMimo:                {},
	APIVolcengine:          {},
	APIVolcengineCoding:    {},
	APIXAIResponses:        {},
	APIOllama:              {},
}

func isSupportedAPIType(api APIType) bool {
	_, ok := supportedAPITypes[api]
	return ok
}

func defaultAuthType(api APIType) AuthType {
	switch api {
	case APIOllama:
		return AuthNone
	case APIOpenAICodex, APIXAIResponses:
		return AuthOAuthToken
	case APIDeepSeek, APIMiniMax:
		return AuthBearerToken
	default:
		return AuthAPIKey
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
	case AuthAPIKey, AuthBearerToken, AuthOAuthToken, AuthNone:
	default:
		if strings.TrimSpace(string(cfg.Auth.Type)) != "" {
			return nil, fmt.Errorf("providers: unsupported auth type %q", cfg.Auth.Type)
		}
	}
	if cfg.API == APIOpenAICodex || cfg.API == APIXAIResponses {
		if cfg.Auth.Type != AuthOAuthToken {
			return nil, fmt.Errorf("providers: %s requires oauth authentication", cfg.API)
		}
		if cfg.HTTPClient == nil {
			return nil, fmt.Errorf("providers: %s requires an authenticated http client", cfg.API)
		}
		if cfg.API == APIXAIResponses {
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
	case APIDeepSeek:
		llm = newDeepSeek(cfg, token)
	case APIMiniMax:
		llm = newMiniMax(cfg, token)
	case APIMimo:
		llm = newMimo(cfg, token)
	case APIVolcengine:
		llm = newVolcengine(cfg, token)
	case APIVolcengineCoding:
		llm = newVolcengineCodingPlan(cfg, token)
	case APIOpenAICompatible:
		llm = newOpenAICompat(cfg, token)
	case APIOpenRouter:
		llm = newOpenRouter(cfg, token)
	case APIOpenAI, APIOpenAIResponses:
		llm = newOpenAIResponses(cfg, token)
	case APIAnthropic:
		llm = newAnthropic(cfg, token)
	case APIAnthropicCompatible:
		llm = newAnthropicWithDefaults(cfg, token, anthropicProviderDefaults{baseURL: defaultAnthropicBaseURL})
	case APIGemini:
		llm = newGemini(cfg, token)
	case APIOllama:
		llm = newOllama(cfg, token)
	default:
		return nil, fmt.Errorf("providers: unsupported api type %q", cfg.API)
	}
	return model.WithRetry(llm, cfg.Retry), nil
}

func resolveToken(cfg AuthConfig) (string, error) {
	if cfg.Type == AuthNone {
		return strings.TrimSpace(cfg.Token), nil
	}
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return "", fmt.Errorf("providers: auth token is empty")
	}
	return token, nil
}
