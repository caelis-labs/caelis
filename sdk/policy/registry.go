package policy

import (
	"context"
	"fmt"
	"strings"
)

type MemoryRegistry struct {
	modes map[string]Mode
}

func NewMemory(modes ...Mode) (*MemoryRegistry, error) {
	reg := &MemoryRegistry{modes: map[string]Mode{}}
	for _, one := range modes {
		if err := reg.Register(one); err != nil {
			return nil, err
		}
	}
	return reg, nil
}

func (r *MemoryRegistry) Register(mode Mode) error {
	if r == nil {
		return fmt.Errorf("sdk/policy: registry is nil")
	}
	if mode == nil {
		return fmt.Errorf("sdk/policy: mode is required")
	}
	name := strings.TrimSpace(strings.ToLower(mode.Name()))
	if name == "" {
		return fmt.Errorf("sdk/policy: mode name is required")
	}
	if r.modes == nil {
		r.modes = map[string]Mode{}
	}
	r.modes[name] = mode
	return nil
}

func (r *MemoryRegistry) Lookup(_ context.Context, name string) (Mode, bool, error) {
	if r == nil {
		return nil, false, nil
	}
	mode, ok := r.modes[strings.TrimSpace(strings.ToLower(name))]
	return mode, ok, nil
}
