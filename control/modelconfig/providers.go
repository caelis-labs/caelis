package modelconfig

import (
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/model"
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
	DefaultEndpointID          string
	DefaultBaseURL             string
	DefaultBaseURLAliases      []string
	DefaultContextWindowTokens int
	DefaultMaxOutputTokens     int
	NoAuthRequired             bool
	PromptForBaseURL           bool
	Endpoints                  []EndpointTemplate
}

var providerTemplates = []ProviderTemplate{
	{Label: "codex", Provider: "openai-codex", API: model.APIOpenAICodex, AuthType: model.AuthOAuthToken, AuthFlow: AuthFlowCodexOAuth, AuthDisplay: "browser/device oauth", Description: "ChatGPT subscription", DefaultBaseURL: CodexOAuthBaseURL, DefaultContextWindowTokens: 258400, DefaultMaxOutputTokens: 32768},
	{Label: "grok", Provider: "xai", API: model.APIXAIResponses, AuthType: model.AuthOAuthToken, AuthFlow: AuthFlowGrokOAuth, AuthDisplay: "browser/device oauth", Description: "Grok subscription", DefaultBaseURL: GrokOAuthBaseURL, DefaultContextWindowTokens: 500000, DefaultMaxOutputTokens: 32768},
	{Label: "deepseek", Provider: "deepseek", API: model.APIDeepSeek, AuthType: model.AuthAPIKey, Description: "DeepSeek API", DefaultBaseURL: "https://api.deepseek.com/anthropic", DefaultBaseURLAliases: []string{"https://api.deepseek.com/v1"}, DefaultContextWindowTokens: 1048576},
	{Label: "xiaomi", Provider: "xiaomi", API: model.APIMimo, AuthType: model.AuthAPIKey, Description: "MiMo API or Token Plan", DefaultEndpointID: "api-cn", DefaultBaseURL: XiaomiAPIBaseURL, DefaultContextWindowTokens: 262144, Endpoints: []EndpointTemplate{
		{ID: "api-cn", BaseURL: XiaomiAPIBaseURL, Display: "api cn", Detail: "MiMo API", API: model.APIMimo},
		{ID: "token-plan-cn", BaseURL: XiaomiTokenPlanCNBaseURL, Display: "token plan cn", Detail: "MiMo Token Plan", API: model.APIMimo},
	}},
	{Label: "magpie", Provider: "magpie", API: model.APIOpenAIResponses, AuthType: model.AuthAPIKey, Description: "Local gateway", DefaultEndpointID: "local", DefaultBaseURL: "http://127.0.0.1:3425/v1", DefaultContextWindowTokens: 128000, Endpoints: []EndpointTemplate{
		{ID: "local", BaseURL: "http://127.0.0.1:3425/v1", Display: "local", Detail: "Running Magpie gateway", API: model.APIOpenAIResponses, AuthType: model.AuthNone, NoAuthRequired: true},
	}},
	{Label: "ollama", Provider: "ollama", API: model.APIOllama, AuthType: model.AuthNone, Description: "Local Ollama models", DefaultEndpointID: "local", DefaultBaseURL: "http://localhost:11434", DefaultContextWindowTokens: 128000, Endpoints: []EndpointTemplate{
		{ID: "local", BaseURL: "http://localhost:11434", Display: "local", Detail: "Installed Ollama models", API: model.APIOllama, AuthType: model.AuthNone, NoAuthRequired: true},
	}},
	{Label: "custom", Provider: "custom", API: model.APIOpenAIResponses, AuthType: model.AuthAPIKey, Description: "Custom API endpoint", PromptForBaseURL: true, DefaultContextWindowTokens: 128000, DefaultMaxOutputTokens: 8192},
}

// ProviderTemplates returns an isolated copy of the maintained provider list.
func ProviderTemplates() []ProviderTemplate {
	out := make([]ProviderTemplate, len(providerTemplates))
	for i, template := range providerTemplates {
		out[i] = cloneProviderTemplate(template)
	}
	return out
}

// LookupProvider resolves a maintained preset or protocol choice for onboarding.
func LookupProvider(value string) (ProviderTemplate, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, template := range providerTemplates {
		if template.Label == value || template.Provider == value {
			return cloneProviderTemplate(template), true
		}
	}
	if value == "mimo" {
		return LookupProvider("xiaomi")
	}
	if value == XiaomiTokenPlanCNAlias {
		template, _ := LookupProvider("xiaomi")
		template.Label = XiaomiTokenPlanCNAlias
		template.DefaultEndpointID, template.DefaultBaseURL = "token-plan-cn", XiaomiTokenPlanCNBaseURL
		return template, true
	}
	if value == "magpie-judgment" {
		template, _ := LookupProvider("magpie")
		template.Label, template.API, template.DefaultEndpointID = value, APISystemOne, "judgment"
		template.Endpoints = []EndpointTemplate{{ID: "judgment", BaseURL: template.DefaultBaseURL, Display: "local", Detail: "Magpie judgment models", API: APISystemOne, AuthType: model.AuthNone, NoAuthRequired: true}}
		return template, true
	}
	for _, protocol := range customProtocols {
		if protocol.Label != value {
			continue
		}
		api := protocol.API
		template, _ := LookupProvider("custom")
		template.Label, template.API, template.DefaultEndpointID = value, api, strings.TrimPrefix(value, "custom-")
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
	return template
}

// CustomProtocols returns the protocol labels accepted by the single custom
// connection preset. They select transport, not another provider catalog.
func CustomProtocols() []string {
	out := make([]string, 0, len(customProtocols))
	for _, protocol := range customProtocols {
		out = append(out, protocol.Label)
	}
	return out
}

var customProtocols = []struct {
	Label string
	API   model.APIType
}{
	{"custom-responses", model.APIOpenAIResponses},
	{"custom-chat", model.APIOpenAICompatible},
	{"custom-messages", model.APIAnthropicCompatible},
	{"custom-judgment", APISystemOne},
}
