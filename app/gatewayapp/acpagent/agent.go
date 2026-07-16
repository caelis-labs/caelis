package acpagent

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controldelegation "github.com/caelis-labs/caelis/control/delegation"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	controlpromptrouter "github.com/caelis-labs/caelis/internal/controlpromptrouter"
	controlcommands "github.com/caelis-labs/caelis/ports/controlcommand"
	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
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
					var delegationStatus controldelegation.Status
					if delegationService, ok := service.(controldelegation.Service); ok {
						delegationStatus, _ = delegationService.DelegationStatus(ctx)
					}
					out := acpPromptCommandNames(delegationStatus)
					status, err := service.AgentStatus(ctx)
					if err != nil {
						return out
					}
					return controlagents.AppendRunNames(out, acpDirectAgentRuns(status), controldelegation.IsDirectRunProfile)
				},
				CoreCommandAllowed: func(_ context.Context, command string) bool {
					return controlcommands.IsACPKnown(command)
				},
				DynamicCommandAllowed: func(_ context.Context, command string) bool {
					return acpAgentCommandAllowed(command)
				},
			})
			return router, nil
		},
	})
}

func acpPromptCommandNames(status controldelegation.Status) []string {
	bound := map[string]struct{}{}
	for _, profile := range status.Profiles {
		if !controldelegation.IsProfileBound(profile) {
			continue
		}
		name := profile.Definition.Profile
		if name == "" {
			name = profile.Binding.Profile
		}
		bound[string(name)] = struct{}{}
	}
	out := make([]string, 0, len(controlcommands.DefaultACPNames()))
	for _, name := range controlcommands.DefaultACPNames() {
		if controldelegation.IsDirectRunProfile(name) {
			if _, ok := bound[name]; !ok {
				continue
			}
		}
		out = append(out, name)
	}
	return out
}

func acpAgentCommandAllowed(command string) bool {
	return controldelegation.IsDirectRunProfile(command)
}

func acpDirectAgentRuns(status control.AgentStatusSnapshot) []controlagents.Run {
	runs := make([]controlagents.Run, 0, len(status.Participants))
	for _, participant := range status.Participants {
		runs = append(runs, controldelegation.DirectRunFromParticipant(participant.Label, participant.Kind, participant.Role, participant.Source))
	}
	return runs
}
