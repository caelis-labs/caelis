package model

import (
	"context"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
)

// APIType identifies the model-provider protocol dialect for one configured
// endpoint.
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

// AuthType identifies how a model-provider endpoint authenticates.
type AuthType = coremodel.AuthType

const (
	AuthAPIKey      = coremodel.AuthAPIKey
	AuthBearerToken = coremodel.AuthBearerToken
	AuthOAuthToken  = coremodel.AuthOAuthToken
	AuthNone        = coremodel.AuthNone
)

type Client = LLM

type ProviderConfig struct {
	ID       string
	Alias    string
	Model    string
	BaseURL  string
	Metadata map[string]any
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
