package acp

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
	runtimeacp "github.com/caelis-labs/caelis/internal/acpagentbridge"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/internal/controlprompt/appserveradapter"
	"github.com/caelis-labs/caelis/surfaces/internal/promptview"
)

// ClientsConfig constructs product ACP from focused AppServer clients only.
// Surfaces never receive Runtime, Kernel, or Stack handles through this path.
type ClientsConfig struct {
	Clients      appserver.AppServerClients
	AppName      string
	UserID       string
	WorkspaceKey string
	WorkspaceCWD string
	// AgentMessageSessionClient optionally observes product-owned child Sessions
	// after trusted Agent-message delivery. When nil, the principal-bound Session
	// client is used; Host exact-target Reconnect authorizes owners without
	// granting RoleSystemSessionRuntime to Surface tokens.
	AgentMessageSessionClient appserver.SessionClient
}

// NewFromClients builds the product ACP surface from focused clients only.
func NewFromClients(cfg ClientsConfig) (*runtimeacp.RuntimeAgent, error) {
	if err := cfg.Clients.Validate(); err != nil {
		return nil, err
	}
	clients := cfg.Clients
	systemSessionClient := cfg.AgentMessageSessionClient
	if systemSessionClient == nil {
		systemSessionClient = clients.Sessions
	}
	return runtimeacp.NewGatewayAgent(runtimeacp.GatewayAgentConfig{
		SessionClient:             clients.Sessions,
		ConfigurationClient:       clients.Configuration,
		AgentMessageSessionClient: systemSessionClient,
		AgentMessageClient:        clients.AgentMessages,
		PresentationClient:        clients.Presentation,
		TerminalClient:            clients.Terminal,
		AppName:                   firstNonEmpty(cfg.AppName, "caelis"),
		UserID:                    firstNonEmpty(cfg.UserID, "local-user"),
		WorkspaceKey:              strings.TrimSpace(cfg.WorkspaceKey),
		WorkspaceCWD:              strings.TrimSpace(cfg.WorkspaceCWD),
		TaskStreamClient:          clients.Tasks,
		SlashResultFormatter:      promptview.FormatSlashResult,
		PromptRouterFactory: func(ctx context.Context, activeSession session.Session) (controlprompt.Router, error) {
			turnSessions := clients.Sessions
			if sessionvisibility.IsSystemManagedSession(activeSession) {
				turnSessions = systemSessionClient
			}
			driverWithTypedTurns, err := appserveradapter.NewAppServerAdapter(appserveradapter.AppServerAdapterConfig{
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
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
