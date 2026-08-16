package acpagentbridge

import (
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

type GatewayAgentConfig struct {
	SessionClient       appserver.SessionClient
	ConfigurationClient appserver.ConfigurationClient
	// AgentMessageSessionClient is the internal Session observer allowed to
	// follow product-owned child Sessions after trusted Agent-message delivery.
	AgentMessageSessionClient appserver.SessionClient
	AgentMessageClient        appserver.AgentMessageClient
	PresentationClient        appserver.PresentationClient
	TerminalClient            appserver.TerminalClient
	AppName                   string
	UserID                    string
	WorkspaceKey              string
	WorkspaceCWD              string
	PromptRouterFactory       PromptRouterFactory
	SlashResultFormatter      SlashResultFormatter
	TaskStreamClient          taskstream.Client
}

// NewGatewayAgent constructs the product ACP surface exclusively from typed
// AppServer clients. Direct Runtime, Session service, and surface-provider
// injection remain available only to the lower-level bridge conformance API.
func NewGatewayAgent(cfg GatewayAgentConfig) (*RuntimeAgent, error) {
	agentMessageSessions := cfg.AgentMessageSessionClient
	if agentMessageSessions == nil {
		agentMessageSessions = cfg.SessionClient
	}
	agentMessageTurns, err := appserver.NewAgentMessageTurnClient(agentMessageSessions, cfg.AgentMessageClient)
	if err != nil {
		return nil, err
	}
	return New(Config{
		SessionClient:        cfg.SessionClient,
		ConfigurationClient:  cfg.ConfigurationClient,
		AgentMessageTurns:    agentMessageTurns,
		PresentationClient:   cfg.PresentationClient,
		TerminalClient:       cfg.TerminalClient,
		PromptRouterFactory:  cfg.PromptRouterFactory,
		SlashResultFormatter: cfg.SlashResultFormatter,
		TaskStreamClient:     cfg.TaskStreamClient,
		AppName:              cfg.AppName,
		UserID:               cfg.UserID,
		WorkspaceKey:         cfg.WorkspaceKey,
		WorkspaceCWD:         cfg.WorkspaceCWD,
		AgentInfo:            &acp.Implementation{Name: cfg.AppName, Version: version.String()},
	})
}
