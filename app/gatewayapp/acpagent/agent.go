package acpagent

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
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
	sessionClient, err := controlclient.BindSessionClient(
		stack.ControlClient(),
		controlclient.Principal{ID: deps.UserID},
	)
	if err != nil {
		return nil, err
	}
	participantClient, err := controlclient.BindParticipantClient(
		stack.ControlParticipants(),
		controlclient.Principal{ID: deps.UserID},
	)
	if err != nil {
		return nil, err
	}
	statusService, err := local.NewStatusService(stack)
	if err != nil {
		return nil, err
	}
	statusClient, err := controlclient.BindStatusClient(
		statusService,
		controlclient.Principal{ID: deps.UserID},
	)
	if err != nil {
		return nil, err
	}
	configurationService, err := local.NewConfigurationService(stack, statusService)
	if err != nil {
		return nil, err
	}
	configurationClient, err := controlclient.BindConfigurationClient(
		configurationService,
		controlclient.Principal{ID: deps.UserID},
	)
	if err != nil {
		return nil, err
	}
	agentService, err := local.NewAgentService(stack)
	if err != nil {
		return nil, err
	}
	agentClient, err := controlclient.BindAgentClient(
		agentService,
		controlclient.Principal{ID: deps.UserID},
	)
	if err != nil {
		return nil, err
	}
	completionService, err := local.NewCompletionService(stack)
	if err != nil {
		return nil, err
	}
	completionClient, err := controlclient.BindCompletionClient(
		completionService,
		controlclient.Principal{ID: deps.UserID},
	)
	if err != nil {
		return nil, err
	}
	pluginService, err := local.NewPluginService(stack)
	if err != nil {
		return nil, err
	}
	pluginClient, err := controlclient.BindPluginClient(
		pluginService,
		controlclient.Principal{ID: deps.UserID},
	)
	if err != nil {
		return nil, err
	}
	taskStreamClient, err := taskstream.BindClient(
		deps.TaskStreams,
		taskstream.Principal{ID: deps.UserID},
	)
	if err != nil {
		return nil, err
	}
	return runtimeacp.NewGatewayAgent(runtimeacp.GatewayAgentConfig{
		Runtime:              deps.Runtime,
		Sessions:             deps.Sessions,
		SessionClient:        sessionClient,
		Assembly:             deps.Assembly,
		AppName:              deps.AppName,
		UserID:               deps.UserID,
		WorkspaceKey:         strings.TrimSpace(stack.Workspace.Key),
		WorkspaceCWD:         strings.TrimSpace(stack.Workspace.CWD),
		TaskStreamClient:     taskStreamClient,
		SlashResultFormatter: promptview.FormatSlashResult,
		SurfaceBuilder: func(req runtimeacp.SurfaceRequest) runtimeacp.Surface {
			return stack.ACPSurface(req.Modes, req.UseFallbackModes, req.Config)
		},
		PromptRouterFactory: func(ctx context.Context, activeSession session.Session) (controlprompt.Router, error) {
			driverWithTypedTurns, err := controladapter.NewAppServerAdapter(controladapter.AppServerAdapterConfig{
				SessionID:     strings.TrimSpace(activeSession.SessionID),
				WorkspaceKey:  strings.TrimSpace(activeSession.WorkspaceKey),
				WorkspaceDir:  strings.TrimSpace(activeSession.CWD),
				Surface:       "acp",
				Sessions:      sessionClient,
				Participants:  participantClient,
				Status:        statusClient,
				Configuration: configurationClient,
				Agents:        agentClient,
				Completion:    completionClient,
				Plugins:       pluginClient,
			})
			if err != nil {
				return nil, err
			}
			router := controlprompt.New(controlprompt.RouterConfig{
				Service: driverWithTypedTurns,
				CommandNames: func(ctx context.Context, service controlprompt.Service) []string {
					handles, _ := participantClient.Handles(ctx, activeSession.SessionID)
					out := acpPromptCommandNamesFromHandles(handles)
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

func acpPromptCommandNamesFromHandles(handles []string) []string {
	out := agentbinding.ProjectBoundDirectNames(controlprompt.DefaultACPNames(), agentbinding.Status{})
	seen := make(map[string]struct{}, len(out)+len(handles))
	for _, name := range out {
		seen[strings.ToLower(strings.TrimSpace(name))] = struct{}{}
	}
	for _, raw := range handles {
		name := controlagents.NormalizeName(raw)
		if !controlagents.IsName(name) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		out = append(out, name)
		seen[name] = struct{}{}
	}
	return out
}

func acpDirectAgentRuns(status controlprompt.AgentStatusSnapshot) []controlagents.Run {
	runs := make([]controlagents.Run, 0, len(status.Participants))
	for _, participant := range status.Participants {
		runs = append(runs, controlagents.DirectRunFromParticipant(participant.Label, participant.Kind, participant.Role, participant.Source))
	}
	return runs
}
