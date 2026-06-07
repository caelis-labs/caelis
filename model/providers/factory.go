package providers

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/model"
)

// ModelConfig describes one configured Layer 4 model endpoint.
type ModelConfig struct {
	Alias                   string
	Provider                string
	Model                   string
	DisplayName             string
	BaseURL                 string
	Token                   string
	Headers                 map[string]string
	HTTPClient              *http.Client
	StreamFirstEventTimeout time.Duration
	MaxOutputTokens         int
	MaxTokens               int
	SupportsTools           bool
	SupportsImage           bool
	SupportsAudio           bool
	OpenRouter              OpenRouterConfig
	CodeFreeCredential      CodeFreeCredentials
}

// ConfiguredFactory creates LLMs from durable model endpoint configuration.
type ConfiguredFactory struct {
	configs map[string]ModelConfig
	aliases map[string]string
	infos   []model.ModelInfo
}

// NewConfiguredFactory builds a factory for providers that have migrated into
// model/providers. Unsupported providers fail closed instead of falling back to
// legacy impl paths.
func NewConfiguredFactory(configs []ModelConfig) (*ConfiguredFactory, error) {
	f := &ConfiguredFactory{
		configs: make(map[string]ModelConfig, len(configs)),
		aliases: make(map[string]string, len(configs)),
		infos:   make([]model.ModelInfo, 0, len(configs)),
	}
	for _, cfg := range configs {
		providerLabel := normalizeFactoryProviderLabel(cfg.Provider)
		canonicalProvider, ok := canonicalFactoryProvider(providerLabel)
		if !ok {
			return nil, fmt.Errorf("providers: unsupported Layer 4 provider %q", cfg.Provider)
		}
		modelName := strings.TrimSpace(cfg.Model)
		if modelName == "" {
			return nil, fmt.Errorf("providers: model is required for provider %q", cfg.Provider)
		}
		modelID := factoryModelID(providerLabel, canonicalProvider, modelName)
		if _, exists := f.configs[modelID]; exists {
			return nil, fmt.Errorf("providers: duplicate model id %q", modelID)
		}
		cfg.Provider = providerLabel
		cfg.Model = modelName
		f.configs[modelID] = cfg
		if alias := strings.TrimSpace(cfg.Alias); alias != "" {
			if _, exists := f.aliases[alias]; exists {
				return nil, fmt.Errorf("providers: duplicate model alias %q", alias)
			}
			f.aliases[alias] = modelID
		}
		info := model.ModelInfo{
			ModelID:       modelID,
			DisplayName:   firstNonEmpty(strings.TrimSpace(cfg.DisplayName), modelName),
			Provider:      canonicalProvider,
			MaxTokens:     cfg.MaxTokens,
			SupportsTools: cfg.SupportsTools,
			SupportsImage: cfg.SupportsImage,
			SupportsAudio: cfg.SupportsAudio,
		}
		if alias := strings.TrimSpace(cfg.Alias); alias != "" {
			info.Aliases = []string{alias}
		}
		f.infos = append(f.infos, info)
	}
	sort.Slice(f.infos, func(i, j int) bool {
		return f.infos[i].ModelID < f.infos[j].ModelID
	})
	return f, nil
}

// ModelInfos returns catalog metadata for all configured endpoints.
func (f *ConfiguredFactory) ModelInfos() []model.ModelInfo {
	if f == nil || len(f.infos) == 0 {
		return nil
	}
	out := make([]model.ModelInfo, len(f.infos))
	copy(out, f.infos)
	for i := range out {
		out[i].Aliases = append([]string(nil), out[i].Aliases...)
	}
	return out
}

// New creates a provider client for a model reference.
func (f *ConfiguredFactory) New(ref model.Ref) (model.LLM, error) {
	if f == nil {
		return nil, fmt.Errorf("providers: configured factory is nil")
	}
	modelID := strings.TrimSpace(ref.ModelID)
	if modelID == "" && strings.TrimSpace(ref.Alias) != "" {
		modelID = f.aliases[strings.TrimSpace(ref.Alias)]
	}
	if modelID == "" {
		return nil, fmt.Errorf("providers: model reference is required")
	}
	cfg, ok := f.configs[modelID]
	if !ok {
		return nil, fmt.Errorf("providers: model %q is not configured", modelID)
	}
	return newConfiguredProvider(cfg)
}

