package builder

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestACPSpeedDiscoveryBindingAndSealedRoundTrip(t *testing.T) {
	p, err := FromACP(agents.Agent{ID: "codex", ConnectionID: "codex"}, agents.Connection{ID: "codex", Name: "Codex", Launcher: agents.Launcher{Command: "codex"}}, agents.RemoteModel{ID: "model"}, agents.SessionOptions{}, agents.DiscoverySnapshot{ConfigOptions: []agents.ConfigOption{{ID: "service_tier", CurrentValue: "priority", Options: []agents.ConfigChoice{{Value: "default"}, {Value: "priority"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	if !p.SupportsFast() || p.Speed.DefaultSpeed != "fast" || len(p.Backend.ACP.SessionDefaults) != 0 {
		t.Fatalf("profile = %#v", p)
	}
	catalog := modelprofile.Configuration{Profiles: []modelprofile.ModelProfile{p}}
	bindings, err := agentbinding.Bind(agentbinding.Configuration{}, agentbinding.Binding{Handle: agentbinding.HandleBreeze, ProfileID: p.ID, Effort: "none", Speed: "fast"}, catalog)
	if err != nil {
		t.Fatal(err)
	}
	bindings, err = agentbinding.SaveBindingSet(bindings, "fast-set")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(bindings)
	if err != nil {
		t.Fatal(err)
	}
	var restored agentbinding.Configuration
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(bindings, restored) {
		t.Fatal("binding set lost speed")
	}
	base, err := placement.Seal(placement.Placement{Kind: placement.KindAgent, Agent: "codex", ProfileID: p.ID, Model: "model", ConfigFingerprint: "config"})
	if err != nil {
		t.Fatal(err)
	}
	fast, err := p.ApplySpeed(base, "fast")
	if err != nil {
		t.Fatal(err)
	}
	off, err := p.ApplySpeed(base, "standard")
	if err != nil {
		t.Fatal(err)
	}
	if fast.SessionConfigValues["service_tier"] != "priority" || off.SessionConfigValues["service_tier"] != "default" || fast.Fingerprint == off.Fingerprint || base.SessionConfigValues != nil {
		t.Fatal("non-independent placement")
	}
	raw, _ = json.Marshal(fast)
	var frozen placement.Placement
	if err = json.Unmarshal(raw, &frozen); err != nil {
		t.Fatal(err)
	}
	if err = placement.ValidateSealed(frozen); err != nil {
		t.Fatal(err)
	}
	if p.SelectedSpeed(frozen) != "fast" {
		t.Fatal("lost selected speed")
	}
	p.Speed = modelprofile.SpeedCapability{}
	catalog.Profiles = []modelprofile.ModelProfile{p}
	if err = agentbinding.ValidateConfiguration(restored, catalog); err == nil {
		t.Fatal("accepted stale capability")
	}
}
