package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/OnslaughtSnail/caelis/ports/tool"
)

// Memory is one in-memory tool registry implementation.
type Memory struct {
	tools map[string]tool.Tool
	order []string
}

// NewMemory returns one in-memory registry from the provided tools.
func NewMemory(tools ...tool.Tool) (*Memory, error) {
	r := &Memory{
		tools: map[string]tool.Tool{},
	}
	if err := r.Register(tools...); err != nil {
		return nil, err
	}
	return r, nil
}

// Register adds tools to the registry, rejecting duplicates.
func (r *Memory) Register(tools ...tool.Tool) error {
	for _, item := range tools {
		if item == nil {
			continue
		}
		def := item.Definition()
		name := strings.TrimSpace(strings.ToUpper(def.Name))
		if name == "" {
			return fmt.Errorf("tool/registry: tool name is required")
		}
		if _, ok := r.tools[name]; ok {
			return fmt.Errorf("tool/registry: duplicate tool %q", name)
		}
		r.tools[name] = item
		r.order = append(r.order, name)
	}
	return nil
}

func (r *Memory) List(context.Context) ([]tool.Tool, error) {
	if r == nil || len(r.order) == 0 {
		return nil, nil
	}
	out := make([]tool.Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.tools[name])
	}
	return out, nil
}

func (r *Memory) Lookup(_ context.Context, name string) (tool.Tool, bool, error) {
	if r == nil {
		return nil, false, nil
	}
	item, ok := r.tools[strings.TrimSpace(strings.ToUpper(name))]
	return item, ok, nil
}

var _ tool.Registry = (*Memory)(nil)
