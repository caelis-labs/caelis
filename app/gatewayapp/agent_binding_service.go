package gatewayapp

import (
	"context"
	"fmt"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

// AgentBindingService projects every configured handle -> ModelProfile +
// effort binding, custom delegation role, and named binding snapshot. Writes
// are owned by the shared recoverable Agent command capability.
type AgentBindingService struct {
	stack *Stack
}

// AgentBindings returns the read-only Control-owned Agent binding projection.
func (s *Stack) AgentBindings() AgentBindingService {
	return AgentBindingService{stack: s}
}

// AgentBindingStatus returns every fixed and custom handle, standard
// ModelProfile, and binding-set status.
func (s AgentBindingService) AgentBindingStatus(ctx context.Context) (agentbinding.Status, error) {
	if s.stack == nil || s.stack.composition.store == nil {
		return agentbinding.Status{}, fmt.Errorf("gatewayapp: Agent binding configuration is unavailable")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return agentbinding.Status{}, err
		}
	}
	doc, err := s.stack.composition.store.LoadContext(contextOrBackground(ctx))
	if err != nil {
		return agentbinding.Status{}, err
	}
	return agentBindingStatusFromConfig(doc.AgentBindings, doc.ModelProfiles), nil
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
