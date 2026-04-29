package e2etest

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
	modelproviders "github.com/OnslaughtSnail/caelis/sdk/model/providers"
	"github.com/OnslaughtSnail/caelis/sdk/model/providers/minimax"
)

type Config struct {
	DefaultProvider string
	DefaultModel    string
	Timeout         time.Duration
	MaxTokens       int
}

type Spec struct {
	Provider string
	Model    string
	LLM      sdkmodel.LLM
}

type providerSpec struct {
	name              string
	api               modelproviders.APIType
	authType          modelproviders.AuthType
	tokenEnvKeys      []string
	modelEnvKeys      []string
	baseURLEnvKeys    []string
	defaultModel      string
	defaultBaseURL    string
	defaultProvider   string
	defaultContextTok int
}

func RequireLLM(t testing.TB, cfg Config) Spec {
	t.Helper()
	spec, err := ResolveLLM(cfg)
	if err != nil {
		t.Skip(err.Error())
		return Spec{}
	}
	return spec
}

func ResolveLLM(cfg Config) (Spec, error) {
	provider := normalizeProviderName(strings.TrimSpace(os.Getenv("SDK_E2E_PROVIDER")))
	if provider == "" {
		provider = normalizeProviderName(cfg.DefaultProvider)
	}
	if provider == "" {
		provider = "minimax"
	}
	switch provider {
	case "minimax":
		return resolveMiniMax(cfg)
	case "openai":
		return resolveFactoryProvider(cfg, providerSpec{
			name:              "openai",
			api:               modelproviders.APIOpenAI,
			authType:          modelproviders.AuthAPIKey,
			tokenEnvKeys:      []string{"OPENAI_API_KEY"},
			modelEnvKeys:      []string{"OPENAI_MODEL"},
			baseURLEnvKeys:    []string{"OPENAI_BASE_URL"},
			defaultModel:      "gpt-4o-mini",
			defaultBaseURL:    "https://api.openai.com/v1",
			defaultProvider:   "openai",
			defaultContextTok: 128000,
		})
	case "openai-compatible":
		return resolveFactoryProvider(cfg, providerSpec{
			name:              "openai-compatible",
			api:               modelproviders.APIOpenAICompatible,
			authType:          modelproviders.AuthAPIKey,
			tokenEnvKeys:      []string{"OPENAI_COMPATIBLE_API_KEY", "OPENAI_API_KEY"},
			modelEnvKeys:      []string{"OPENAI_COMPATIBLE_MODEL", "OPENAI_MODEL"},
			baseURLEnvKeys:    []string{"OPENAI_COMPATIBLE_BASE_URL", "OPENAI_BASE_URL", "SDK_E2E_BASE_URL"},
			defaultModel:      "gpt-4o-mini",
			defaultBaseURL:    "https://api.openai.com/v1",
			defaultProvider:   "openai-compatible",
			defaultContextTok: 128000,
		})
	case "openrouter":
		return resolveFactoryProvider(cfg, providerSpec{
			name:              "openrouter",
			api:               modelproviders.APIOpenRouter,
			authType:          modelproviders.AuthAPIKey,
			tokenEnvKeys:      []string{"OPENROUTER_API_KEY"},
			modelEnvKeys:      []string{"OPENROUTER_MODEL"},
			baseURLEnvKeys:    []string{"OPENROUTER_BASE_URL"},
			defaultModel:      "openai/gpt-4o-mini",
			defaultBaseURL:    "https://openrouter.ai/api/v1",
			defaultProvider:   "openrouter",
			defaultContextTok: 262144,
		})
	case "gemini":
		return resolveFactoryProvider(cfg, providerSpec{
			name:              "gemini",
			api:               modelproviders.APIGemini,
			authType:          modelproviders.AuthAPIKey,
			tokenEnvKeys:      []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
			modelEnvKeys:      []string{"GEMINI_MODEL"},
			baseURLEnvKeys:    []string{"GEMINI_BASE_URL"},
			defaultModel:      "gemini-2.5-flash",
			defaultBaseURL:    "https://generativelanguage.googleapis.com/v1beta",
			defaultProvider:   "gemini",
			defaultContextTok: 128000,
		})
	case "anthropic":
		return resolveFactoryProvider(cfg, providerSpec{
			name:              "anthropic",
			api:               modelproviders.APIAnthropic,
			authType:          modelproviders.AuthAPIKey,
			tokenEnvKeys:      []string{"ANTHROPIC_API_KEY"},
			modelEnvKeys:      []string{"ANTHROPIC_MODEL"},
			baseURLEnvKeys:    []string{"ANTHROPIC_BASE_URL"},
			defaultModel:      "claude-sonnet-4-20250514",
			defaultBaseURL:    "https://api.anthropic.com",
			defaultProvider:   "anthropic",
			defaultContextTok: 200000,
		})
	case "anthropic-compatible":
		return resolveFactoryProvider(cfg, providerSpec{
			name:              "anthropic-compatible",
			api:               modelproviders.APIAnthropicCompatible,
			authType:          modelproviders.AuthAPIKey,
			tokenEnvKeys:      []string{"ANTHROPIC_COMPATIBLE_API_KEY", "ANTHROPIC_API_KEY"},
			modelEnvKeys:      []string{"ANTHROPIC_COMPATIBLE_MODEL", "ANTHROPIC_MODEL"},
			baseURLEnvKeys:    []string{"ANTHROPIC_COMPATIBLE_BASE_URL", "ANTHROPIC_BASE_URL", "SDK_E2E_BASE_URL"},
			defaultModel:      "claude-sonnet-4-20250514",
			defaultBaseURL:    "https://api.anthropic.com",
			defaultProvider:   "anthropic-compatible",
			defaultContextTok: 200000,
		})
	case "deepseek":
		return resolveFactoryProvider(cfg, providerSpec{
			name:              "deepseek",
			api:               modelproviders.APIDeepSeek,
			authType:          modelproviders.AuthAPIKey,
			tokenEnvKeys:      []string{"DEEPSEEK_API_KEY"},
			modelEnvKeys:      []string{"DEEPSEEK_MODEL"},
			baseURLEnvKeys:    []string{"DEEPSEEK_BASE_URL"},
			defaultModel:      "deepseek-v4-flash",
			defaultBaseURL:    "https://api.deepseek.com/v1",
			defaultProvider:   "deepseek",
			defaultContextTok: 128000,
		})
	case "xiaomi", "mimo":
		return resolveFactoryProvider(cfg, providerSpec{
			name:              "xiaomi",
			api:               modelproviders.APIMimo,
			authType:          modelproviders.AuthAPIKey,
			tokenEnvKeys:      []string{"XIAOMI_API_KEY", "MIMO_API_KEY"},
			modelEnvKeys:      []string{"XIAOMI_MODEL", "MIMO_MODEL"},
			baseURLEnvKeys:    []string{"XIAOMI_BASE_URL", "MIMO_BASE_URL"},
			defaultModel:      "mimo-v2-flash",
			defaultBaseURL:    "https://api.xiaomimimo.com/v1",
			defaultProvider:   "xiaomi",
			defaultContextTok: 128000,
		})
	case "volcengine":
		return resolveFactoryProvider(cfg, providerSpec{
			name:              "volcengine",
			api:               modelproviders.APIVolcengine,
			authType:          modelproviders.AuthAPIKey,
			tokenEnvKeys:      []string{"VOLCENGINE_API_KEY", "ARK_API_KEY"},
			modelEnvKeys:      []string{"VOLCENGINE_MODEL", "ARK_MODEL"},
			baseURLEnvKeys:    []string{"VOLCENGINE_BASE_URL", "ARK_BASE_URL"},
			defaultBaseURL:    "https://ark.cn-beijing.volces.com/api/v3",
			defaultProvider:   "volcengine",
			defaultContextTok: 128000,
		})
	case "volcengine-coding-plan", "volcengine_coding_plan":
		return resolveFactoryProvider(cfg, providerSpec{
			name:              "volcengine-coding-plan",
			api:               modelproviders.APIVolcengineCoding,
			authType:          modelproviders.AuthAPIKey,
			tokenEnvKeys:      []string{"VOLCENGINE_API_KEY", "ARK_API_KEY"},
			modelEnvKeys:      []string{"VOLCENGINE_MODEL", "ARK_MODEL"},
			baseURLEnvKeys:    []string{"VOLCENGINE_BASE_URL", "ARK_BASE_URL"},
			defaultBaseURL:    "https://ark.cn-beijing.volces.com/api/coding/v3",
			defaultProvider:   "volcengine",
			defaultContextTok: 128000,
		})
	case "ollama":
		return resolveFactoryProvider(cfg, providerSpec{
			name:              "ollama",
			api:               modelproviders.APIOllama,
			authType:          modelproviders.AuthNone,
			modelEnvKeys:      []string{"OLLAMA_MODEL"},
			baseURLEnvKeys:    []string{"OLLAMA_BASE_URL"},
			defaultModel:      "qwen2.5:7b",
			defaultBaseURL:    "http://localhost:11434",
			defaultProvider:   "ollama",
			defaultContextTok: 128000,
		})
	case "codefree":
		return resolveCodeFree(cfg)
	default:
		return Spec{}, fmt.Errorf("SDK_E2E_PROVIDER=%q is not supported", provider)
	}
}

