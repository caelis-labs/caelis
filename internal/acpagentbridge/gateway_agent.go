package acpagentbridge

import (
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/version"
	"github.com/caelis-labs/caelis/protocol/acp"
	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

type GatewayAgentConfig struct {
	SessionClient        appserver.SessionClient
	ConfigurationClient  appserver.ConfigurationClient
	PresentationClient   appserver.PresentationClient
	TerminalClient       appserver.TerminalClient
	AppName              string
	UserID               string
	WorkspaceKey         string
	WorkspaceCWD         string
	PromptRouterFactory  PromptRouterFactory
	SlashResultFormatter SlashResultFormatter
	TaskStreamClient     taskstream.Client
}

// NewGatewayAgent constructs the product ACP surface exclusively from typed
// AppServer clients. Direct Runtime, Session service, and surface-provider
// injection remain available only to the lower-level bridge conformance API.
func NewGatewayAgent(cfg GatewayAgentConfig) (*RuntimeAgent, error) {
	return New(Config{
		SessionClient:        cfg.SessionClient,
		ConfigurationClient:  cfg.ConfigurationClient,
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
