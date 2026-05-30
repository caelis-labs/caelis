// Package plugin defines deterministic contribution contracts for Caelis
// extensions. It intentionally avoids Go's platform-limited plugin mechanism.
package plugin

import (
	"context"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/core/tool"
)

type Manifest struct {
	ID           string            `json:"id"`
	Name         string            `json:"name,omitempty"`
	Version      string            `json:"version,omitempty"`
	Description  string            `json:"description,omitempty"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Meta         map[string]string `json:"meta,omitempty"`
}

type Contribution interface {
	Manifest() Manifest
	Register(context.Context, Registry) error
}

type ModelProviderFactory func(context.Context, ModelProviderConfig) (model.Provider, error)

type ModelProviderConfig struct {
	ID              string         `json:"id,omitempty"`
	Profile         string         `json:"profile,omitempty"`
	Provider        string         `json:"provider,omitempty"`
	Endpoint        string         `json:"endpoint,omitempty"`
	Model           string         `json:"model,omitempty"`
	Token           string         `json:"token,omitempty"`
	TokenEnv        string         `json:"token_env,omitempty"`
	AuthType        string         `json:"auth_type,omitempty"`
	HeaderKey       string         `json:"header_key,omitempty"`
	MaxOutputTokens int            `json:"max_output_tokens,omitempty"`
	Meta            map[string]any `json:"meta,omitempty"`
}

type StoreFactory func(context.Context, StoreConfig) (session.Store, error)

type StoreConfig struct {
	Backend string         `json:"backend,omitempty"`
	URI     string         `json:"uri,omitempty"`
	Meta    map[string]any `json:"meta,omitempty"`
}

type ToolFactory func(context.Context, ToolConfig) (tool.Tool, error)

type ToolConfig struct {
	Name      string               `json:"name,omitempty"`
	Sandbox   sandbox.Runtime      `json:"-"`
	ACPAgents []ACPAgentDescriptor `json:"acp_agents,omitempty"`
	Meta      map[string]any       `json:"meta,omitempty"`
}

type ACPAgentDescriptor struct {
	Name        string            `json:"name,omitempty"`
	Description string            `json:"description,omitempty"`
	Command     string            `json:"command,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
	WorkDir     string            `json:"work_dir,omitempty"`
	Roles       []string          `json:"roles,omitempty"`
}

type FactoryAlias struct {
	Name        string            `json:"name,omitempty"`
	Uses        string            `json:"uses,omitempty"`
	Description string            `json:"description,omitempty"`
	Meta        map[string]string `json:"meta,omitempty"`
}

type PromptFragment struct {
	ID       string   `json:"id,omitempty"`
	Scope    string   `json:"scope,omitempty"`
	Priority int      `json:"priority,omitempty"`
	Text     string   `json:"text,omitempty"`
	Paths    []string `json:"paths,omitempty"`
}

type SkillDescriptor struct {
	Name        string   `json:"name,omitempty"`
	Description string   `json:"description,omitempty"`
	Paths       []string `json:"paths,omitempty"`
}

type RendererHint struct {
	EventType string         `json:"event_type,omitempty"`
	ToolName  string         `json:"tool_name,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	Meta      map[string]any `json:"meta,omitempty"`
}

type Registry interface {
	RegisterModelProvider(string, ModelProviderFactory) error
	RegisterStore(string, StoreFactory) error
	RegisterSandboxBackend(string, sandbox.BackendFactory) error
	RegisterTool(string, ToolFactory) error
	RegisterACPAgent(ACPAgentDescriptor) error
	RegisterPrompt(PromptFragment) error
	RegisterSkill(SkillDescriptor) error
	RegisterRendererHint(RendererHint) error
}
