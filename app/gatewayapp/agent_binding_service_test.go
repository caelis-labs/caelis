package gatewayapp

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestAgentBindingServicePersistsUnifiedProfileBindingAndRefreshesRuntime(t *testing.T) {
	store := newAppConfigStore(t.TempDir())
	profile := modelprofile.ModelProfile{
		ID: "acp:claude:opus", DisplayName: "Claude Opus",
		Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{AgentID: "claude", RemoteModelID: "opus"}},
		Effort:  modelprofile.EffortCapability{DefaultEffort: "none", Choices: []modelprofile.EffortChoice{{Canonical: "none"}}},
	}
	if err := store.Save(AppConfig{
		ExternalAgents: controlagents.Configuration{
			Connections: []controlagents.Connection{{ID: "claude", Launcher: controlagents.Launcher{Command: "claude-acp"}}},
			Agents:      []controlagents.Agent{{ID: "claude", Name: "Claude", ConnectionID: "claude"}},
		},
		ModelProfiles: modelprofile.Configuration{Profiles: []modelprofile.ModelProfile{profile}},
	}); err != nil {
		t.Fatal(err)
	}
	refreshes := 0
	stack := &Stack{store: store, refreshConfiguredAgentsHook: func() error {
		refreshes++
		return nil
	}}
	service := stack.AgentBindings()
	status, err := service.BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleOrbit, ProfileID: profile.ID, Effort: "none",
	})
	if err != nil {
		t.Fatal(err)
	}
	if refreshes != 1 {
		t.Fatalf("runtime refreshes = %d, want 1", refreshes)
	}
	assertAgentBindingTarget(t, status, agentbinding.HandleOrbit, profile.ID)

	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	binding, ok := agentbinding.Lookup(loaded.AgentBindings, agentbinding.HandleOrbit)
	if !ok || binding.ProfileID != profile.ID || binding.Effort != "none" {
		t.Fatalf("persisted binding = %#v, ok=%v", binding, ok)
	}

	status, err = service.ResetAgentBinding(context.Background(), agentbinding.HandleOrbit)
	if err != nil {
		t.Fatal(err)
	}
	if refreshes != 2 {
		t.Fatalf("runtime refreshes = %d, want 2", refreshes)
	}
	assertAgentBindingTarget(t, status, agentbinding.HandleOrbit, "")
}

func TestAgentBindingServiceRollsForwardAfterCommittedConfigWriteFault(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	profile, err := stack.Connect(ModelConfig{Provider: "ollama", Model: "binding-committed"})
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("directory fsync after rename failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "fsync", fault)

	status, err := stack.AgentBindings().BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleOrbit, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort,
	})
	requireCommittedConfigWriteError(t, err, fault)
	if writeCount() != 1 {
		t.Fatalf("config writes = %d, want one committed write", writeCount())
	}
	assertAgentBindingTarget(t, status, agentbinding.HandleOrbit, profile.ID)
	loaded, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if binding, ok := agentbinding.Lookup(loaded.AgentBindings, agentbinding.HandleOrbit); !ok || binding.ProfileID != profile.ID {
		t.Fatalf("committed binding = %#v, ok=%v", binding, ok)
	}
	if _, ok := agentConfigForToolTest(stack.runtime.Assembly.Agents, string(agentbinding.HandleOrbit)); !ok {
		t.Fatalf("runtime assembly is missing committed binding %q", agentbinding.HandleOrbit)
	}
}

func assertAgentBindingTarget(t *testing.T, status agentbinding.Status, handle agentbinding.Handle, profileID string) {
	t.Helper()
	for _, item := range status.Handles {
		if item.Definition.Handle != handle {
			continue
		}
		if item.Binding.ProfileID != profileID {
			t.Fatalf("handle %q status = %#v, want ModelProfile %q", handle, item, profileID)
		}
		return
	}
	t.Fatalf("handle %q missing from status %#v", handle, status)
}
