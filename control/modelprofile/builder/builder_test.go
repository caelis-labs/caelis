package builder

import (
	"reflect"
	"testing"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestFromProviderBuildsStandardProfile(t *testing.T) {
	profile, err := FromProvider(modelconfig.Config{
		Provider: "openai", ProviderEndpointID: "openai@default", Alias: "openai/gpt-5", Model: "gpt-5",
		DefaultReasoningEffort: "high", ReasoningLevels: []string{"low", "high"},
	})
	if err != nil {
		t.Fatalf("FromProvider() error = %v", err)
	}
	if profile.ID != "provider:openai@default/openai/gpt-5" || profile.Kind() != modelprofile.BackendProvider || profile.Effort.DefaultEffort != "high" {
		t.Fatalf("FromProvider() = %#v", profile)
	}
}

func TestFromACPKeepsConnectionIdentityAndMovesModelDefaultsToProfile(t *testing.T) {
	agent := controlagents.Agent{ID: "claude", Name: "Claude", ConnectionID: "claude"}
	connection := controlagents.Connection{ID: "claude", Name: "Claude", Launcher: controlagents.Launcher{Command: "claude-agent"}}
	discovery := controlagents.DiscoverySnapshot{
		ConnectionID: "claude",
		ConfigOptions: []controlagents.ConfigOption{{
			ID: "thought_level", Purpose: controlagents.ConfigOptionPurposeReasoningEffort, CurrentValue: "very-high",
			Options: []controlagents.ConfigChoice{{Value: "high"}, {Value: "very-high"}},
		}},
	}
	profile, err := FromACP(
		agent,
		connection,
		controlagents.RemoteModel{ID: "Opus-V4", Name: "Opus"},
		controlagents.SessionOptions{ModelID: "Opus-V4", ConfigValues: map[string]string{"mode": "code", "thought_level": "very-high"}},
		discovery,
	)
	if err != nil {
		t.Fatalf("FromACP() error = %v", err)
	}
	if profile.Backend.ACP.AgentID != "claude" || profile.Backend.ACP.RemoteModelID != "Opus-V4" || profile.Effort.DefaultEffort != "xhigh" {
		t.Fatalf("FromACP() = %#v", profile)
	}
	if !reflect.DeepEqual(profile.Backend.ACP.SessionDefaults, map[string]string{"mode": "code"}) {
		t.Fatalf("SessionDefaults = %#v", profile.Backend.ACP.SessionDefaults)
	}
}
