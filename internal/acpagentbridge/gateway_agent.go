package acpagentbridge

import (
	"context"
	"fmt"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	bridgeassembly "github.com/caelis-labs/caelis/internal/acpagentbridge/assembly"
	assemblyapi "github.com/caelis-labs/caelis/internal/controlassembly"
	"github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

type GatewayAgentConfig struct {
	Runtime              agent.Runtime
	Sessions             session.Service
	SessionClient        controlclient.SessionClient
	Resolver             kernel.RuntimeResolver
	ApprovalReviewer     kernel.ApprovalReviewer
	Assembly             assemblyapi.ResolvedAssembly
	AppName              string
	UserID               string
	WorkspaceKey         string
	WorkspaceCWD         string
	SurfaceBuilder       SurfaceBuilder
	PromptRouterFactory  PromptRouterFactory
	SlashResultFormatter SlashResultFormatter
	TaskStreams          taskstream.Service
	TaskStreamPrincipal  taskstream.Principal
	TaskStreamClient     taskstream.Client
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
	if cfg.SessionClient == nil && cfg.Resolver == nil {
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
	var buildAgentSpec BuildAgentSpecFunc
	if cfg.SessionClient == nil {
		buildAgentSpec = func(ctx context.Context, session session.Session, req acp.PromptRequest) (agent.AgentSpec, error) {
			resolved, err := cfg.Resolver.ResolveTurn(ctx, kernel.TurnIntent{
				SessionRef: session.SessionRef,
				Surface:    "acp",
			})
			if err != nil {
				return agent.AgentSpec{}, err
			}
			return resolved.RunRequest.AgentSpec, nil
		}
	}
	return New(Config{
		Runtime:               cfg.Runtime,
		Sessions:              cfg.Sessions,
		SessionClient:         cfg.SessionClient,
		BuildAgentSpec:        buildAgentSpec,
		Modes:                 surface,
		ApprovalModes:         approvalSurface,
		Config:                surface,
		Models:                surface,
		Commands:              surface,
		PromptRouterFactory:   cfg.PromptRouterFactory,
		SlashResultFormatter:  cfg.SlashResultFormatter,
		PromptCaps:            surface,
		TaskStreams:           cfg.TaskStreams,
		TaskStreamPrincipal:   cfg.TaskStreamPrincipal,
		TaskStreamClient:      cfg.TaskStreamClient,
		ApprovalReviewer:      cfg.ApprovalReviewer,
		ApprovalModelResolver: cfg.Resolver,
		AppName:               cfg.AppName,
		UserID:                cfg.UserID,
		WorkspaceKey:          cfg.WorkspaceKey,
		WorkspaceCWD:          cfg.WorkspaceCWD,
		AgentInfo:             &acp.Implementation{Name: cfg.AppName, Version: version.String()},
	})
}
