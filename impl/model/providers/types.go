package providers

import (
	"net/http"
	"time"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/ports/model"
)

// APIType defines protocol dialect used by a model provider.
type APIType = coremodel.APIType

const (
	APIOpenAI              = coremodel.APIOpenAI
	APIOpenAICompatible    = coremodel.APIOpenAICompatible
	APIOpenRouter          = coremodel.APIOpenRouter
	APICodeFree            = coremodel.APICodeFree
	APIGemini              = coremodel.APIGemini
	APIAnthropic           = coremodel.APIAnthropic
	APIAnthropicCompatible = coremodel.APIAnthropicCompatible
	APIDeepSeek            = coremodel.APIDeepSeek
	APIMiniMax             = coremodel.APIMiniMax
	APIVolcengine          = coremodel.APIVolcengine
	APIMimo                = coremodel.APIMimo
	APIVolcengineCoding    = coremodel.APIVolcengineCoding
	APIOllama              = coremodel.APIOllama
)

// AuthType defines model provider authentication strategy.
type AuthType = coremodel.AuthType

const (
	AuthAPIKey      = coremodel.AuthAPIKey
	AuthBearerToken = coremodel.AuthBearerToken
	AuthOAuthToken  = coremodel.AuthOAuthToken
	AuthNone        = coremodel.AuthNone
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
	Retry                     model.RetryConfig
}