func resolveCodeFree(cfg Config) (Spec, error) {
	modelName := resolveModelName(cfg, "codefree", []string{"CODEFREE_MODEL"}, "GLM-5.1")
	if modelName == "" {
		return Spec{}, fmt.Errorf("codefree model is not set")
	}
	baseURL := resolveBaseURL([]string{"CODEFREE_BASE_URL"}, "https://www.srdcloud.cn")
	if baseURL == "" {
		return Spec{}, fmt.Errorf("codefree base URL is not set")
	}
	if _, err := os.Stat(resolveCodeFreeCredentialPathForE2E()); err != nil {
		if os.IsNotExist(err) {
			return Spec{}, fmt.Errorf("codefree oauth credentials are not available")
		}
		return Spec{}, fmt.Errorf("codefree oauth credentials are not readable: %w", err)
	}

	factory := modelproviders.NewFactory()
	cfgRecord := modelproviders.Config{
		Alias:               buildAlias("codefree", modelName),
		Provider:            "codefree",
		API:                 modelproviders.APICodeFree,
		Model:               modelName,
		BaseURL:             baseURL,
		Timeout:             resolveTimeout(cfg),
		MaxOutputTok:        resolveMaxTokens(cfg),
		ContextWindowTokens: codeFreeContextWindowTokensForE2E(modelName),
		Auth: modelproviders.AuthConfig{
			Type: modelproviders.AuthNone,
		},
	}
	if err := factory.Register(cfgRecord); err != nil {
		return Spec{}, err
	}
	llm, err := factory.NewByAlias(cfgRecord.Alias)
	if err != nil {
		return Spec{}, err
	}
	return Spec{
		Provider: cfgRecord.Provider,
		Model:    modelName,
		LLM:      llm,
	}, nil
}

