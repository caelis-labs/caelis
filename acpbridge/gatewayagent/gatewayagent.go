package gatewayagent

import (
	"context"
	"fmt"

	"github.com/OnslaughtSnail/caelis/acp"
	"github.com/OnslaughtSnail/caelis/acpbridge/agentruntime"
	bridgeassembly "github.com/OnslaughtSnail/caelis/acpbridge/assembly"
	"github.com/OnslaughtSnail/caelis/app/gatewayapp"
	"github.com/OnslaughtSnail/caelis/gateway"
	"github.com/OnslaughtSnail/caelis/internal/version"
	sdkruntime "github.com/OnslaughtSnail/caelis/sdk/runtime"
	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

func New(stack *gatewayapp.Stack) (*agentruntime.RuntimeAgent, error) {
	deps, err := stack.ACPAgentDependencies()
	if err != nil {
		return nil, err
	}
	modes, configs := bridgeassembly.ProvidersFromAssembly(bridgeassembly.ProviderConfig{
		AppName:  deps.AppName,
		UserID:   deps.UserID,
		Assembly: deps.Assembly,
		Sessions: deps.Sessions,
	})
	surface := stack.ACPSurface(modes, len(deps.Assembly.Modes) > 0, configs)
	return agentruntime.New(agentruntime.Config{
		Runtime:  deps.Runtime,
		Sessions: deps.Sessions,
		BuildAgentSpec: func(ctx context.Context, session sdksession.Session, req acp.PromptRequest) (sdkruntime.AgentSpec, error) {
			resolver := deps.Gateway.Resolver()
			if resolver == nil {
				return sdkruntime.AgentSpec{}, fmt.Errorf("gatewayapp: resolver not available")
			}
			resolved, err := resolver.ResolveTurn(ctx, gateway.TurnIntent{
				SessionRef: session.SessionRef,
				Surface:    "acp",
			})
			if err != nil {
				return sdkruntime.AgentSpec{}, err
			}
			return resolved.RunRequest.AgentSpec, nil
		},
		Modes:                 surface,
		Config:                surface,
		Models:                surface,
		Commands:              surface,
		PromptCaps:            surface,
		ApprovalReviewer:      deps.Gateway.ApprovalReviewer(),
		ApprovalModelResolver: deps.Gateway.Resolver(),
		AppName:               deps.AppName,
		UserID:                deps.UserID,
		AgentInfo:             &acp.Implementation{Name: deps.AppName, Version: version.String()},
	})
}
