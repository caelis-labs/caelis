// Package modelrouter selects configured model providers at request time.
package modelrouter

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/plugin"
	appregistry "github.com/OnslaughtSnail/caelis/internal/app/registry"
	appsettings "github.com/OnslaughtSnail/caelis/internal/app/settings"
)

type Router struct {
	settings *appsettings.Manager
	registry *appregistry.Registry
}

func New(settings *appsettings.Manager, registry *appregistry.Registry) (*Router, error) {
	if settings == nil {
		return nil, errors.New("app/modelrouter: settings manager is required")
	}
	if registry == nil {
		return nil, errors.New("app/modelrouter: registry is required")
	}
	return &Router{settings: settings, registry: registry}, nil
}

func (r *Router) ID() string {
	return "configured-model-router"
}

func (r *Router) Models(context.Context) ([]model.ModelInfo, error) {
	choices, err := r.settings.ListModelChoices()
	if err != nil {
		return nil, err
	}
	out := make([]model.ModelInfo, 0, len(choices))
	for _, choice := range choices {
		out = append(out, model.ModelInfo{
			ID:       choice.ID,
			Name:     choice.Alias,
			Provider: choice.Provider,
		})
	}
	return out, nil
}

func (r *Router) Stream(ctx context.Context, req model.Request) (model.Stream, error) {
	cfg, err := r.settings.ResolveModel(req.Model)
	if err != nil {
		return nil, err
	}
	providerName := strings.ToLower(firstNonEmpty(cfg.Provider, "openai_compatible"))
	factory, ok := r.registry.ModelProvider(providerName)
	if !ok {
		return nil, errors.New("app/modelrouter: model provider is not registered: " + providerName)
	}
	provider, err := factory(ctx, plugin.ModelProviderConfig{
		ID:        cfg.ID,
		Profile:   cfg.ProfileID,
		Provider:  providerName,
		Endpoint:  cfg.BaseURL,
		Model:     cfg.Model,
		Token:     cfg.Token,
		TokenEnv:  cfg.TokenEnv,
		AuthType:  cfg.AuthType,
		HeaderKey: cfg.HeaderKey,
		Meta:      maps.Clone(cfg.Meta),
	})
	if err != nil {
		return nil, err
	}
	next := cloneRequest(req)
	next.Model = cfg.Model
	if next.Reasoning.Effort == "" {
		next.Reasoning.Effort = cfg.ReasoningEffort
	}
	return provider.Stream(ctx, next)
}

func cloneRequest(in model.Request) model.Request {
	out := in
	out.Messages = make([]model.Message, 0, len(in.Messages))
	for _, message := range in.Messages {
		out.Messages = append(out.Messages, model.CloneMessage(message))
	}
	out.Tools = append([]model.ToolSpec(nil), in.Tools...)
	out.Instructions = append([]string(nil), in.Instructions...)
	if in.Output != nil {
		output := *in.Output
		output.JSONSchema = maps.Clone(in.Output.JSONSchema)
		out.Output = &output
	}
	out.Meta = maps.Clone(in.Meta)
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

var _ model.Provider = (*Router)(nil)
