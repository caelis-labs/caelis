package providers

import (
	"net/http"
	"time"

	"github.com/caelis-labs/caelis/ports/model"
)

// APIType defines protocol dialect used by a model provider.
type APIType = model.APIType

const (
	APIOpenAI              = model.APIOpenAI
	APIOpenAICompatible    = model.APIOpenAICompatible
	APIOpenRouter          = model.APIOpenRouter
	APICodeFree            = model.APICodeFree
	APIGemini              = model.APIGemini
	APIAnthropic           = model.APIAnthropic
	APIAnthropicCompatible = model.APIAnthropicCompatible
	APIDeepSeek            = model.APIDeepSeek
	APIMiniMax             = model.APIMiniMax
	APIVolcengine          = model.APIVolcengine
	APIMimo                = model.APIMimo
	APIVolcengineCoding    = model.APIVolcengineCoding
	APIOllama              = model.APIOllama
)

// AuthType defines model provider authentication strategy.
type AuthType = model.AuthType

const (
	AuthAPIKey      = model.AuthAPIKey
	AuthBearerToken = model.AuthBearerToken
	AuthOAuthToken  = model.AuthOAuthToken
	AuthNone        = model.AuthNone
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
	StreamFirstEventTimeout   time.Duration
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
	Retry                     model.RetryConfig
}
