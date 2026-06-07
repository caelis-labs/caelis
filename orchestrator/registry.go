package orchestrator

import (
	"context"
	"fmt"
	"sync"

	"github.com/OnslaughtSnail/caelis/agent"
)

// Registry resolves agent names to their configuration.
type Registry interface {
	// Get returns the agent config for the given name.
	// Returns nil if the agent is not found.
	Get(ctx context.Context, name string) (*AgentConfig, error)

	// List returns all registered agent configs.
	List(ctx context.Context) ([]AgentConfig, error)
}

// AgentConfig describes a spawnable agent.
type AgentConfig struct {
	// Name is the unique agent identifier.
	Name string

	// Description is a human-readable description.
	Description string

	// Internal is a pre-registered internal agent.Agent instance.
	// When set, the orchestrator uses the ACP loopback path.
	Internal agent.Agent

	// External configures an external ACP agent process.
	// When set (Command != ""), the orchestrator uses the ACP client path.
	External ExternalAgentConfig
}

// ExternalAgentConfig describes an external ACP agent process.
type ExternalAgentConfig struct {
	// Command is the executable to launch.
	Command string

	// Args are command-line arguments.
	Args []string

	// Env is the process environment (key=value).
	Env map[string]string

	// WorkDir is the working directory.
	WorkDir string
}

// MemoryRegistry is an in-memory agent registry.
type MemoryRegistry struct {
	mu      sync.RWMutex
	agents  map[string]AgentConfig
	order   []string
}

// NewMemoryRegistry creates a new in-memory agent registry.
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{
		agents: make(map[string]AgentConfig),
	}
}

// Register adds or updates an agent config.
func (r *MemoryRegistry) Register(cfg AgentConfig) error {
	name := cfg.Name
	if name == "" {
		return fmt.Errorf("orchestrator: agent name is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.agents[name]; !exists {
		r.order = append(r.order, name)
	}
	r.agents[name] = cfg
	return nil
}

// Get returns the agent config for the given name.
func (r *MemoryRegistry) Get(_ context.Context, name string) (*AgentConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.agents[name]
	if !ok {
		return nil, nil
	}
	return &cfg, nil
}

// List returns all registered agent configs.
func (r *MemoryRegistry) List(_ context.Context) ([]AgentConfig, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]AgentConfig, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.agents[name])
	}
	return out, nil
}

// resolveAgent finds an agent by name from the registry or from the parent's SubAgents.
// Returns the internal agent.Agent if found, or nil if it's an external agent.
func (o *Orchestrator) resolveAgent(ctx context.Context, name string, parent agent.Agent) (agent.Agent, *AgentConfig, error) {
	// Check the orchestrator registry first.
	if o.cfg.AgentRegistry != nil {
		cfg, err := o.cfg.AgentRegistry.Get(ctx, name)
		if err != nil {
			return nil, nil, fmt.Errorf("orchestrator: registry lookup %q: %w", name, err)
		}
		if cfg != nil {
			return cfg.Internal, cfg, nil
		}
	}

	// Fall back to parent's SubAgents.
	if parent != nil {
		if child := parent.FindAgent(name); child != nil {
			return child, &AgentConfig{
				Name:     name,
				Internal: child,
			}, nil
		}
		// If no name specified, use the single sub-agent.
		if name == "" {
			subs := parent.SubAgents()
			if len(subs) == 1 {
				return subs[0], &AgentConfig{
					Name:     subs[0].Name(),
					Internal: subs[0],
				}, nil
			}
		}
	}

	return nil, nil, fmt.Errorf("orchestrator: agent %q not found", name)
}
