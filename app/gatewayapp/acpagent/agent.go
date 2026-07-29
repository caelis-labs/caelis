package acpagent

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
	"github.com/caelis-labs/caelis/surfaces/promptview"
)

func NewFromStack(stack *gatewayapp.Stack) (*runtimeacp.RuntimeAgent, error) {
	deps, err := stack.ACPAgentDependencies()
	if err != nil {
		return nil, err
	}
	return runtimeacp.NewGatewayAgent(runtimeacp.GatewayAgentConfig{
		Runtime:              deps.Runtime,
		Sessions:             deps.Sessions,
		Resolver:             deps.Resolver,
		ApprovalReviewer:     deps.ApprovalReviewer,
		Assembly:             deps.Assembly,
		AppName:              deps.AppName,
		UserID:               deps.UserID,
		WorkspaceKey:         strings.TrimSpace(stack.Workspace.Key),
		TaskStreams:          deps.TaskStreams,
		TaskStreamPrincipal:  taskstream.Principal{ID: deps.UserID},
		SlashResultFormatter: promptview.FormatSlashResult,
		SurfaceBuilder: func(req runtimeacp.SurfaceRequest) runtimeacp.Surface {
			return stack.ACPSurface(req.Modes, req.UseFallbackModes, req.Config)
		},
		PromptRouterFactory: func(ctx context.Context, activeSession session.Session) (controlprompt.Router, error) {
			driver, err := local.NewLocalAdapterForSession(ctx, stack, activeSession, "acp", "")
			if err != nil {
				return nil, err
			}
			router := controlprompt.New(controlprompt.RouterConfig{
				Service: driver,
				CommandNames: func(ctx context.Context, service controlprompt.Service) []string {
					var bindingStatus agentbinding.Status
					if bindingService, ok := service.(agentbinding.Service); ok {
						bindingStatus, _ = bindingService.AgentBindingStatus(ctx)
					}
					out := acpPromptCommandNames(bindingStatus)
					status, err := service.AgentStatus(ctx)
					if err != nil {
						return out
					}
					return controlagents.AppendRunNames(out, acpDirectAgentRuns(status), nil)
				},
				CoreCommandAllowed: func(_ context.Context, command string) bool {
					return controlprompt.IsACPKnown(command)
				},
			})
			return router, nil
		},
	})
}

func acpPromptCommandNames(status agentbinding.Status) []string {
	return agentbinding.ProjectBoundDirectNames(controlprompt.DefaultACPNames(), status)
}

func acpDirectAgentRuns(status controlprompt.AgentStatusSnapshot) []controlagents.Run {
	runs := make([]controlagents.Run, 0, len(status.Participants))
	for _, participant := range status.Participants {
		runs = append(runs, controlagents.DirectRunFromParticipant(participant.Label, participant.Kind, participant.Role, participant.Source))
	}
	return runs
}
