package gatewayapp

import (
	"context"
	"errors"
	"fmt"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// AgentBindingService owns every fixed handle -> ModelProfile + effort binding.
type AgentBindingService struct {
	stack *Stack
}

// AgentBindings returns the Control-owned fixed-handle configuration service.
func (s *Stack) AgentBindings() AgentBindingService {
	return AgentBindingService{stack: s}
}

// AgentBindingStatus returns every fixed handle and standard ModelProfile.
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

// BindAgentBinding persists one fixed handle, ModelProfile, and canonical
// effort. Existing prepared work keeps its previously sealed placement.
func (s AgentBindingService) BindAgentBinding(ctx context.Context, binding agentbinding.Binding) (agentbinding.Status, error) {
	return s.mutate(ctx, "bind Agent handle", func(doc AppConfig) (agentbinding.Configuration, error) {
		return agentbinding.Bind(doc.AgentBindings, binding, doc.ModelProfiles)
	})
}

// ResetAgentBinding removes one explicit handle binding. Delegation handles
// become unavailable; system handles return to the provider-backed default.
func (s AgentBindingService) ResetAgentBinding(ctx context.Context, handle agentbinding.Handle) (agentbinding.Status, error) {
	return s.mutate(ctx, "reset Agent handle", func(doc AppConfig) (agentbinding.Configuration, error) {
		return agentbinding.Reset(doc.AgentBindings, handle)
	})
}

func (s AgentBindingService) mutate(
	ctx context.Context,
	action string,
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
	if err := s.stack.rejectReconfigureWhileActive(action); err != nil {
		return agentbinding.Status{}, err
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
	for _, definition := range agentbinding.Definitions() {
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
	return status
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var _ agentbinding.Service = AgentBindingService{}
