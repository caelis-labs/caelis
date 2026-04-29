package acp

import (
	"context"
	"fmt"
	"maps"
	"strings"
	"sync"

	sdkdelegation "github.com/OnslaughtSnail/caelis/sdk/delegation"
	sdkplugin "github.com/OnslaughtSnail/caelis/sdk/plugin"
)

// AgentConfig aliases the app-owned ACP agent declaration shape used by
// assembly. The runtime path should share this exact data shape between
// subagent and controller wiring.
type AgentConfig = sdkplugin.AgentConfig

type Registry struct {
	mu     sync.RWMutex
	agents map[string]AgentConfig
}

func NewRegistry(configs []AgentConfig) (*Registry, error) {
	out := &Registry{agents: map[string]AgentConfig{}}
	for _, one := range configs {
		if err := out.Register(one); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *Registry) Register(cfg AgentConfig) error {
	cfg = normalizeAgentConfig(cfg)
	if cfg.Name == "" {
		return fmt.Errorf("sdk/subagent/acp: agent name is required")
	}
	if strings.TrimSpace(cfg.Command) == "" {
		return fmt.Errorf("sdk/subagent/acp: command is required for agent %q", cfg.Name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[cfg.Name] = cfg
	return nil
}

func (r *Registry) Replace(configs []AgentConfig) error {
	next := make(map[string]AgentConfig, len(configs))
	for _, cfg := range configs {
		cfg = normalizeAgentConfig(cfg)
		if cfg.Name == "" {
			return fmt.Errorf("sdk/subagent/acp: agent name is required")
		}
		if strings.TrimSpace(cfg.Command) == "" {
			return fmt.Errorf("sdk/subagent/acp: command is required for agent %q", cfg.Name)
		}
		next[cfg.Name] = cfg
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents = next
	return nil
}

func (r *Registry) Get(_ context.Context, name string) (sdkdelegation.Agent, error) {
	cfg, err := r.lookup(name)
	if err != nil {
		return sdkdelegation.Agent{}, err
	}
	return sdkdelegation.Agent{Name: cfg.Name, Description: cfg.Description}, nil
}

func (r *Registry) List(context.Context) ([]sdkdelegation.Agent, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]sdkdelegation.Agent, 0, len(r.agents))
	for _, one := range r.agents {
		out = append(out, sdkdelegation.Agent{Name: one.Name, Description: one.Description})
	}
	return out, nil
}

func (r *Registry) Resolve(name string) (AgentConfig, error) {
	return r.lookup(name)
}

func (r *Registry) lookup(name string) (AgentConfig, error) {
	if r == nil {
		return AgentConfig{}, fmt.Errorf("sdk/subagent/acp: registry is unavailable")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = "self"
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.agents[name]
	if !ok {
		return AgentConfig{}, fmt.Errorf("sdk/subagent/acp: agent %q not found", name)
	}
	return normalizeAgentConfig(cfg), nil
}

func normalizeAgentConfig(in AgentConfig) AgentConfig {
	out := in
	out.Name = strings.TrimSpace(in.Name)
	out.Description = strings.TrimSpace(in.Description)
	out.Command = strings.TrimSpace(in.Command)
	out.WorkDir = strings.TrimSpace(in.WorkDir)
	if len(in.Args) > 0 {
		out.Args = append([]string(nil), in.Args...)
	}
	out.Env = maps.Clone(in.Env)
	return out
}
