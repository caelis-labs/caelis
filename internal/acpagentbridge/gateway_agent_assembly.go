package acpagentbridge

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
)

func newGatewayPromptRouterFactory(clients appserver.AppServerClients, systemSessionClient appserver.SessionClient) PromptRouterFactory {
	return func(ctx context.Context, activeSession session.Session) (controlprompt.Router, error) {
		turnSessions := clients.Sessions
		if sessionvisibility.IsSystemManagedSession(activeSession) {
			turnSessions = systemSessionClient
		}
		driver, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
			SessionID:     strings.TrimSpace(activeSession.SessionID),
			WorkspaceKey:  strings.TrimSpace(activeSession.WorkspaceKey),
			WorkspaceDir:  strings.TrimSpace(activeSession.CWD),
			Surface:       "acp",
			Sessions:      turnSessions,
			Participants:  clients.Participants,
			Status:        clients.Status,
			Configuration: clients.Configuration,
			Agents:        clients.Agents,
			Completion:    clients.Completion,
			Plugins:       clients.Plugins,
		})
		if err != nil {
			return nil, err
		}
		return controlprompt.New(controlprompt.RouterConfig{
			Service: driver,
			CommandNames: func(ctx context.Context, service controlprompt.RouterService) []string {
				handles, _ := clients.Participants.Handles(ctx, activeSession.SessionID)
				out := gatewayCommandNamesForSession(handles, activeSession)
				status, err := service.AgentStatus(ctx)
				if err != nil {
					return out
				}
				return controlagents.AppendRunNames(out, gatewayDirectAgentRuns(status), nil)
			},
			CoreCommandAllowed: func(_ context.Context, command string) bool {
				return controlprompt.IsACPKnown(command)
			},
		}), nil
	}
}

func gatewayCommandNamesForSession(handles []string, activeSession session.Session) []string {
	out := gatewayCommandNamesFromHandles(handles)
	if activeSession.Controller.Kind == session.ControllerKindACP {
		out = controlprompt.WithoutNames(out, "compact")
	}
	return out
}

func gatewayCommandNamesFromHandles(handles []string) []string {
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

func gatewayDirectAgentRuns(status controlprompt.AgentStatusSnapshot) []controlagents.Run {
	runs := make([]controlagents.Run, 0, len(status.Participants))
	for _, participant := range status.Participants {
		runs = append(runs, controlagents.DirectRunFromParticipant(participant.Label, participant.Kind, participant.Role, participant.Source))
	}
	return runs
}
