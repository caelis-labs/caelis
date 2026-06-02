package model

import (
	"context"
	"time"
)

// APIType identifies the model-provider protocol dialect for one configured
// endpoint.
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
	APIMiniMax             APIType = "minimax"
	APIVolcengine          APIType = "volcengine"
	APIMimo                APIType = "mimo"
	APIVolcengineCoding    APIType = "volcengine_coding_plan"
	APIOllama              APIType = "ollama"
)

// AuthType identifies how a model-provider endpoint authenticates.
type AuthType string

const (
	AuthAPIKey      AuthType = "api_key"
	AuthBearerToken AuthType = "bearer_token"
	AuthOAuthToken  AuthType = "oauth_token"
	AuthNone        AuthType = "none"
)

type Client = LLM

type ProviderConfig struct {
	ID                      string
	Alias                   string
	Model                   string
	BaseURL                 string
	StreamFirstEventTimeout time.Duration
	Metadata                map[string]any
}

type ModelRef struct {
	Alias string
	Model string
}

type ModelInfo struct {
	Alias        string
	Provider     string
	Model        string
	DisplayName  string
	Capabilities map[string]any
}

type ProviderCapabilities struct {
	ListModels bool
	Streaming  bool
	ToolCalls  bool
	Reasoning  bool
}

type ListModelsRequest struct {
	Provider string
}

type ModelAlias struct {
	Alias string
	Model string
}

type Provider interface {
	ID() string
	NewClient(context.Context, ProviderConfig) (LLM, error)
	ListModels(context.Context, ListModelsRequest) ([]ModelInfo, error)
	Capabilities() ProviderCapabilities
}

type Registry interface {
	Resolve(context.Context, ModelRef) (LLM, ModelInfo, error)
	ListAliases(context.Context) ([]ModelAlias, error)
}
