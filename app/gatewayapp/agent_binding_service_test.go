package gatewayapp

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp/internal/configstore"
	"github.com/caelis-labs/caelis/control/agentbinding"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestAgentBindingServicePersistsUnifiedProfileBindingForFutureActivation(t *testing.T) {
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
	stack := &Stack{store: store}
	service := stack.testAgentBindings()
	status, err := service.BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleOrbit, ProfileID: profile.ID, Effort: "none",
	})
	if err != nil {
		t.Fatal(err)
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
	assertAgentBindingTarget(t, status, agentbinding.HandleOrbit, "")
}

func TestAgentBindingServiceRollsForwardAfterCommittedConfigWriteFault(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	profile, err := stack.connectTestModel(ModelConfig{Provider: "ollama", Model: "binding-committed"})
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("directory fsync after rename failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "fsync", fault)

	revision, err := stack.ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := stack.AgentCommands().BindAgentBinding(context.Background(), controlclient.Principal{ID: stack.UserID}, controlclient.BindAgentBindingRequest{
		WriteBase: controlclient.WriteBase{OperationID: "binding-committed-fault", ExpectedRevision: &revision},
		Binding:   agentbinding.Binding{Handle: agentbinding.HandleOrbit, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort},
	})
	if err != nil || result.Outcome != controlclient.OutcomeCommitted || !strings.Contains(result.Detail, fault.Error()) {
		t.Fatalf("BindAgentBinding() = %#v, %v", result, err)
	}
	if writeCount() != 1 {
		t.Fatalf("config writes = %d, want one committed write", writeCount())
	}
	status, err := stack.AgentBindings().AgentBindingStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertAgentBindingTarget(t, status, agentbinding.HandleOrbit, profile.ID)
	loaded, loadErr := stack.store.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if binding, ok := agentbinding.Lookup(loaded.AgentBindings, agentbinding.HandleOrbit); !ok || binding.ProfileID != profile.ID {
		t.Fatalf("committed binding = %#v, ok=%v", binding, ok)
	}
}

func TestAgentBindingCommandCachesUnknownWhenCommittedRevisionCannotBeObserved(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	profile, err := stack.connectTestModel(ModelConfig{Provider: "ollama", Model: "binding-readback"})
	if err != nil {
		t.Fatal(err)
	}
	fault := errors.New("directory fsync after rename failed")
	writeCount := installCommittedConfigSaveFault(t, stack, "fsync", fault)
	committedFault := stack.store.saveHook
	committedPath := stack.store.path + ".committed"
	pathBlocked := false
	restore := func() {
		if !pathBlocked {
			return
		}
		if err := os.Remove(stack.store.path); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(committedPath, stack.store.path); err != nil {
			t.Fatal(err)
		}
		pathBlocked = false
	}
	t.Cleanup(restore)
	stack.store.saveHook = func(doc AppConfig) error {
		committedErr := committedFault(doc)
		if !configstore.WriteCommitted(committedErr) {
			return committedErr
		}
		if err := os.Rename(stack.store.path, committedPath); err != nil {
			return errors.Join(committedErr, err)
		}
		if err := os.Mkdir(stack.store.path, 0o700); err != nil {
			_ = os.Rename(committedPath, stack.store.path)
			return errors.Join(committedErr, err)
		}
		pathBlocked = true
		return committedErr
	}
	expected, err := stack.ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	request := controlclient.BindAgentBindingRequest{
		WriteBase: controlclient.WriteBase{OperationID: "binding-committed-readback", ExpectedRevision: &expected},
		Binding:   agentbinding.Binding{Handle: agentbinding.HandleOrbit, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort},
	}
	result, err := stack.AgentCommands().BindAgentBinding(context.Background(), controlclient.Principal{ID: stack.UserID}, request)
	if !errors.Is(err, fault) || result.Outcome != controlclient.OutcomeUnknown || result.Revision != 0 {
		t.Fatalf("BindAgentBinding(committed readback failure) = %#v, %v", result, err)
	}
	restore()
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if binding, ok := agentbinding.Lookup(doc.AgentBindings, agentbinding.HandleOrbit); !ok || binding.ProfileID != profile.ID {
		t.Fatalf("persisted binding = %#v, %v", binding, ok)
	}
	replayed, replayErr := stack.AgentCommands().BindAgentBinding(context.Background(), controlclient.Principal{ID: stack.UserID}, request)
	if replayErr != nil || replayed != result || writeCount() != 1 {
		t.Fatalf("BindAgentBinding(replay) = %#v, %v writes=%d; want %#v and one write", replayed, replayErr, writeCount(), result)
	}
}

func TestAgentBindingCommandCommitsWithoutRuntimeRefresh(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	profile, err := stack.connectTestModel(ModelConfig{Provider: "ollama", Model: "binding-refresh"})
	if err != nil {
		t.Fatal(err)
	}
	expected, err := stack.ConfigurationRevision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := stack.AgentCommands().BindAgentBinding(context.Background(), controlclient.Principal{ID: stack.UserID}, controlclient.BindAgentBindingRequest{
		WriteBase: controlclient.WriteBase{OperationID: "binding-refresh-failure", ExpectedRevision: &expected},
		Binding:   agentbinding.Binding{Handle: agentbinding.HandleOrbit, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort},
	})
	if err != nil || result.Outcome != controlclient.OutcomeCommitted || result.Revision != expected+1 {
		t.Fatalf("BindAgentBinding() = %#v, %v", result, err)
	}
	doc, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if binding, ok := agentbinding.Lookup(doc.AgentBindings, agentbinding.HandleOrbit); !ok || binding.ProfileID != profile.ID {
		t.Fatalf("committed binding was rolled back: %#v, %v", binding, ok)
	}
}

func TestAgentBindingServicePersistsCustomRoleAndSwitchesNamedSnapshot(t *testing.T) {
	stack, _ := newLocalStateTestStack(t)
	profile, err := stack.connectTestModel(ModelConfig{Provider: "ollama", Model: "binding-role"})
	if err != nil {
		t.Fatal(err)
	}
	service := stack.testAgentBindings()
	status, err := service.CreateAgentRole(context.Background(), agentbinding.Role{
		Handle: "research", Description: "Investigate unfamiliar systems.",
	}, agentbinding.Binding{ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort})
	if err != nil {
		t.Fatal(err)
	}
	assertAgentBindingTarget(t, status, "research", profile.ID)

	if _, err := service.SaveAgentBindingSet(context.Background(), "research-only"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.BindAgentBinding(context.Background(), agentbinding.Binding{
		Handle: agentbinding.HandleOrbit, ProfileID: profile.ID, Effort: profile.Effort.DefaultEffort,
	}); err != nil {
		t.Fatal(err)
	}
	status, err = service.ApplyAgentBindingSet(context.Background(), "research-only")
	if err != nil {
		t.Fatal(err)
	}
	assertAgentBindingTarget(t, status, agentbinding.HandleOrbit, "")
	assertAgentBindingTarget(t, status, "research", profile.ID)

	loaded, err := stack.store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if role, ok := agentbinding.LookupRole(loaded.AgentBindings, "research"); !ok || role.Description == "" {
		t.Fatalf("persisted role = %#v, %v", role, ok)
	}
	if set, ok := agentbinding.LookupBindingSet(loaded.AgentBindings, "research-only"); !ok || len(set.Bindings) != 1 {
		t.Fatalf("persisted set = %#v, %v", set, ok)
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
