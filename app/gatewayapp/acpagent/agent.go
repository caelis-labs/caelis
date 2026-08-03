package acpagent

import (
	"context"
	"strings"

	agentmessage "github.com/caelis-labs/caelis/agent-sdk/message"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/app/gatewayapp"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter"
	"github.com/caelis-labs/caelis/app/gatewayapp/controladapter/local"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/surfaces/promptview"
)

func NewFromStack(stack *gatewayapp.Stack) (*runtimeacp.RuntimeAgent, error) {
	appServer, err := local.NewAppServer(stack)
	if err != nil {
		return nil, err
	}
	clients, taskStreamClient, err := appServer.Bind(controlclient.Principal{ID: stack.UserID})
	if err != nil {
		return nil, err
	}
	systemSessionClient, err := controlclient.BindSessionClient(stack.ControlClient(), controlclient.Principal{
		ID: stack.UserID, Roles: []string{controlclient.RoleSystemSessionRuntime},
	})
	if err != nil {
		return nil, err
	}
	return runtimeacp.NewGatewayAgent(runtimeacp.GatewayAgentConfig{
		SessionClient:      clients.Sessions,
		PresentationClient: clients.Presentation,
		TerminalClient:     clients.Terminal,
		AppName:            stack.AppName,
		UserID:             stack.UserID,
		WorkspaceKey:       strings.TrimSpace(stack.Workspace.Key),
		WorkspaceCWD:       strings.TrimSpace(stack.Workspace.CWD),
		TaskStreamClient:   taskStreamClient,
		AgentMessages: func(ctx context.Context, sessionID string, req agentmessage.Request) (runtimeacp.AgentMessageDelivery, error) {
			delivery, err := stack.DeliverAgentMessage(ctx, session.SessionRef{SessionID: sessionID}, req)
			if err != nil {
				return runtimeacp.AgentMessageDelivery{}, err
			}
			out := runtimeacp.AgentMessageDelivery{Accepted: delivery.Accepted, State: delivery.State}
			if delivery.Turn != nil {
				out.TurnID = delivery.Turn.TurnID()
				out.StartedTurn = true
				out.Events = delivery.Turn.ACPEvents()
				out.Cancel = func() { delivery.Turn.Cancel() }
				out.Close = delivery.Turn.Close
			}
			return out, nil
		},
		SlashResultFormatter: promptview.FormatSlashResult,
		PromptRouterFactory: func(ctx context.Context, activeSession session.Session) (controlprompt.Router, error) {
			turnSessions := clients.Sessions
			if sessionvisibility.IsSystemManagedSession(activeSession) {
				turnSessions = systemSessionClient
			}
			driverWithTypedTurns, err := controladapter.NewAppServerAdapter(controladapter.AppServerAdapterConfig{
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
			router := controlprompt.New(controlprompt.RouterConfig{
				Service: driverWithTypedTurns,
				CommandNames: func(ctx context.Context, service controlprompt.Service) []string {
					handles, _ := clients.Participants.Handles(ctx, activeSession.SessionID)
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