func codeFreeContextWindowTokensForE2E(modelName string) int {
	if strings.EqualFold(strings.TrimSpace(modelName), "GLM-5.1") {
		return 128000
	}
	return 88000
}

func resolveMiniMax(cfg Config) (Spec, error) {
	apiKey := strings.TrimSpace(os.Getenv("MINIMAX_API_KEY"))
	if apiKey == "" {
		return Spec{}, fmt.Errorf("MINIMAX_API_KEY is not set")
	}
	modelName := resolveModelName(cfg, "minimax", []string{"MINIMAX_MODEL"}, "MiniMax-M2")
	return Spec{
		Provider: "minimax",
		Model:    modelName,
		LLM: minimax.New(minimax.Config{
			Model:     modelName,
			APIKey:    apiKey,
			Timeout:   resolveTimeout(cfg),
			MaxTokens: resolveMaxTokens(cfg),
		}),
	}, nil
}

func resolveFactoryProvider(cfg Config, spec providerSpec) (Spec, error) {
	token := firstNonEmptyEnv(spec.tokenEnvKeys...)
	if spec.authType != modelproviders.AuthNone && token == "" {
		return Spec{}, fmt.Errorf("%s is not set", strings.Join(spec.tokenEnvKeys, " or "))
	}
	modelName := resolveModelName(cfg, spec.defaultProvider, spec.modelEnvKeys, spec.defaultModel)
	if modelName == "" {
		return Spec{}, fmt.Errorf("%s model is not set", spec.name)
	}
	baseURL := resolveBaseURL(spec.baseURLEnvKeys, spec.defaultBaseURL)
	if requiresBaseURL(spec.api) && baseURL == "" {
		return Spec{}, fmt.Errorf("%s base URL is not set", spec.name)
	}

	factory := modelproviders.NewFactory()
	cfgRecord := modelproviders.Config{
		Alias:               buildAlias(spec.defaultProvider, modelName),
		Provider:            spec.defaultProvider,
		API:                 spec.api,
		Model:               modelName,
		BaseURL:             baseURL,
		Timeout:             resolveTimeout(cfg),
		MaxOutputTok:        resolveMaxTokens(cfg),
		ContextWindowTokens: spec.defaultContextTok,
		Auth: modelproviders.AuthConfig{
			Type:  spec.authType,
			Token: token,
		},
	}
	if err := factory.Register(cfgRecord); err != nil {
		return Spec{}, err
	}
	llm, err := factory.NewByAlias(cfgRecord.Alias)
	if err != nil {
		return Spec{}, err
	}
	return Spec{
		Provider: cfgRecord.Provider,
		Model:    modelName,
		LLM:      llm,
	}, nil
}

