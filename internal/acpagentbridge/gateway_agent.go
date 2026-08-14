package acpagentbridge

import (
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

type GatewayAgentConfig struct {
	SessionClient       controlclient.SessionClient
	ConfigurationClient controlclient.ConfigurationClient
	// AgentMessageSessionClient is the internal Session observer allowed to
	// follow product-owned child Sessions after trusted Agent-message delivery.
	AgentMessageSessionClient controlclient.SessionClient
	AgentMessageClient        controlclient.AgentMessageClient
	PresentationClient        controlclient.PresentationClient
	TerminalClient            controlclient.TerminalClient
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
	agentMessageTurns, err := controlclient.NewAgentMessageTurnClient(agentMessageSessions, cfg.AgentMessageClient)
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
