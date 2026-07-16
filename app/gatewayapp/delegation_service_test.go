package gatewayapp

import (
	"context"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	controldelegation "github.com/caelis-labs/caelis/control/delegation"
)

func TestDelegationServicePersistsBindingWithoutReplacingRuntime(t *testing.T) {
	store := newAppConfigStore(t.TempDir())
	if err := store.Save(AppConfig{AgentRoster: controlagents.Configuration{
		Connections: []controlagents.Connection{{ID: "claude", Launcher: controlagents.Launcher{Command: "claude-acp"}}},
		Agents:      []controlagents.Agent{{ID: "claude", Name: "Claude", Backing: controlagents.AgentBacking{ConnectionID: "claude"}}},
	}}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	refreshes := 0
	stack := &Stack{store: store, refreshConfiguredAgentsHook: func() error {
		refreshes++
		return nil
	}}
	service := stack.Delegation()
	status, err := service.BindDelegation(context.Background(), controldelegation.BindRequest{
		Profile: controldelegation.ProfileOrbit,
		AgentID: "claude",
	})
	if err != nil {
		t.Fatalf("BindDelegation() error = %v", err)
	}
	if refreshes != 0 {
		t.Fatalf("runtime refreshes = %d, want none", refreshes)
	}
	assertDelegationProfileTarget(t, status, controldelegation.ProfileOrbit, "claude")

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	binding, ok := controldelegation.LookupBinding(loaded.Delegation, controldelegation.ProfileOrbit)
	if !ok || binding.Target != controldelegation.TargetAgent || binding.AgentID != "claude" {
		t.Fatalf("persisted binding = %#v, ok=%v", binding, ok)
	}

	status, err = service.ResetDelegation(context.Background(), controldelegation.ProfileOrbit)
	if err != nil {
		t.Fatalf("ResetDelegation() error = %v", err)
	}
	if refreshes != 0 {
		t.Fatalf("runtime refreshes = %d, want none", refreshes)
	}
	assertDelegationProfileTarget(t, status, controldelegation.ProfileOrbit, "")
}

func assertDelegationProfileTarget(t *testing.T, status controldelegation.Status, profile controldelegation.Profile, agentID string) {
	t.Helper()
	for _, item := range status.Profiles {
		if item.Definition.Profile != profile {
			continue
		}
		if agentID == "" {
			if item.Binding.Target != controldelegation.TargetSelf {
				t.Fatalf("profile %q binding = %#v, want self", profile, item.Binding)
			}
			return
		}
		if item.Binding.Target != controldelegation.TargetAgent || item.Agent.ID != agentID {
			t.Fatalf("profile %q status = %#v, want Agent %q", profile, item, agentID)
		}
		return
	}
	t.Fatalf("profile %q missing from status %#v", profile, status)
}
