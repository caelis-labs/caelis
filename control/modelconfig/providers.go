package modelconfig

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/control/modelcatalog"
)

const (
	// CodexOAuthBaseURL is the fixed ChatGPT Codex Responses API root used by
	// the maintained subscription-backed provider.
	CodexOAuthBaseURL = "https://chatgpt.com/backend-api/codex"
	// CodexOAuthCredentialRef selects the one Control-owned Codex OAuth account.
	CodexOAuthCredentialRef = "codex:default"
	// GrokOAuthBaseURL is the fixed Grok Build Responses API root used by the
	// subscription-backed Grok OAuth provider.
	GrokOAuthBaseURL = "https://cli-chat-proxy.grok.com/v1"
	// GrokOAuthCredentialRef selects the one Control-owned xAI OAuth account.
	GrokOAuthCredentialRef = "xai:default"
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
	// AuthFlowCodexOAuth uses the public Codex CLI OAuth client with browser and
	// device-code login modes.
	AuthFlowCodexOAuth AuthFlow = "codex_oauth"
	// AuthFlowGrokOAuth uses xAI's public Grok Build OAuth client with browser
	// PKCE and device-code login modes.
	AuthFlowGrokOAuth AuthFlow = "grok_oauth"
)

// EndpointTemplate describes one maintained endpoint variant for a provider.
type EndpointTemplate struct {
	ID             string
	BaseURL        string
	Display        string
	Detail         string
	API            model.APIType
	AuthType       model.AuthType
	NoAuthRequired bool
	// CatalogProvider selects the capability-catalog namespace for this
	// endpoint. Empty inherits ProviderTemplate.Provider.
	CatalogProvider string
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
	{Label: "codex", API: model.APIOpenAICodex, AuthType: model.AuthOAuthToken, AuthFlow: AuthFlowCodexOAuth, AuthDisplay: "browser/device oauth", PreserveModelOrder: true, Provider: "openai-codex", Description: "ChatGPT subscription models through Codex", DefaultBaseURL: CodexOAuthBaseURL, DefaultContextWindowTokens: 272000, DefaultMaxOutputTokens: 32768, DefaultReasoningLevels: []string{"low", "medium", "high", "xhigh"}, DefaultReasoningMode: "effort", DefaultReasoningEffort: "medium"},
	{Label: "grok", API: model.APIXAIResponses, AuthType: model.AuthOAuthToken, AuthFlow: AuthFlowGrokOAuth, AuthDisplay: "browser/device oauth", PreserveModelOrder: true, Provider: "xai", Description: "Grok models through an eligible xAI subscription", DefaultBaseURL: GrokOAuthBaseURL, DefaultContextWindowTokens: 500000, DefaultMaxOutputTokens: 32768, DefaultReasoningLevels: []string{"low", "medium", "high"}, DefaultReasoningMode: "effort", DefaultReasoningEffort: "high"},
	{Label: "openai", API: model.APIOpenAI, AuthType: model.AuthAPIKey, Provider: "openai", Description: "OpenAI-hosted models", DefaultBaseURL: "https://api.openai.com/v1", DefaultContextWindowTokens: 128000},
	{Label: "openai-compatible", API: model.APIOpenAICompatible, AuthType: model.AuthAPIKey, Provider: "openai-compatible", Description: "OpenAI-compatible proxy or self-hosted endpoint", DefaultBaseURL: "https://api.openai.com/v1", DefaultContextWindowTokens: 262144, DefaultMaxOutputTokens: 32768, DefaultReasoningLevels: []string{"none", "minimal", "low", "medium", "high", "xhigh"}, DefaultReasoningMode: "effort", DefaultReasoningEffort: "medium", PromptForBaseURL: true},
	{Label: "openrouter", API: model.APIOpenRouter, AuthType: model.AuthAPIKey, Provider: "openrouter", Description: "OpenRouter multi-provider routing", DefaultBaseURL: "https://openrouter.ai/api/v1", DefaultContextWindowTokens: 262144, UseModelDirectory: true},
	{Label: "gemini", API: model.APIGemini, AuthType: model.AuthAPIKey, Provider: "gemini", Description: "Google Gemini API", DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta", DefaultContextWindowTokens: 128000},
	{Label: "anthropic", API: model.APIAnthropic, AuthType: model.AuthAPIKey, Provider: "anthropic", Description: "Anthropic Claude API", DefaultBaseURL: "https://api.anthropic.com", DefaultContextWindowTokens: 200000, DefaultMaxOutputTokens: 1024},
	{Label: "anthropic-compatible", API: model.APIAnthropicCompatible, AuthType: model.AuthAPIKey, Provider: "anthropic-compatible", Description: "Anthropic-compatible proxy or self-hosted endpoint", DefaultBaseURL: "https://api.anthropic.com", DefaultContextWindowTokens: 200000, DefaultMaxOutputTokens: 32768, DefaultReasoningLevels: []string{"none", "minimal", "low", "medium", "high", "max"}, DefaultReasoningMode: "effort", DefaultReasoningEffort: "medium", PromptForBaseURL: true},
	{Label: "deepseek", API: model.APIDeepSeek, AuthType: model.AuthAPIKey, Provider: "deepseek", Description: "DeepSeek V4 models", DefaultBaseURL: "https://api.deepseek.com/anthropic", DefaultBaseURLAliases: []string{"https://api.deepseek.com/v1"}, DefaultContextWindowTokens: 1048576},
	{Label: "xiaomi", API: model.APIMimo, AuthType: model.AuthAPIKey, Provider: "xiaomi", Description: "Xiaomi Mimo models", DefaultEndpointID: "api-cn", DefaultBaseURL: XiaomiAPIBaseURL, DefaultContextWindowTokens: 262144, Endpoints: []EndpointTemplate{
		{ID: "api-cn", BaseURL: XiaomiAPIBaseURL, Display: "api cn", Detail: "Xiaomi MiMo API CN · OpenAI-compatible", API: model.APIMimo},
		{ID: "token-plan-cn", BaseURL: XiaomiTokenPlanCNBaseURL, Display: "token plan cn", Detail: "Xiaomi MiMo Token Plan CN · OpenAI-compatible", API: model.APIMimo},
	}},
	{Label: "minimax", API: model.APIMiniMax, AuthType: model.AuthBearerToken, Provider: "minimax", Description: "MiniMax models over an Anthropic-compatible API", DefaultBaseURL: "https://api.minimaxi.com/anthropic", DefaultContextWindowTokens: 204800, DefaultMaxOutputTokens: 8192},
	{Label: "volcengine", API: model.APIVolcengine, AuthType: model.AuthAPIKey, Provider: "volcengine", Description: "Volcengine Ark models", DefaultEndpointID: "standard", DefaultBaseURL: "https://ark.cn-beijing.volces.com/api/v3", DefaultContextWindowTokens: 128000, Endpoints: []EndpointTemplate{
		{ID: "standard", BaseURL: "https://ark.cn-beijing.volces.com/api/v3", Display: "standard api", Detail: "regular Ark endpoint", API: model.APIVolcengine},
		{ID: "coding-plan", BaseURL: "https://ark.cn-beijing.volces.com/api/coding/v3", Display: "coding plan", Detail: "Ark coding-plan endpoint", API: model.APIVolcengineCoding},
	}},
	{Label: "ollama", API: model.APIOllama, AuthType: model.AuthNone, Provider: "ollama", Description: "Local Ollama runtime or direct Ollama Cloud API", PreserveModelOrder: true, DefaultEndpointID: "local", DefaultBaseURL: "http://localhost:11434", DefaultContextWindowTokens: 128000, Endpoints: []EndpointTemplate{
		{ID: "local", BaseURL: "http://localhost:11434", Display: "local", Detail: "Installed models through the local Ollama service", API: model.APIOllama, AuthType: model.AuthNone, NoAuthRequired: true},
		{ID: "cloud", BaseURL: "https://ollama.com", Display: "cloud api", Detail: "Frontier models directly through Ollama Cloud", API: model.APIOllama, AuthType: model.AuthAPIKey, CatalogProvider: modelcatalog.OllamaCloudProvider},
	}},
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
		return EndpointTemplate{
			ID:             defaultEndpointID,
			BaseURL:        firstNonEmpty(baseURL, template.DefaultBaseURL),
			API:            template.API,
			AuthType:       template.AuthType,
			NoAuthRequired: template.NoAuthRequired,
		}, true
	}
	for _, alias := range template.DefaultBaseURLAliases {
		if normalized == NormalizeBaseURL(alias) {
			return EndpointTemplate{
				ID:             defaultEndpointID,
				BaseURL:        strings.TrimSpace(baseURL),
				API:            template.API,
				AuthType:       template.AuthType,
				NoAuthRequired: template.NoAuthRequired,
			}, true
		}
	}
	return EndpointTemplate{}, false
}

// NormalizeBaseURL normalizes endpoint URLs for identity comparisons.
func NormalizeBaseURL(baseURL string) string {
	return strings.ToLower(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
}

func cloneProviderTemplate(template ProviderTemplate) ProviderTemplate {
	template.Endpoints = append([]EndpointTemplate(nil), template.Endpoints...)
	template.DefaultBaseURLAliases = append([]string(nil), template.DefaultBaseURLAliases...)
	template.DefaultReasoningLevels = append([]string(nil), template.DefaultReasoningLevels...)
	return template
}
