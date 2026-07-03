package providers

import (
	"fmt"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/ports/model"
)

// Factory builds model providers from alias configs.
type Factory struct {
	configs map[string]Config
}

var supportedAPITypes = map[APIType]struct{}{
	APIOpenAI:              {},
	APIOpenAICompatible:    {},
	APIOpenRouter:          {},
	APICodeFree:            {},
	APIGemini:              {},
	APIAnthropic:           {},
	APIAnthropicCompatible: {},
	APIDeepSeek:            {},
	APIMiniMax:             {},
	APIMimo:                {},
	APIVolcengine:          {},
	APIVolcengineCoding:    {},
	APIOllama:              {},
}

// NewFactory returns an empty provider factory.
func NewFactory() *Factory {
	return &Factory{configs: map[string]Config{}}
}

// Register adds or overwrites one alias config.
func (f *Factory) Register(cfg Config) error {
	if f == nil {
		return fmt.Errorf("providers: factory is nil")
	}
	alias := strings.ToLower(strings.TrimSpace(cfg.Alias))
	if alias == "" {
		return fmt.Errorf("providers: alias is required")
	}
	if !isSupportedAPIType(cfg.API) {
		return fmt.Errorf("providers: unsupported api type %q", cfg.API)
	}
	authType := strings.TrimSpace(string(cfg.Auth.Type))
	if authType != "" && cfg.Auth.Type != AuthAPIKey && cfg.Auth.Type != AuthBearerToken && cfg.Auth.Type != AuthOAuthToken && cfg.Auth.Type != AuthNone {
		return fmt.Errorf("providers: unsupported auth type %q", cfg.Auth.Type)
	}
	if cfg.Auth.Type == "" {
		cfg.Auth.Type = defaultAuthType(cfg.API)
	}
	cfg.Alias = alias
	f.configs[alias] = cfg
	return nil
}

func isSupportedAPIType(api APIType) bool {
	_, ok := supportedAPITypes[api]
	return ok
}

func defaultAuthType(api APIType) AuthType {
	switch api {
	case APIOllama, APICodeFree:
		return AuthNone
	case APIDeepSeek, APIMiniMax:
		return AuthBearerToken
	default:
		return AuthAPIKey
	}
}

// NewByAlias creates a model provider by alias.
func (f *Factory) NewByAlias(alias string) (model.LLM, error) {
	if f == nil {
		return nil, fmt.Errorf("providers: factory is nil")
	}
	alias = strings.ToLower(strings.TrimSpace(alias))
	if alias == "" {
		return nil, fmt.Errorf("providers: model alias is required")
	}
	cfg, ok := f.configs[alias]
	if !ok {
		return nil, fmt.Errorf("providers: unknown model alias %q", alias)
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
	case APICodeFree:
		llm = newCodeFree(cfg)
	case APIOpenAI:
		llm = newOpenAICompat(cfg, token)
	case APIAnthropic, APIAnthropicCompatible:
		llm = newAnthropic(cfg, token)
	case APIGemini:
		llm = newGemini(cfg, token)
	case APIOllama:
		llm = newOllama(cfg, token)
	default:
		return nil, fmt.Errorf("providers: unsupported api type %q", cfg.API)
	}
	return model.WithRetry(llm, cfg.Retry), nil
}

// NewByAlias creates a model provider from a new empty factory.
func NewByAlias(alias string) (model.LLM, error) {
	return NewFactory().NewByAlias(alias)
}

// ListModels returns available aliases from current factory.
func (f *Factory) ListModels() []string {
	if f == nil {
		return nil
	}
	out := make([]string, 0, len(f.configs))
	for k := range f.configs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ListModels returns aliases from a new empty factory.
func ListModels() []string {
	return NewFactory().ListModels()
}

// ConfigForAlias returns the registered Config for the given alias.
// Returns zero Config and false if the alias is not registered.
func (f *Factory) ConfigForAlias(alias string) (Config, bool) {
	if f == nil {
		return Config{}, false
	}
	alias = strings.ToLower(strings.TrimSpace(alias))
	cfg, ok := f.configs[alias]
	return cfg, ok
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
