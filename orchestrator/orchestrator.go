package orchestrator

import (
	"sync"

	"github.com/OnslaughtSnail/caelis/acp/client"
	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/policy"
	"github.com/OnslaughtSnail/caelis/runner"
	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/tool"
	"github.com/OnslaughtSnail/caelis/trace"
)

// Config holds dependencies for creating an Orchestrator.
type Config struct {
	// Sessions is the session store for creating/managing child sessions.
	Sessions session.Service

	// ModelRegistry resolves model refs to LLM instances.
	ModelRegistry model.Registry

	// ToolRegistry resolves tool names to Tool instances.
	ToolRegistry tool.Registry

	// Sandbox creates sandboxed execution environments.
	Sandbox sandbox.Factory

	// Policy evaluates tool calls against authorization rules.
	Policy policy.Engine

	// Approver handles approval requests for tool calls.
	Approver agent.ApprovalRequester

	// Hooks are invocation/tool lifecycle observers.
	Hooks []agent.Hook

	// Tracer records distributed trace spans.
	Tracer trace.Tracer

	// Compactor compresses context when approaching token limits.
	Compactor runner.Compactor

	// TaskStore persists task snapshots for async operations.
	TaskStore runner.TaskStore

	// SystemPrompt is assembled into the system message for child invocations.
	SystemPrompt string

	// ACPClientFactory creates ACP clients for external agents.
	// If nil, external ACP agents are not supported.
	ACPClientFactory client.ACPClientFactory

	// AgentRegistry resolves agent names to AgentConfig.
	// If nil, only internal agents from parent SubAgents() are available.
	AgentRegistry Registry
}

// Orchestrator owns multi-agent orchestration: SPAWN delegation, ACP child
// lifecycle, context visibility, permission bridging, and stream merge.
type Orchestrator struct {
	cfg Config

	mu      sync.Mutex
	children map[string]*ChildHandle // keyed by task ID
}

// New creates a new Orchestrator.
func New(cfg Config) (*Orchestrator, error) {
	if cfg.Sessions == nil {
		return nil, &ConfigError{Field: "Sessions", Message: "session service is required"}
	}
	return &Orchestrator{
		cfg:      cfg,
		children: make(map[string]*ChildHandle),
	}, nil
}

// ConfigError reports a missing or invalid configuration field.
type ConfigError struct {
	Field   string
	Message string
}

func (e *ConfigError) Error() string {
	return "orchestrator config: " + e.Field + ": " + e.Message
}
