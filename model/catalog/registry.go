package catalog

import (
	"context"
	"fmt"
	"sync"

	"github.com/OnslaughtSnail/caelis/model"
)

// Registry implements model.Registry with a static catalog of models.
type Registry struct {
	mu      sync.RWMutex
	models  map[string]model.ModelInfo
	factory func(model.Ref) (model.LLM, error)
}

// Config holds configuration for the model catalog.
type Config struct {
	Models  []model.ModelInfo
	Factory func(model.Ref) (model.LLM, error)
}

// New creates a new catalog registry.
func New(cfg Config) *Registry {
	r := &Registry{
		models:  make(map[string]model.ModelInfo),
		factory: cfg.Factory,
	}
	for _, m := range cfg.Models {
		r.models[m.ModelID] = m
	}
	return r
}

// Resolve returns an LLM and its info for the given reference.
func (r *Registry) Resolve(_ context.Context, ref model.Ref) (model.LLM, model.ModelInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	id := ref.ModelID
	if id == "" {
		// Try alias lookup.
		for _, info := range r.models {
			for _, alias := range info.Aliases {
				if alias == ref.Alias {
					id = info.ModelID
					break
				}
			}
			if id != "" {
				break
			}
		}
	}
	if id == "" {
		return nil, model.ModelInfo{}, fmt.Errorf("model not found: %v", ref)
	}

	info, ok := r.models[id]
	if !ok {
		return nil, model.ModelInfo{}, fmt.Errorf("model not found: %s", id)
	}

	if r.factory == nil {
		return nil, info, nil
	}
	llm, err := r.factory(model.Ref{ModelID: id})
	if err != nil {
		return nil, model.ModelInfo{}, fmt.Errorf("creating model %s: %w", id, err)
	}
	return llm, info, nil
}

// List returns all known models.
func (r *Registry) List(_ context.Context) ([]model.ModelInfo, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]model.ModelInfo, 0, len(r.models))
	for _, m := range r.models {
		out = append(out, m)
	}
	return out, nil
}

// Register adds or updates a model in the catalog.
func (r *Registry) Register(info model.ModelInfo) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.models[info.ModelID] = info
}

// Compile-time interface check.
var _ model.Registry = (*Registry)(nil)
