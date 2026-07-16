package gatewayapp

import (
	"context"
	"fmt"
	"strings"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	controldelegation "github.com/caelis-labs/caelis/control/delegation"
	"github.com/caelis-labs/caelis/control/modelconfig"
)

// DelegationService owns fixed profile bindings.
type DelegationService struct {
	stack *Stack
}

// Delegation returns the Control-owned delegation configuration service.
func (s *Stack) Delegation() DelegationService {
	return DelegationService{stack: s}
}

// DelegationStatus returns fixed profiles and their effective roster targets.
func (s DelegationService) DelegationStatus(ctx context.Context) (controldelegation.Status, error) {
	if s.stack == nil || s.stack.store == nil {
		return controldelegation.Status{}, fmt.Errorf("gatewayapp: delegation configuration is unavailable")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return controldelegation.Status{}, err
		}
	}
	doc, err := s.stack.store.Load()
	if err != nil {
		return controldelegation.Status{}, err
	}
	return delegationStatusFromConfig(doc.Delegation, doc.AgentRoster, doc.Models.Configs), nil
}

// BindDelegation binds one fixed profile and refreshes future runtime
// placement. Existing child tasks keep the placement captured at Spawn time.
func (s DelegationService) BindDelegation(ctx context.Context, req controldelegation.BindRequest) (controldelegation.Status, error) {
	if s.stack == nil || s.stack.store == nil {
		return controldelegation.Status{}, fmt.Errorf("gatewayapp: delegation configuration is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return controldelegation.Status{}, err
	}
	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	s.stack.agentRosterMu.Lock()
	defer s.stack.agentRosterMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("bind subagent profile"); err != nil {
		return controldelegation.Status{}, err
	}
	doc, err := s.stack.store.Load()
	if err != nil {
		return controldelegation.Status{}, err
	}
	next, err := controldelegation.BindAgent(
		doc.Delegation,
		req.Profile,
		req.AgentID,
		req.ReasoningEffort,
		doc.AgentRoster,
		doc.Models.Configs,
	)
	if err != nil {
		return controldelegation.Status{}, err
	}
	return s.persist(next, doc)
}

// ResetDelegation removes one configurable profile's explicit Agent binding.
func (s DelegationService) ResetDelegation(ctx context.Context, profile controldelegation.Profile) (controldelegation.Status, error) {
	if s.stack == nil || s.stack.store == nil {
		return controldelegation.Status{}, fmt.Errorf("gatewayapp: delegation configuration is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return controldelegation.Status{}, err
	}
	s.stack.reconfigureMu.Lock()
	defer s.stack.reconfigureMu.Unlock()
	s.stack.agentRosterMu.Lock()
	defer s.stack.agentRosterMu.Unlock()
	if err := s.stack.rejectReconfigureWhileActive("reset subagent profile"); err != nil {
		return controldelegation.Status{}, err
	}
	doc, err := s.stack.store.Load()
	if err != nil {
		return controldelegation.Status{}, err
	}
	next, err := controldelegation.Reset(doc.Delegation, profile)
	if err != nil {
		return controldelegation.Status{}, err
	}
	return s.persist(next, doc)
}

func (s DelegationService) persist(next controldelegation.Configuration, previous AppConfig) (controldelegation.Status, error) {
	doc := previous
	doc.Delegation = controldelegation.NormalizeConfiguration(next)
	if err := s.stack.store.Save(doc); err != nil {
		return controldelegation.Status{}, err
	}
	s.stack.invalidateDelegationResolutionSnapshot()
	return delegationStatusFromConfig(doc.Delegation, doc.AgentRoster, doc.Models.Configs), nil
}

func delegationStatusFromConfig(
	configuration controldelegation.Configuration,
	roster controlagents.Configuration,
	models []modelconfig.Config,
) controldelegation.Status {
	definitions := make(map[controldelegation.Profile]controldelegation.Definition)
	for _, definition := range controldelegation.Definitions() {
		definitions[definition.Profile] = definition
	}
	status := controldelegation.Status{}
	for _, binding := range controldelegation.ListBindings(configuration) {
		profile := controldelegation.ProfileStatus{
			Definition: definitions[binding.Profile],
			Binding:    binding,
		}
		if binding.Target == controldelegation.TargetAgent {
			profile.Agent, _ = controlagents.LookupAgent(roster, binding.AgentID)
		}
		status.Profiles = append(status.Profiles, profile)
	}
	modelByID := make(map[string]modelconfig.Config, len(models))
	for _, raw := range models {
		model := modelconfig.NormalizeConfig(raw)
		if model.ID != "" {
			modelByID[strings.ToLower(strings.TrimSpace(model.ID))] = model
		}
	}
	for _, agent := range controlagents.ListAgents(roster) {
		target := controldelegation.TargetStatus{Agent: agent}
		if model, ok := modelByID[strings.ToLower(strings.TrimSpace(agent.Backing.ModelAlias))]; ok {
			target.ReasoningLevels = append([]string(nil), model.ReasoningLevels...)
		}
		status.Targets = append(status.Targets, target)
	}
	return status
}

var _ controldelegation.Service = DelegationService{}
