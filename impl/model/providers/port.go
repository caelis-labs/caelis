package providers

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/model"
)

type Provider struct {
	API     APIType
	Factory *Factory
	Name    string
}

func (p Provider) ID() string {
	if id := strings.TrimSpace(p.Name); id != "" {
		return id
	}
	if p.API != "" {
		return string(p.API)
	}
	return "model-providers"
}

func (p Provider) NewClient(ctx context.Context, cfg model.ProviderConfig) (model.Client, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	alias := firstNonEmptyPortValue(cfg.Alias, cfg.Model, cfg.ID)
	if alias == "" {
		return nil, fmt.Errorf("providers: alias or model is required")
	}
	factory := p.Factory
	if factory == nil {
		factory = NewFactory()
	}
	providerCfg := Config{
		Alias:   alias,
		API:     p.apiFor(cfg),
		Model:   cfg.Model,
		BaseURL: cfg.BaseURL,
	}
	if providerCfg.Model == "" {
		providerCfg.Model = alias
	}
	if token, ok := metadataString(cfg.Metadata, "token"); ok {
		providerCfg.Auth.Token = token
	}
	if tokenEnv, ok := metadataString(cfg.Metadata, "token_env"); ok {
		providerCfg.Auth.TokenEnv = tokenEnv
	}
	if authType, ok := metadataString(cfg.Metadata, "auth_type"); ok {
		providerCfg.Auth.Type = AuthType(authType)
	}
	if err := factory.Register(providerCfg); err != nil {
		return nil, err
	}
	return factory.NewByAlias(alias)
}

func (p Provider) ListModels(ctx context.Context, _ model.ListModelsRequest) ([]model.ModelInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.Factory == nil {
		return nil, nil
	}
	aliases := p.Factory.ListModels()
	out := make([]model.ModelInfo, 0, len(aliases))
	for _, alias := range aliases {
		info := model.ModelInfo{
			Alias:    alias,
			Provider: p.ID(),
			Model:    alias,
		}
		if cfg, ok := p.Factory.ConfigForAlias(alias); ok {
			info.Provider = firstNonEmptyPortValue(cfg.Provider, string(cfg.API), p.ID())
			info.Model = firstNonEmptyPortValue(cfg.Model, alias)
		}
		out = append(out, info)
	}
	return out, nil
}

func (Provider) Capabilities() model.ProviderCapabilities {
	return model.ProviderCapabilities{
		ListModels: true,
		Streaming:  true,
		ToolCalls:  true,
		Reasoning:  true,
	}
}

func (p Provider) apiFor(cfg model.ProviderConfig) APIType {
	if p.API != "" {
		return p.API
	}
	if api, ok := metadataString(cfg.Metadata, "api"); ok {
		return APIType(api)
	}
	switch strings.ToLower(strings.TrimSpace(cfg.ID)) {
	case string(APIOpenAI):
		return APIOpenAI
	case string(APIOpenRouter):
		return APIOpenRouter
	case string(APICodeFree):
		return APICodeFree
	case string(APIGemini):
		return APIGemini
	case string(APIAnthropic):
		return APIAnthropic
	case string(APIDeepSeek):
		return APIDeepSeek
	case string(APIMiniMax):
		return APIMiniMax
	case string(APIMimo):
		return APIMimo
	case string(APIVolcengine):
		return APIVolcengine
	case string(APIVolcengineCoding), "volcengine_coding":
		return APIVolcengineCoding
	case string(APIOllama):
		return APIOllama
	default:
		return APIOpenAICompatible
	}
}

type Registry struct {
	Factory *Factory
}

func (r Registry) Resolve(ctx context.Context, ref model.ModelRef) (model.Client, model.ModelInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, model.ModelInfo{}, err
	}
	if r.Factory == nil {
		return nil, model.ModelInfo{}, fmt.Errorf("providers: factory is nil")
	}
	alias := firstNonEmptyPortValue(ref.Alias, ref.Model)
	if alias == "" {
		return nil, model.ModelInfo{}, fmt.Errorf("providers: model alias is required")
	}
	client, err := r.Factory.NewByAlias(alias)
	if err != nil {
		return nil, model.ModelInfo{}, err
	}
	info := model.ModelInfo{
		Alias:    alias,
		Provider: "model-providers",
		Model:    alias,
	}
	if cfg, ok := r.Factory.ConfigForAlias(alias); ok {
		info.Provider = firstNonEmptyPortValue(cfg.Provider, string(cfg.API), info.Provider)
		info.Model = firstNonEmptyPortValue(cfg.Model, alias)
	}
	return client, info, nil
}

func (r Registry) ListAliases(ctx context.Context) ([]model.ModelAlias, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.Factory == nil {
		return nil, nil
	}
	aliases := r.Factory.ListModels()
	out := make([]model.ModelAlias, 0, len(aliases))
	for _, alias := range aliases {
		modelName := alias
		if cfg, ok := r.Factory.ConfigForAlias(alias); ok {
			modelName = firstNonEmptyPortValue(cfg.Model, alias)
		}
		out = append(out, model.ModelAlias{Alias: alias, Model: modelName})
	}
	return out, nil
}

func metadataString(values map[string]any, key string) (string, bool) {
	if len(values) == 0 {
		return "", false
	}
	value, ok := values[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	return text, ok && text != ""
}

func firstNonEmptyPortValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var _ model.Provider = Provider{}
var _ model.Registry = Registry{}
