package model

import "context"

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
