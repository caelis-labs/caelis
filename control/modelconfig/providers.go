package modelconfig

import (
	"net/url"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
)

const (
	// CodexOAuthBaseURL is the fixed ChatGPT Codex Responses API root used by
	// the maintained subscription-backed provider.
	CodexOAuthBaseURL = "https://chatgpt.com/backend-api/codex"
	// CodexOAuthCredentialRef selects the one Control-owned Codex OAuth account.
	CodexOAuthCredentialRef = "codex:default"
	// XiaomiAPIBaseURL is the standard Xiaomi MiMo endpoint.
	XiaomiAPIBaseURL = "https://api.xiaomimimo.com/v1"
	// XiaomiTokenPlanCNBaseURL is the Xiaomi coding-plan endpoint in China.
	XiaomiTokenPlanCNBaseURL = "https://token-plan-cn.xiaomimimo.com/v1"
	// XiaomiTokenPlanCNAlias selects the Xiaomi token-plan endpoint directly.
	XiaomiTokenPlanCNAlias = "xiaomi-token-plan-cn"
)

// AuthFlow identifies an interactive authentication flow maintained by
// Control. Empty means the provider uses only its configured credential.
type AuthFlow string

const (
	// AuthFlowCodeFreeOAuth uses CodeFree's browser OAuth flow.
	AuthFlowCodeFreeOAuth AuthFlow = "codefree_oauth"
	// AuthFlowCodexOAuth uses the public Codex CLI OAuth client with browser and
	// device-code login modes.
	AuthFlowCodexOAuth AuthFlow = "codex_oauth"
)

// EndpointTemplate describes one maintained endpoint variant for a provider.
type EndpointTemplate struct {
	ID       string
	BaseURL  string
	Display  string
	Detail   string
	API      model.APIType
	TokenEnv string
}

// ProviderTemplate is Control's maintained onboarding policy for one provider.
// It owns endpoint, authentication, and conservative unknown-model defaults;
// concrete model capabilities remain in modelcatalog.
type ProviderTemplate struct {
	Label                      string
	Provider                   string
	Description                string
	API                        model.APIType
	AuthType                   model.AuthType
	AuthFlow                   AuthFlow
	AuthDisplay                string
	PreserveModelOrder         bool
	DefaultTokenEnv            string
	DefaultEndpointID          string
	DefaultBaseURL             string
	DefaultBaseURLAliases      []string
	DefaultContextWindowTokens int
	DefaultMaxOutputTokens     int
	DefaultReasoningLevels     []string
	DefaultReasoningMode       string
	DefaultReasoningEffort     string
	NoAuthRequired             bool
	PromptForBaseURL           bool
	UseModelDirectory          bool
	Endpoints                  []EndpointTemplate
}

