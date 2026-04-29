package providers

import (
	"net/http"
	"time"
)

// APIType defines protocol dialect used by a model provider.
type APIType string

const (
	APIOpenAI              APIType = "openai"
	APIOpenAICompatible    APIType = "openai_compatible"
	APIOpenRouter          APIType = "openrouter"
	APICodeFree            APIType = "codefree"
	APIGemini              APIType = "gemini"
	APIAnthropic           APIType = "anthropic"
	APIAnthropicCompatible APIType = "anthropic_compatible"
	APIDeepSeek            APIType = "deepseek"
	APIVolcengine          APIType = "volcengine"
	APIMimo                APIType = "mimo"
	APIVolcengineCoding    APIType = "volcengine_coding_plan"
	APIOllama              APIType = "ollama"
)

// AuthType defines model provider authentication strategy.
type AuthType string

const (
	AuthAPIKey      AuthType = "api_key"
	AuthBearerToken AuthType = "bearer_token"
	AuthOAuthToken  AuthType = "oauth_token"
	AuthNone        AuthType = "none"
)

// AuthConfig is provider-agnostic auth configuration.
type AuthConfig struct {
	Type          AuthType
	TokenEnv      string
	Token         string
	CredentialRef string
	HeaderKey     string
	Prefix        string
}

// OpenRouterConfig carries OpenRouter-native request options.
// Zero values mean "use OpenRouter defaults".
type OpenRouterConfig struct {
	Models     []string
	Route      string
	Provider   map[string]any
	Transforms []string
	Plugins    []map[string]any
}

// Config is a provider-agnostic model alias definition.
type Config struct {
	Alias                     string
	Provider                  string
	API                       APIType
	Model                     string
	BaseURL                   string
	Headers                   map[string]string
	HTTPClient                *http.Client
	Timeout                   time.Duration
	MaxOutputTok              int
	ContextWindowTokens       int
	ReasoningLevels           []string
	ReasoningMode             string
	SupportedReasoningEfforts []string
	DefaultReasoningEffort    string
	ThinkingMode              string
	ThinkingBudget            int
	ReasoningEffort           string
	OpenRouter                OpenRouterConfig
	Auth                      AuthConfig
}
