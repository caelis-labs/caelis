package placement

import (
	"encoding/json"
	"testing"

	sdkplacement "github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelprofile"
	"github.com/caelis-labs/caelis/control/modelprofile/builder"
)

func TestACPProfileSpeedDefaultsReachFrozenPlacement(t *testing.T) {
	for _, configured := range []string{"", "default", "priority"} {
		t.Run("configured="+configured, func(t *testing.T) {
			snapshot := testSnapshot()
			defaults := agents.SessionOptions{}
			if configured != "" {
				defaults.ConfigValues = map[string]string{"SERVICE_TIER": configured}
			}
			profile, err := builder.FromACP(snapshot.Agents.Agents[0], snapshot.Agents.Connections[0], agents.RemoteModel{ID: "model"}, defaults,
				agents.DiscoverySnapshot{ConfigOptions: []agents.ConfigOption{{ID: "service_tier", CurrentValue: "priority", Options: []agents.ConfigChoice{{Value: "default"}, {Value: "priority"}}}}})
			if err != nil {
				t.Fatal(err)
			}
			snapshot.Profiles = modelprofile.Configuration{Profiles: []modelprofile.ModelProfile{profile}}
			snapshot.Bindings = agentbinding.Configuration{Bindings: []agentbinding.Binding{{Handle: agentbinding.HandleOrbit, ProfileID: profile.ID, Effort: "none"}}}
			for _, speed := range []string{"", "standard", "fast"} {
				snapshot.Bindings.Bindings[0].Speed = speed
				frozen, err := ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleOrbit, Purpose: PurposeSpawn})
				if err != nil {
					t.Fatal(err)
				}
				want := configured
				if speed != "" {
					want, _ = profile.WireSpeed(speed)
				}
				if got := frozen.SessionConfigValues["service_tier"]; got != want {
					t.Fatalf("binding speed %q: tier = %q, want %q", speed, got, want)
				}
				raw, err := json.Marshal(frozen)
				if err != nil {
					t.Fatal(err)
				}
				var restored sdkplacement.Placement
				if err := json.Unmarshal(raw, &restored); err != nil {
					t.Fatal(err)
				}
				snapshot.Bindings.Bindings[0].Speed = "fast"
				if err := ValidateFrozen(snapshot, restored); err != nil {
					t.Fatalf("restored placement changed after rebinding: %v", err)
				}
			}
			if got := profile.Backend.ACP.SessionDefaults["service_tier"]; got != configured {
				t.Fatalf("binding overwrote profile default: %q, want %q", got, configured)
			}
		})
	}
}

func TestFrozenSpeedSurvivesRebindingAndRestart(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Profiles.Profiles[1].Speed = modelprofile.SpeedCapability{ACPConfigID: "service_tier", DefaultSpeed: "fast", Choices: []modelprofile.SpeedChoice{{Canonical: "standard", WireValue: "default"}, {Canonical: "fast", WireValue: "priority"}}}
	snapshot.Bindings.Bindings[0].Speed = "standard"
	frozen, err := ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleOrbit, Purpose: PurposeSpawn})
	if err != nil {
		t.Fatal(err)
	}
	if frozen.SessionConfigValues["service_tier"] != "default" || frozen.SessionConfigValues["thought_level"] != "very-high" {
		t.Fatalf("independent speed/effort lost: %#v", frozen)
	}
	raw, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	var restored sdkplacement.Placement
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	snapshot.Bindings.Bindings[0].Speed = "fast"
	if err := ValidateFrozen(snapshot, restored); err != nil {
		t.Fatalf("rebinding changed prepared work: %v", err)
	}
	current, err := ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleOrbit, Purpose: PurposeSpawn})
	if err != nil || current.SessionConfigValues["service_tier"] != "priority" || current.Fingerprint == restored.Fingerprint {
		t.Fatalf("new work did not use new selection: %#v %v", current, err)
	}
	snapshot.Profiles.Profiles[1].Speed.Choices[1].WireValue = "fast"
	if err := ValidateFrozen(snapshot, restored); err == nil {
		t.Fatal("changed capability silently retargeted frozen work")
	}
}

func TestProviderSpeedCannotBypassEndpointValidation(t *testing.T) {
	snapshot := testSnapshot()
	snapshot.Profiles.Profiles[0].Speed = modelprofile.SpeedCapability{DefaultSpeed: "standard", Choices: []modelprofile.SpeedChoice{{Canonical: "standard", WireValue: "default"}, {Canonical: "fast", WireValue: "priority"}}}
	snapshot.Bindings.Bindings[1].Speed = "fast"
	if _, err := ResolveHandle(snapshot, HandleRequest{Handle: agentbinding.HandleGuardian, Purpose: PurposeGuardian}); err == nil {
		t.Fatal("unverified provider endpoint accepted Fast")
	}
}
