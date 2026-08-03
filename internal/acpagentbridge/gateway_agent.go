package acpagentbridge

import (
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

type GatewayAgentConfig struct {
	SessionClient        controlclient.SessionClient
	PresentationClient   controlclient.PresentationClient
	TerminalClient       controlclient.TerminalClient
	AppName              string
	UserID               string
	WorkspaceKey         string
	WorkspaceCWD         string
	PromptRouterFactory  PromptRouterFactory
	SlashResultFormatter SlashResultFormatter
	TaskStreamClient     taskstream.Client
	AgentMessages        AgentMessageHandler
}

// NewGatewayAgent constructs the product ACP surface exclusively from typed
// AppServer clients. Direct Runtime, Session service, and surface-provider
// injection remain available only to the lower-level bridge conformance API.
func NewGatewayAgent(cfg GatewayAgentConfig) (*RuntimeAgent, error) {
	return New(Config{
		SessionClient:        cfg.SessionClient,
		PresentationClient:   cfg.PresentationClient,
		TerminalClient:       cfg.TerminalClient,
		PromptRouterFactory:  cfg.PromptRouterFactory,
		SlashResultFormatter: cfg.SlashResultFormatter,
		TaskStreamClient:     cfg.TaskStreamClient,
		AgentMessages:        cfg.AgentMessages,
		AppName:              cfg.AppName,
		UserID:               cfg.UserID,
		WorkspaceKey:         cfg.WorkspaceKey,
		WorkspaceCWD:         cfg.WorkspaceCWD,
		AgentInfo:            &acp.Implementation{Name: cfg.AppName, Version: version.String()},
	})
}
