package acpagentbridge

import (
	"context"
	"fmt"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	bridgeassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	assemblyapi "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

type GatewayAgentConfig struct {
	Runtime             agent.Runtime
	Sessions            session.Service
	Resolver            gateway.RuntimeResolver
	ApprovalReviewer    gateway.ApprovalReviewer
	Assembly            assemblyapi.ResolvedAssembly
	AppName             string
	UserID              string
	WorkspaceKey        string
	SurfaceBuilder      SurfaceBuilder
	PromptRouterFactory PromptRouterFactory
	TaskStreams         taskstream.Service
	TaskStreamPrincipal taskstream.Principal
}

type SurfaceRequest struct {
	// Modes are ACP client-visible app/session modes. When UseFallbackModes is
	// true they may be assembly-owned values such as "default" or "plan"; they
	// must not be used as approval-routing modes.
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
	if cfg.Resolver == nil {
		return nil, fmt.Errorf("internal/acpagentbridge: gateway resolver is required")
	}
	if cfg.SurfaceBuilder == nil {
		return nil, fmt.Errorf("internal/acpagentbridge: surface builder is required")
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
	approvalSurface := cfg.SurfaceBuilder(SurfaceRequest{
		Modes:            nil,
		UseFallbackModes: false,
		Config:           nil,
	})
	return New(Config{
		Runtime:  cfg.Runtime,
		Sessions: cfg.Sessions,
		BuildAgentSpec: func(ctx context.Context, session session.Session, req acp.PromptRequest) (agent.AgentSpec, error) {
			resolved, err := cfg.Resolver.ResolveTurn(ctx, gateway.TurnIntent{
				SessionRef: session.SessionRef,
				Surface:    "acp",
			})
			if err != nil {
				return agent.AgentSpec{}, err
			}
			return resolved.RunRequest.AgentSpec, nil
		},
		Modes:                 surface,
		ApprovalModes:         approvalSurface,
		Config:                surface,
		Models:                surface,
		Commands:              surface,
		PromptRouterFactory:   cfg.PromptRouterFactory,
		PromptCaps:            surface,
		TaskStreams:           cfg.TaskStreams,
		TaskStreamPrincipal:   cfg.TaskStreamPrincipal,
		ApprovalReviewer:      cfg.ApprovalReviewer,
		ApprovalModelResolver: cfg.Resolver,
		AppName:               cfg.AppName,
		UserID:                cfg.UserID,
		WorkspaceKey:          cfg.WorkspaceKey,
		AgentInfo:             &acp.Implementation{Name: cfg.AppName, Version: version.String()},
	})
}