func newConfiguredProvider(cfg ModelConfig) (model.LLM, error) {
	providerLabel := normalizeFactoryProviderLabel(cfg.Provider)
	common := OpenAICompatConfig{
		Name:                    cfg.DisplayName,
		Provider:                providerLabel,
		BaseURL:                 cfg.BaseURL,
		Token:                   cfg.Token,
		Model:                   cfg.Model,
		Headers:                 cloneHeaders(cfg.Headers),
		HTTPClient:              cfg.HTTPClient,
		StreamFirstEventTimeout: cfg.StreamFirstEventTimeout,
		MaxOutputTok:            cfg.MaxOutputTokens,
		OpenRouter:              cfg.OpenRouter,
	}
	switch providerLabel {
	case "openai":
		return NewOpenAI(OpenAIConfig{
			Name:                    cfg.DisplayName,
			BaseURL:                 cfg.BaseURL,
			Token:                   cfg.Token,
			Model:                   cfg.Model,
			Headers:                 cloneHeaders(cfg.Headers),
			HTTPClient:              cfg.HTTPClient,
			StreamFirstEventTimeout: cfg.StreamFirstEventTimeout,
		}), nil
	case "openai-compatible":
		return NewOpenAICompatible(common), nil
	case "deepseek":
		return NewDeepSeek(common), nil
	case "openrouter":
		return NewOpenRouter(common), nil
	case "mimo", "xiaomi":
		return NewMimo(common), nil
	case "volcengine":
		return NewVolcengine(common), nil
	case "volcengine-coding", "volcengine_coding", "volcengine_coding_plan":
		return NewVolcengineCoding(common), nil
	case "minimax":
		return NewMiniMax(MiniMaxConfig{
			Name:                    cfg.DisplayName,
			BaseURL:                 cfg.BaseURL,
			Token:                   cfg.Token,
			Model:                   cfg.Model,
			Headers:                 cloneHeaders(cfg.Headers),
			HTTPClient:              cfg.HTTPClient,
			StreamFirstEventTimeout: cfg.StreamFirstEventTimeout,
			MaxOutputTok:            cfg.MaxOutputTokens,
		}), nil
	case "codefree":
		return NewCodeFree(CodeFreeConfig{
			Name:                    cfg.DisplayName,
			BaseURL:                 cfg.BaseURL,
			Model:                   cfg.Model,
			Credentials:             cfg.CodeFreeCredential,
			Headers:                 cloneHeaders(cfg.Headers),
			HTTPClient:              cfg.HTTPClient,
			StreamFirstEventTimeout: cfg.StreamFirstEventTimeout,
			MaxOutputTok:            cfg.MaxOutputTokens,
		}), nil
	case "ollama":
		return NewOllama(OllamaConfig{
			Name:                    cfg.DisplayName,
			BaseURL:                 cfg.BaseURL,
			Model:                   cfg.Model,
			Headers:                 cloneHeaders(cfg.Headers),
			HTTPClient:              cfg.HTTPClient,
			StreamFirstEventTimeout: cfg.StreamFirstEventTimeout,
			MaxOutputTok:            cfg.MaxOutputTokens,
		}), nil
	case "anthropic":
		return NewAnthropic(AnthropicConfig{
			Name:                    cfg.DisplayName,
			BaseURL:                 cfg.BaseURL,
			Token:                   cfg.Token,
			Model:                   cfg.Model,
			Headers:                 cloneHeaders(cfg.Headers),
			HTTPClient:              cfg.HTTPClient,
			StreamFirstEventTimeout: cfg.StreamFirstEventTimeout,
			MaxOutputTok:            cfg.MaxOutputTokens,
		}), nil
	case "gemini":
		return NewGemini(GeminiConfig{
			Name:                    cfg.DisplayName,
			BaseURL:                 cfg.BaseURL,
			Token:                   cfg.Token,
			Model:                   cfg.Model,
			Headers:                 cloneHeaders(cfg.Headers),
			HTTPClient:              cfg.HTTPClient,
			StreamFirstEventTimeout: cfg.StreamFirstEventTimeout,
			MaxOutputTok:            cfg.MaxOutputTokens,
		}), nil
	default:
		return nil, fmt.Errorf("providers: unsupported Layer 4 provider %q", cfg.Provider)
	}
}

func normalizeFactoryProviderLabel(provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "openai_compatible":
		return "openai-compatible"
	case "volcengine-coding-plan":
		return "volcengine_coding_plan"
	default:
		return provider
	}
}

func canonicalFactoryProvider(provider string) (string, bool) {
	switch provider {
	case "openai", "openai-compatible", "deepseek", "openrouter", "minimax", "codefree", "ollama", "anthropic", "gemini":
		return provider, true
	case "mimo", "xiaomi":
		return "mimo", true
	case "volcengine":
		return "volcengine", true
	case "volcengine-coding", "volcengine_coding", "volcengine_coding_plan":
		return "volcengine-coding", true
	default:
		return "", false
	}
}

func factoryModelID(providerLabel string, canonicalProvider string, modelName string) string {
	modelName = strings.TrimSpace(modelName)
	if canonicalProvider == "openrouter" {
		modelName = normalizeOpenRouterModelID(modelName)
	}
	return strings.TrimSpace(providerLabel) + "/" + modelName
}
