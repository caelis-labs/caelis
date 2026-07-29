package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// AgentBindingService owns every configured handle -> ModelProfile + effort
// binding, custom delegation role, and named binding snapshot.
type AgentBindingService struct {
	stack *Stack
}

// AgentBindings returns the Control-owned Agent binding configuration service.
func (s *Stack) AgentBindings() AgentBindingService {
	return AgentBindingService{stack: s}
}

// AgentBindingStatus returns every fixed and custom handle, standard
// ModelProfile, and binding-set status.
func (s AgentBindingService) AgentBindingStatus(ctx context.Context) (agentbinding.Status, error) {
	if s.stack == nil || s.stack.store == nil {
		return agentbinding.Status{}, fmt.Errorf("gatewayapp: Agent binding configuration is unavailable")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return agentbinding.Status{}, err
		}
	}
	doc, err := s.stack.store.Load()
	if err != nil {
		return agentbinding.Status{}, err
	}
	return agentBindingStatusFromConfig(doc.AgentBindings, doc.ModelProfiles), nil
}

// BindAgentBinding persists one handle, ModelProfile, and canonical
// effort. Existing prepared work keeps its previously sealed placement.
func (s AgentBindingService) BindAgentBinding(ctx context.Context, binding agentbinding.Binding) (agentbinding.Status, error) {
	return s.mutate(ctx, "bind Agent handle", true, func(doc AppConfig) (agentbinding.Configuration, error) {
		return agentbinding.Bind(doc.AgentBindings, binding, doc.ModelProfiles)
	})
}

// ResetAgentBinding removes one explicit handle binding. Delegation handles
// become unavailable; system handles return to the provider-backed default.
func (s AgentBindingService) ResetAgentBinding(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	return s.mutate(ctx, "reset Agent handle", true, func(doc AppConfig) (agentbinding.Configuration, error) {
		return agentbinding.Reset(doc.AgentBindings, handle)
	})
}

// CreateAgentRole adds one custom delegation role with an optional initial
// binding.
func (s AgentBindingService) CreateAgentRole(
	ctx context.Context,
	role agentbinding.Role,
	initial agentbinding.Binding,
) (agentbinding.Status, error) {
	return s.mutate(ctx, "create Agent role", true, func(doc AppConfig) (agentbinding.Configuration, error) {
		return agentbinding.CreateRole(doc.AgentBindings, role, initial, doc.ModelProfiles)
	})
}

// DeleteAgentRole removes one custom role and its active or saved bindings.
func (s AgentBindingService) DeleteAgentRole(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	return s.mutate(ctx, "delete Agent role", true, func(doc AppConfig) (agentbinding.Configuration, error) {
		return agentbinding.DeleteRole(doc.AgentBindings, handle)
	})
}

// SaveAgentBindingSet creates or replaces one snapshot without changing the
// active runtime configuration.
func (s AgentBindingService) SaveAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	return s.mutate(ctx, "save Agent binding set", false, func(doc AppConfig) (agentbinding.Configuration, error) {
		return agentbinding.SaveBindingSet(doc.AgentBindings, name)
	})
}

// ApplyAgentBindingSet atomically replaces every active explicit binding.
func (s AgentBindingService) ApplyAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	return s.mutate(ctx, "apply Agent binding set", true, func(doc AppConfig) (agentbinding.Configuration, error) {
		return agentbinding.ApplyBindingSet(doc.AgentBindings, name, doc.ModelProfiles)
	})
}

// DeleteAgentBindingSet removes one saved snapshot without changing the active
// runtime configuration.
func (s AgentBindingService) DeleteAgentBindingSet(ctx context.Context, name string) (agentbinding.Status, error) {
	return s.mutate(ctx, "delete Agent binding set", false, func(doc AppConfig) (agentbinding.Configuration, error) {
		return agentbinding.DeleteBindingSet(doc.AgentBindings, name)
	})
}

func (s AgentBindingService) mutate(
	ctx context.Context,
	action string,
	refreshRuntime bool,
	update func(AppConfig) (agentbinding.Configuration, error),
) (agentbinding.Status, error) {
	if s.stack == nil || s.stack.store == nil {
		return agentbinding.Status{}, fmt.Errorf("gatewayapp: Agent binding configuration is unavailable")
	}
	ctx = contextOrBackground(ctx)
	if err := ctx.Err(); err != nil {
		return agentbinding.Status{}, err
	}
	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	s.stack.assemblyMutationMu.Lock()
	defer s.stack.assemblyMutationMu.Unlock()
	if refreshRuntime {
		if err := s.stack.rejectReconfigureWhileActive(action); err != nil {
			return agentbinding.Status{}, err
		}
	}
	doc, err := s.stack.store.Load()
	if err != nil {
		return agentbinding.Status{}, err
	}
	previous := doc
	next, err := update(doc)
	if err != nil {
		return agentbinding.Status{}, err
	}
	doc.AgentBindings = agentbinding.NormalizeConfiguration(next)
	status := agentBindingStatusFromConfig(doc.AgentBindings, doc.ModelProfiles)
	saveErr := s.stack.store.Save(doc)
	if saveErr != nil && !configstore.WriteCommitted(saveErr) {
		return agentbinding.Status{}, saveErr
	}
	if !refreshRuntime {
		return status, saveErr
	}
	if err := s.stack.refreshConfiguredAgentsFromStore(); err != nil {
		if saveErr != nil {
			return status, errors.Join(saveErr, err)
		}
		rollbackErr := s.stack.store.Save(previous)
		refreshErr := s.stack.refreshConfiguredAgentsFromStore()
		return agentbinding.Status{}, errors.Join(err, rollbackErr, refreshErr)
	}
	return status, saveErr
}

func agentBindingStatusFromConfig(
	bindings agentbinding.Configuration,
	profiles modelprofile.Configuration,
) agentbinding.Status {
	status := agentbinding.Status{}
	for _, definition := range agentbinding.CatalogFor(bindings).Definitions() {
		item := agentbinding.HandleStatus{
			Definition: definition,
			Binding:    agentbinding.Binding{Handle: definition.Handle},
		}
		if binding, ok := agentbinding.Lookup(bindings, definition.Handle); ok {
			item.Binding = binding
			item.Profile, _ = modelprofile.Lookup(profiles, binding.ProfileID)
		}
		status.Handles = append(status.Handles, item)
	}
	status.Targets = append(status.Targets, modelprofile.NormalizeConfiguration(profiles).Profiles...)
	status.Sets = agentbinding.BindingSetStatuses(bindings, profiles)
	return status
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var _ agentbinding.ConfigurationService = AgentBindingService{}