func normalizeProviderName(raw string) string {
	value := strings.ToLower(strings.TrimSpace(raw))
	switch value {
	case "":
		return "minimax"
	case "openai_compatible":
		return "openai-compatible"
	case "anthropic_compatible":
		return "anthropic-compatible"
	case "volcengine_coding_plan":
		return "volcengine-coding-plan"
	default:
		return value
	}
}

func resolveModelName(cfg Config, provider string, envKeys []string, fallback string) string {
	if model := strings.TrimSpace(os.Getenv("SDK_E2E_MODEL")); model != "" {
		return model
	}
	if model := firstNonEmptyEnv(envKeys...); model != "" {
		return model
	}
	defaultProvider := normalizeProviderName(cfg.DefaultProvider)
	if defaultProvider == "" || defaultProvider == normalizeProviderName(provider) {
		if model := strings.TrimSpace(cfg.DefaultModel); model != "" {
			return model
		}
	}
	if model := strings.TrimSpace(fallback); model != "" {
		return model
	}
	return ""
}

func resolveBaseURL(envKeys []string, fallback string) string {
	if baseURL := strings.TrimSpace(os.Getenv("SDK_E2E_BASE_URL")); baseURL != "" {
		return baseURL
	}
	if baseURL := firstNonEmptyEnv(envKeys...); baseURL != "" {
		return baseURL
	}
	return strings.TrimSpace(fallback)
}

func resolveTimeout(cfg Config) time.Duration {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 90 * time.Second
	}
	return timeout
}

func resolveMaxTokens(cfg Config) int {
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 512
	}
	return maxTokens
}

func buildAlias(provider string, model string) string {
	provider = strings.TrimSpace(provider)
	model = strings.TrimSpace(model)
	if provider == "" {
		return model
	}
	return provider + "/" + model
}

func requiresBaseURL(api modelproviders.APIType) bool {
	switch api {
	case modelproviders.APIGemini, modelproviders.APIAnthropic, modelproviders.APIAnthropicCompatible:
		return false
	default:
		return true
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func resolveCodeFreeCredentialPathForE2E() string {
	if path := strings.TrimSpace(os.Getenv("CODEFREE_OAUTH_CREDS_PATH")); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(home + "/.caelis/providers/codefree/oauth_creds.json")
}
