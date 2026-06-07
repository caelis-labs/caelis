package tool

import (
	"context"
	"sync"
)

// MemoryRegistry is a concrete tool.Registry backed by a map.
// Tools are registered by name and looked up by name.
type MemoryRegistry struct {
	mu    sync.RWMutex
	tools map[string]Tool
}

// NewMemoryRegistry creates an empty memory tool registry.
func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{tools: make(map[string]Tool)}
}

// Register adds a tool to the registry. If a tool with the same name
// already exists, it is replaced.
func (r *MemoryRegistry) Register(t Tool) {
	if t == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[t.Definition().Name] = t
}

// RegisterAll adds multiple tools to the registry.
func (r *MemoryRegistry) RegisterAll(tools []Tool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range tools {
		if t == nil {
			continue
		}
		r.tools[t.Definition().Name] = t
	}
}

// Lookup returns a tool by name.
func (r *MemoryRegistry) Lookup(_ context.Context, name string) (Tool, bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tools[name]
	return t, ok, nil
}

// List returns all registered tools.
func (r *MemoryRegistry) List(_ context.Context) ([]Tool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t)
	}
	return out, nil
}

// Count returns the number of registered tools.
func (r *MemoryRegistry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.tools)
}

// Names returns all registered tool names.
func (r *MemoryRegistry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	return names
}

// Compile-time interface check.
var _ Registry = (*MemoryRegistry)(nil)
