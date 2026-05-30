// Package registry provides an in-memory core tool registry adapter.
package registry

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/OnslaughtSnail/caelis/core/tool"
)

type Registry struct {
	mu    sync.RWMutex
	tools map[string]tool.Tool
	order []string
}

func New(items ...tool.Tool) (*Registry, error) {
	reg := &Registry{tools: map[string]tool.Tool{}}
	for _, item := range items {
		if err := reg.Register(item); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

func (r *Registry) Register(item tool.Tool) error {
	if item == nil {
		return fmt.Errorf("tools/registry: tool is required")
	}
	name := strings.TrimSpace(item.Definition().Name)
	if name == "" {
		return fmt.Errorf("tools/registry: tool name is required")
	}
	key := strings.ToLower(name)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tools == nil {
		r.tools = map[string]tool.Tool{}
	}
	if _, exists := r.tools[key]; !exists {
		r.order = append(r.order, key)
	}
	r.tools[key] = item
	return nil
}

func (r *Registry) List(ctx context.Context) ([]tool.Tool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]tool.Tool, 0, len(r.order))
	for _, key := range r.order {
		if item := r.tools[key]; item != nil {
			out = append(out, item)
		}
	}
	return out, nil
}

func (r *Registry) Lookup(ctx context.Context, name string) (tool.Tool, bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		return nil, false, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	item, ok := r.tools[key]
	return item, ok, nil
}
