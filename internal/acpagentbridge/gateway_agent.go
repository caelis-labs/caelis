package acpagentbridge

import (
	"strings"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/version"
	acp "github.com/caelis-labs/caelis/protocol/acp/schema"
)

type GatewayAgentConfig struct {
	Clients             appserver.AppServerClients
	SystemSessionClient appserver.SessionClient
	AppName             string
	UserID              string
	WorkspaceKey        string
	WorkspaceCWD        string
	// ManagedSessionHistoryToken is Host assembly input for one short-lived,
	// read-only managed child history bridge.
	ManagedSessionHistoryToken string
	SlashResultFormatter       SlashResultFormatter
}

// NewGatewayAgent constructs the product ACP surface exclusively from typed
// AppServer clients. Direct Runtime, Session service, and surface-provider
// injection remain available only to the lower-level bridge conformance API.
func NewGatewayAgent(cfg GatewayAgentConfig) (*RuntimeAgent, error) {
	if err := cfg.Clients.Validate(); err != nil {
		return nil, err
	}
	clients := cfg.Clients
	systemSessionClient := cfg.SystemSessionClient
	if systemSessionClient == nil {
		systemSessionClient = clients.Sessions
	}
	return New(Config{
		SessionClient:              clients.Sessions,
		ConfigurationClient:        clients.Configuration,
		PresentationClient:         clients.Presentation,
		PromptRouterFactory:        newGatewayPromptRouterFactory(clients, systemSessionClient),
		SlashResultFormatter:       cfg.SlashResultFormatter,
		TaskStreamClient:           clients.Tasks,
		AppName:                    firstNonEmptyGatewayValue(cfg.AppName, "caelis"),
		UserID:                     firstNonEmptyGatewayValue(cfg.UserID, "local-user"),
		WorkspaceKey:               cfg.WorkspaceKey,
		WorkspaceCWD:               cfg.WorkspaceCWD,
		ManagedSessionHistoryToken: cfg.ManagedSessionHistoryToken,
		AgentInfo:                  &acp.Implementation{Name: cfg.AppName, Version: version.String()},
	})
}

func firstNonEmptyGatewayValue(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
