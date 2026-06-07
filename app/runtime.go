package app

import (
	"context"
	"fmt"

	"github.com/OnslaughtSnail/caelis/agent"
	"github.com/OnslaughtSnail/caelis/gateway"
	"github.com/OnslaughtSnail/caelis/model"
	"github.com/OnslaughtSnail/caelis/model/catalog"
	caelisplugin "github.com/OnslaughtSnail/caelis/plugin"
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
	AppName       string
	Agent         agent.Agent
	SessionStore  session.Service
	ModelProfiles []model.ModelInfo
	ModelFactory  func(model.Ref) (model.LLM, error)
	ToolList      []tool.Tool
	// SandboxBackends exposes an ordered set of sandbox backends to the Layer 4
	// runner. Runtime metadata can select a backend by descriptor name.
	SandboxBackends []sandbox.Backend
	// SandboxBackend is retained as the single-backend shorthand.
	SandboxBackend sandbox.Backend
	PolicyEngine   policy.Engine
	SkillRegistry  skill.Registry
	PluginRegistry caelisplugin.Registry
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
	sandboxFact, err := runtimeSandboxFactory(cfg)
	if err != nil {
		return nil, err
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
		Plugins:       cfg.PluginRegistry,
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

func runtimeSandboxFactory(cfg RuntimeConfig) (sandbox.Factory, error) {
	switch {
	case len(cfg.SandboxBackends) > 0:
		return newSandboxBackendFactory(cfg.SandboxBackends)
	case cfg.SandboxBackend != nil:
		return newSandboxBackendFactory([]sandbox.Backend{cfg.SandboxBackend})
	default:
		return newSandboxBackendFactory([]sandbox.Backend{host.New()})
	}
}

type sandboxBackendFactory struct {
	order    []string
	backends map[string]sandbox.Backend
	descs    map[string]sandbox.Descriptor
}

func newSandboxBackendFactory(backends []sandbox.Backend) (*sandboxBackendFactory, error) {
	if len(backends) == 0 {
		return nil, fmt.Errorf("app: no sandbox backends configured")
	}
	f := &sandboxBackendFactory{
		backends: make(map[string]sandbox.Backend, len(backends)),
		descs:    make(map[string]sandbox.Descriptor, len(backends)),
	}
	for i, backend := range backends {
		if backend == nil {
			return nil, fmt.Errorf("app: sandbox backend %d is nil", i)
		}
		desc, err := backend.Describe(context.Background())
		if err != nil {
			return nil, fmt.Errorf("app: describe sandbox backend %d: %w", i, err)
		}
		name := desc.Name
		if name == "" {
			name = backend.Name()
			desc.Name = name
		}
		if name == "" {
			return nil, fmt.Errorf("app: sandbox backend %d has empty name", i)
		}
		if _, exists := f.backends[name]; exists {
			return nil, fmt.Errorf("app: duplicate sandbox backend %q", name)
		}
		f.order = append(f.order, name)
		f.backends[name] = backend
		f.descs[name] = desc
	}
	return f, nil
}

func (f *sandboxBackendFactory) Create(_ context.Context, cfg sandbox.Config) (sandbox.Backend, error) {
	name := cfg.BackendName
	if name == "" && len(f.order) > 0 {
		name = f.order[0]
	}
	backend, ok := f.backends[name]
	if !ok {
		return nil, fmt.Errorf("sandbox backend %q is not configured", cfg.BackendName)
	}
	return backend, nil
}

func (f *sandboxBackendFactory) Available(context.Context) ([]sandbox.Descriptor, error) {
	descs := make([]sandbox.Descriptor, 0, len(f.order))
	for _, name := range f.order {
		descs = append(descs, f.descs[name])
	}
	return descs, nil
}