var providerTemplates = []ProviderTemplate{
	{Label: "codex", API: model.APIOpenAICodex, AuthType: model.AuthOAuthToken, AuthFlow: AuthFlowCodexOAuth, AuthDisplay: "browser/device oauth", PreserveModelOrder: true, Provider: "openai-codex", Description: "ChatGPT subscription through a community Codex OAuth integration", DefaultBaseURL: CodexOAuthBaseURL, DefaultContextWindowTokens: 272000, DefaultMaxOutputTokens: 32768, DefaultReasoningLevels: []string{"low", "medium", "high", "xhigh"}, DefaultReasoningMode: "effort", DefaultReasoningEffort: "medium"},
	{Label: "openai", API: model.APIOpenAI, AuthType: model.AuthAPIKey, Provider: "openai", Description: "OpenAI-hosted models", DefaultTokenEnv: "OPENAI_API_KEY", DefaultBaseURL: "https://api.openai.com/v1", DefaultContextWindowTokens: 128000},
	{Label: "openai-compatible", API: model.APIOpenAICompatible, AuthType: model.AuthAPIKey, Provider: "openai-compatible", Description: "OpenAI-compatible proxy or self-hosted endpoint", DefaultTokenEnv: "OPENAI_COMPATIBLE_API_KEY", DefaultBaseURL: "https://api.openai.com/v1", DefaultContextWindowTokens: 262144, DefaultMaxOutputTokens: 32768, DefaultReasoningLevels: []string{"none", "minimal", "low", "medium", "high", "xhigh"}, DefaultReasoningMode: "effort", DefaultReasoningEffort: "medium", PromptForBaseURL: true},
	{Label: "codefree", API: model.APICodeFree, AuthType: model.AuthNone, AuthFlow: AuthFlowCodeFreeOAuth, AuthDisplay: "browser oauth", Provider: "codefree", Description: "China Telecom SRD CodeFree models via browser OAuth", DefaultBaseURL: "https://www.srdcloud.cn", DefaultContextWindowTokens: 128000, DefaultMaxOutputTokens: 8000, NoAuthRequired: true},
	{Label: "openrouter", API: model.APIOpenRouter, AuthType: model.AuthAPIKey, Provider: "openrouter", Description: "OpenRouter multi-provider routing", DefaultTokenEnv: "OPENROUTER_API_KEY", DefaultBaseURL: "https://openrouter.ai/api/v1", DefaultContextWindowTokens: 262144, UseModelDirectory: true},
	{Label: "gemini", API: model.APIGemini, AuthType: model.AuthAPIKey, Provider: "gemini", Description: "Google Gemini API", DefaultTokenEnv: "GEMINI_API_KEY", DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta", DefaultContextWindowTokens: 128000},
	{Label: "anthropic", API: model.APIAnthropic, AuthType: model.AuthAPIKey, Provider: "anthropic", Description: "Anthropic Claude API", DefaultTokenEnv: "ANTHROPIC_API_KEY", DefaultBaseURL: "https://api.anthropic.com", DefaultContextWindowTokens: 200000, DefaultMaxOutputTokens: 1024},
	{Label: "anthropic-compatible", API: model.APIAnthropicCompatible, AuthType: model.AuthAPIKey, Provider: "anthropic-compatible", Description: "Anthropic-compatible proxy or self-hosted endpoint", DefaultTokenEnv: "ANTHROPIC_COMPATIBLE_API_KEY", DefaultBaseURL: "https://api.anthropic.com", DefaultContextWindowTokens: 200000, DefaultMaxOutputTokens: 32768, DefaultReasoningLevels: []string{"none", "minimal", "low", "medium", "high", "max"}, DefaultReasoningMode: "effort", DefaultReasoningEffort: "medium", PromptForBaseURL: true},
	{Label: "deepseek", API: model.APIDeepSeek, AuthType: model.AuthAPIKey, Provider: "deepseek", Description: "DeepSeek V4 models", DefaultTokenEnv: "DEEPSEEK_API_KEY", DefaultBaseURL: "https://api.deepseek.com/anthropic", DefaultBaseURLAliases: []string{"https://api.deepseek.com/v1"}, DefaultContextWindowTokens: 1048576},
	{Label: "xiaomi", API: model.APIMimo, AuthType: model.AuthAPIKey, Provider: "xiaomi", Description: "Xiaomi Mimo models", DefaultTokenEnv: "XIAOMI_API_KEY", DefaultEndpointID: "api-cn", DefaultBaseURL: XiaomiAPIBaseURL, DefaultContextWindowTokens: 262144, Endpoints: []EndpointTemplate{
		{ID: "api-cn", BaseURL: XiaomiAPIBaseURL, Display: "api cn", Detail: "Xiaomi MiMo API CN · OpenAI-compatible", API: model.APIMimo, TokenEnv: "XIAOMI_API_KEY"},
		{ID: "token-plan-cn", BaseURL: XiaomiTokenPlanCNBaseURL, Display: "token plan cn", Detail: "Xiaomi MiMo Token Plan CN · OpenAI-compatible", API: model.APIMimo, TokenEnv: "MIMO_TOKEN_PLAN_API_KEY"},
	}},
	{Label: "minimax", API: model.APIMiniMax, AuthType: model.AuthBearerToken, Provider: "minimax", Description: "MiniMax models over an Anthropic-compatible API", DefaultTokenEnv: "MINIMAX_API_KEY", DefaultBaseURL: "https://api.minimaxi.com/anthropic", DefaultContextWindowTokens: 204800, DefaultMaxOutputTokens: 8192},
	{Label: "volcengine", API: model.APIVolcengine, AuthType: model.AuthAPIKey, Provider: "volcengine", Description: "Volcengine Ark standard or coding-plan endpoints", DefaultTokenEnv: "VOLCENGINE_API_KEY", DefaultEndpointID: "standard", DefaultBaseURL: "https://ark.cn-beijing.volces.com/api/v3", DefaultContextWindowTokens: 128000, Endpoints: []EndpointTemplate{
		{ID: "standard", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Display: "standard api", Detail: "regular Ark endpoint", API: model.APIVolcengine, TokenEnv: "VOLCENGINE_API_KEY"},
		{ID: "coding-plan", BaseURL: "https://ark.cn-beijing.volces.com/api/coding/v3", Display: "coding plan", Detail: "Ark coding-plan endpoint", API: model.APIVolcengineCoding, TokenEnv: "VOLCENGINE_API_KEY"},
	}},
	{Label: "ollama", API: model.APIOllama, AuthType: model.AuthNone, Provider: "ollama", Description: "Local Ollama runtime", DefaultBaseURL: "http://localhost:11434", DefaultContextWindowTokens: 128000, NoAuthRequired: true},
}

// ProviderTemplates returns an isolated copy of the maintained provider list.
func ProviderTemplates() []ProviderTemplate {
	out := make([]ProviderTemplate, len(providerTemplates))
	for i, template := range providerTemplates {
		out[i] = cloneProviderTemplate(template)
	}
	return out
}

// LookupProvider resolves a provider label or maintained alias.
func LookupProvider(value string) (ProviderTemplate, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, template := range providerTemplates {
		if template.Label == value || template.Provider == value {
			return cloneProviderTemplate(template), true
		}
	}
	if value == XiaomiTokenPlanCNAlias {
		template, _ := LookupProvider("xiaomi")
		template.Label = XiaomiTokenPlanCNAlias
		template.DefaultEndpointID = "token-plan-cn"
		template.DefaultBaseURL = XiaomiTokenPlanCNBaseURL
		template.DefaultContextWindowTokens = 1048576
		return template, true
	}
	return ProviderTemplate{}, false
}

// EndpointForBaseURL resolves the maintained endpoint matching baseURL. An
// empty baseURL selects the provider's declared default endpoint.
func EndpointForBaseURL(template ProviderTemplate, baseURL string) (EndpointTemplate, bool) {
	normalized := NormalizeBaseURL(baseURL)
	for _, endpoint := range template.Endpoints {
		if normalized != "" && normalized == NormalizeBaseURL(endpoint.BaseURL) {
			return endpoint, true
		}
	}
	if normalized == "" {
		for _, endpoint := range template.Endpoints {
			if strings.EqualFold(strings.TrimSpace(endpoint.ID), strings.TrimSpace(template.DefaultEndpointID)) {
				return endpoint, true
			}
		}
	}
	defaultEndpointID := strings.TrimSpace(template.DefaultEndpointID)
	if defaultEndpointID == "" {
		defaultEndpointID = "default"
	}
	if normalized == "" || normalized == NormalizeBaseURL(template.DefaultBaseURL) {
		return EndpointTemplate{ID: defaultEndpointID, BaseURL: firstNonEmpty(baseURL, template.DefaultBaseURL), API: template.API}, true
	}
	for _, alias := range template.DefaultBaseURLAliases {
		if normalized == NormalizeBaseURL(alias) {
			return EndpointTemplate{ID: defaultEndpointID, BaseURL: strings.TrimSpace(baseURL), API: template.API}, true
		}
	}
	return EndpointTemplate{}, false
}

// NormalizeBaseURL normalizes endpoint URLs for identity comparisons.
func NormalizeBaseURL(baseURL string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
}

// DefaultTokenEnv returns the maintained environment variable hint for a
// provider and endpoint.
func DefaultTokenEnv(provider string, baseURL string) string {
	template, ok := LookupProvider(provider)
	if !ok {
		return ""
	}
	if endpoint, ok := EndpointForBaseURL(template, baseURL); ok && strings.TrimSpace(endpoint.TokenEnv) != "" {
		return strings.TrimSpace(endpoint.TokenEnv)
	}
	if host := endpointHost(baseURL); host != "" {
		for _, endpoint := range template.Endpoints {
			if host == endpointHost(endpoint.BaseURL) && strings.TrimSpace(endpoint.TokenEnv) != "" {
				return strings.TrimSpace(endpoint.TokenEnv)
			}
		}
	}
	return strings.TrimSpace(template.DefaultTokenEnv)
}

func cloneProviderTemplate(template ProviderTemplate) ProviderTemplate {
	template.Endpoints = append([]EndpointTemplate(nil), template.Endpoints...)
	template.DefaultBaseURLAliases = append([]string(nil), template.DefaultBaseURLAliases...)
	template.DefaultReasoningLevels = append([]string(nil), template.DefaultReasoningLevels...)
	return template
}

func endpointHost(baseURL string) string {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(parsed.Host))
}
