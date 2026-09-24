package gatewayapp

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestAgentBindingStatusProjectsNativeProfileEligibility(t *testing.T) {
	profiles := modelprofile.Configuration{Profiles: []modelprofile.ModelProfile{
		{ID: "provider:model", Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{ModelConfigID: "model"}}},
		{ID: "provider:judge", Judgment: true, Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{ModelConfigID: "judge"}}},
		{ID: "acp:agent", Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{AgentID: "agent", RemoteModelID: "model"}}},
	}}
	bindings := agentbinding.Configuration{Roles: []agentbinding.Role{{Handle: "reviewer", Description: "Review changes"}}}
	status := agentBindingStatusFromConfig(bindings, profiles)
	wants := map[agentbinding.Handle][]string{
		agentbinding.HandleSelf:           {},
		agentbinding.HandleBreeze:         {"acp:agent", "provider:model"},
		agentbinding.HandleGuardian:       {"provider:model"},
		agentbinding.HandleSteward:        {"provider:model"},
		agentbinding.HandleToolSearch:     {"provider:judge"},
		agentbinding.HandleGuardianScreen: {"provider:judge"},
		agentbinding.HandleMemoryVerifier: {"provider:judge"},
		"reviewer":                        {"acp:agent", "provider:model"},
	}
	for _, handle := range status.Handles {
		want, ok := wants[handle.Definition.Handle]
		if !ok {
			continue
		}
		if !reflect.DeepEqual(handle.EligibleProfileIDs, want) {
			t.Errorf("%s eligible profiles = %#v, want %#v", handle.Definition.Handle, handle.EligibleProfileIDs, want)
		}
		delete(wants, handle.Definition.Handle)
	}
	if len(wants) != 0 {
		t.Fatalf("missing handles: %v", wants)
	}
	for _, handle := range agentBindingStatusFromConfig(bindings, modelprofile.Configuration{}).Handles {
		if handle.EligibleProfileIDs == nil || len(handle.EligibleProfileIDs) != 0 {
			t.Errorf("%s empty catalog eligibility = %#v, want empty array", handle.Definition.Handle, handle.EligibleProfileIDs)
		}
	}
}
