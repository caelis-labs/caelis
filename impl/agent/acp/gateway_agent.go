package acp

import (
	"context"
	"fmt"

	bridgeassembly "github.com/OnslaughtSnail/caelis/impl/agent/acp/assembly"
	"github.com/OnslaughtSnail/caelis/internal/version"
	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/agent"
	assemblyapi "github.com/OnslaughtSnail/caelis/ports/assembly"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp"
)

type GatewayAgentConfig struct {
	Runtime        agent.Runtime
	Sessions       session.Service
	Gateway        *kernel.Gateway
	Assembly       assemblyapi.ResolvedAssembly
	AppName        string
	UserID         string
	SurfaceBuilder SurfaceBuilder
}

type SurfaceRequest struct {
	Modes            acp.ModeProvider
	UseFallbackModes bool
	Config           acp.ConfigProvider
}

type SurfaceBuilder func(SurfaceRequest) Surface

type Surface interface {
	acp.ModeProvider
	acp.ConfigProvider
	acp.ModelProvider
	acp.CommandProvider
	acp.PromptCapabilitiesProvider
}

func NewGatewayAgent(cfg GatewayAgentConfig) (*RuntimeAgent, error) {
	if cfg.Gateway == nil {
		return nil, fmt.Errorf("impl/agent/acp: gateway is required")
	}
	if cfg.SurfaceBuilder == nil {
		return nil, fmt.Errorf("impl/agent/acp: surface builder is required")
	}
	modes, configs := bridgeassembly.ProvidersFromAssembly(bridgeassembly.ProviderConfig{
		AppName:  cfg.AppName,
		UserID:   cfg.UserID,
		Assembly: cfg.Assembly,
		Sessions: cfg.Sessions,
	})
	surface := cfg.SurfaceBuilder(SurfaceRequest{
		Modes:            modes,
		UseFallbackModes: len(cfg.Assembly.Modes) > 0,
		Config:           configs,
	})
	return New(Config{
		Runtime:  cfg.Runtime,
		Sessions: cfg.Sessions,
		BuildAgentSpec: func(ctx context.Context, session session.Session, req acp.PromptRequest) (agent.AgentSpec, error) {
			resolver := cfg.Gateway.Resolver()
			if resolver == nil {
				return agent.AgentSpec{}, fmt.Errorf("gatewayapp: resolver not available")
			}
			resolved, err := resolver.ResolveTurn(ctx, kernel.TurnIntent{
				SessionRef: session.SessionRef,
				Surface:    "acp",
			})
			if err != nil {
				return agent.AgentSpec{}, err
			}
			return resolved.RunRequest.AgentSpec, nil
		},
		Modes:                 surface,
		Config:                surface,
		Models:                surface,
		Commands:              surface,
		PromptCaps:            surface,
		ApprovalReviewer:      cfg.Gateway.ApprovalReviewer(),
		ApprovalModelResolver: cfg.Gateway.Resolver(),
		AppName:               cfg.AppName,
		UserID:                cfg.UserID,
		AgentInfo:             &acp.Implementation{Name: cfg.AppName, Version: version.String()},
	})
}
