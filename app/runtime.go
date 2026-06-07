package app

import (
	"context"
	"fmt"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/gateway"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/model/catalog"
	"github.com/OnslaughtSnail/caelis/policy"
	"github.com/OnslaughtSnail/caelis/policy/presets"
	"github.com/OnslaughtSnail/caelis/runner"
	"github.com/OnslaughtSnail/caelis/sandbox"
	"github.com/OnslaughtSnail/caelis/sandbox/host"
	"github.com/OnslaughtSnail/caelis/session"
	"github.com/OnslaughtSnail/caelis/skill"
	"github.com/OnslaughtSnail/caelis/tool"
)

// Runtime holds the assembled runtime dependencies.
type Runtime struct {
	Gateway gateway.Service
	Runner  *runner.Runner
	Config  RuntimeConfig
}

// RuntimeConfig holds the configuration for assembling a Runtime.
type RuntimeConfig struct {
	AppName        string
	Agent          agent.Agent
	SessionStore   session.Service
	ModelProfiles  []model.ModelInfo
	ModelFactory   func(model.Ref) (model.LLM, error)
	ToolList       []tool.Tool
	SandboxBackend sandbox.Backend
	PolicyEngine   policy.Engine
	SkillRegistry  skill.Registry
	Approver       agent.ApprovalRequester
	SystemPrompt   string
}

// NewRuntime assembles a Runtime from the given config.
// It creates default implementations for any nil dependencies.
func NewRuntime(cfg RuntimeConfig) (*Runtime, error) {
	if cfg.Agent == nil {
		return nil, fmt.Errorf("app: Agent is required")
	}

	// Session store: default to in-memory.
	sessionStore := cfg.SessionStore
	if sessionStore == nil {
		sessionStore = session.InMemoryService()
	}

	// Model registry: create from profiles. Empty catalogs are valid for
	// agents that do not require model resolution.
	modelReg := catalog.New(catalog.Config{
		Models:  cfg.ModelProfiles,
		Factory: cfg.ModelFactory,
	})

	// Tool registry: create from tool list.
	toolReg := tool.NewMemoryRegistry()
	if len(cfg.ToolList) > 0 {
		toolReg.RegisterAll(cfg.ToolList)
	}

	// Sandbox: default to host backend.
	var sandboxFact sandbox.Factory
	if cfg.SandboxBackend != nil {
		sandboxFact = &singleBackendFactory{backend: cfg.SandboxBackend}
	} else {
		sandboxFact = &singleBackendFactory{backend: host.New()}
	}

	// Policy: default to workspace-write.
	policyEngine := cfg.PolicyEngine
	if policyEngine == nil {
		policyEngine = &presets.WorkspaceWrite{}
	}

	// Runner.
	r, err := runner.New(runner.Config{
		Agent:         cfg.Agent,
		Sessions:      sessionStore,
		ModelRegistry: modelReg,
		ToolRegistry:  toolReg,
		Sandbox:       sandboxFact,
		Policy:        policyEngine,
		Skills:        cfg.SkillRegistry,
		Approver:      cfg.Approver,
		SystemPrompt:  cfg.SystemPrompt,
	})
	if err != nil {
		return nil, fmt.Errorf("app: create runner: %w", err)
	}

	return &Runtime{
		Gateway: newRuntimeGateway(sessionStore, r),
		Runner:  r,
		Config:  cfg,
	}, nil
}

// singleBackendFactory wraps a single backend as a sandbox.Factory.
type singleBackendFactory struct {
	backend sandbox.Backend
}

func (f *singleBackendFactory) Create(_ context.Context, _ sandbox.Config) (sandbox.Backend, error) {
	return f.backend, nil
}

func (f *singleBackendFactory) Available(_ context.Context) ([]sandbox.Descriptor, error) {
	desc, _ := f.backend.Describe(context.Background())
	return []sandbox.Descriptor{desc}, nil
}
