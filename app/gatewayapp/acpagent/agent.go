package acpagent

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/app/gatewayapp/internal/agentregistry"
	runtimeacp "github.com/caelis-labs/caelis/impl/agent/acp"
	controlpromptrouter "github.com/caelis-labs/caelis/internal/controlpromptrouter"
	controlcommands "github.com/caelis-labs/caelis/ports/controlcommand"
	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/ports/session"
	"github.com/caelis-labs/caelis/protocol/acp/control"
)

func NewFromStack(stack *gatewayapp.Stack) (*runtimeacp.RuntimeAgent, error) {
	deps, err := stack.ACPAgentDependencies()
	if err != nil {
		return nil, err
	}
	return runtimeacp.NewGatewayAgent(runtimeacp.GatewayAgentConfig{
		Runtime:          deps.Runtime,
		Sessions:         deps.Sessions,
		Resolver:         deps.Resolver,
		ApprovalReviewer: deps.ApprovalReviewer,
		Assembly:         deps.Assembly,
		AppName:          deps.AppName,
		UserID:           deps.UserID,
		WorkspaceKey:     strings.TrimSpace(stack.Workspace.Key),
		SurfaceBuilder: func(req runtimeacp.SurfaceRequest) runtimeacp.Surface {
			return stack.ACPSurface(req.Modes, req.UseFallbackModes, req.Config)
		},
		PromptRouterFactory: func(ctx context.Context, activeSession session.Session) (controlprompt.Router, error) {
			driver, err := local.NewLocalAdapterForSession(ctx, stack, activeSession, "acp", "")
			if err != nil {
				return nil, err
			}
			router := controlpromptrouter.New(controlprompt.RouterConfig{
				Service: driver,
				CommandNames: func(ctx context.Context, service control.Service) []string {
					return acpPromptCommandNames(stack)
				},
				CoreCommandAllowed: func(_ context.Context, command string) bool {
					return controlcommands.IsACPKnown(command)
				},
				DynamicCommandAllowed: func(_ context.Context, command string) bool {
					return acpAgentCommandAllowed(stack, command)
				},
			})
			return router, nil
		},
	})
}

func acpPromptCommandNames(stack *gatewayapp.Stack) []string {
	out := controlcommands.DefaultACPNames()
	if stack == nil {
		return out
	}
	return controlcommands.AppendAgentNames(out, stackACPAgentNames(stack), acpAgentNameAllowed)
}

func acpAgentCommandAllowed(stack *gatewayapp.Stack, command string) bool {
	if command == "" || stack == nil {
		return false
	}
	return controlcommands.AgentNameAllowed(stackACPAgentNames(stack), command, acpAgentNameAllowed)
}

func stackACPAgentNames(stack *gatewayapp.Stack) []string {
	if stack == nil {
		return nil
	}
	names := make([]string, 0)
	for _, agent := range stack.ListACPAgents() {
		if name := strings.TrimSpace(agent.Name); name != "" {
			names = append(names, name)
		}
	}
	return names
}

func acpAgentNameAllowed(name string) bool {
	return !agentregistry.ReservedSlashCommandName(name)
}
