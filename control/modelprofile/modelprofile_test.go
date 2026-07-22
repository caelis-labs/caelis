package modelprofile

import (
	"reflect"
	"strings"
	"testing"
)

func TestACPProfilePreservesExactEffortMappingAndDefaults(t *testing.T) {
	raw := ModelProfile{
		ID: " ACP:Claude:Opus ", DisplayName: " Opus ",
		Backend: Backend{ACP: &ACPBackend{
			AgentID: " Claude ", RemoteModelID: " Opus-V4 ",
			SessionDefaults: map[string]string{" mode ": " Code "},
		}},
		Effort: EffortCapability{
			DefaultEffort: "very-high", ACPConfigID: " thought_level ",
			Choices: []EffortChoice{{Canonical: "high", WireValue: "high"}, {Canonical: "very_high", WireValue: "very-High"}},
		},
	}
	if err := Validate(raw); err != nil {
		t.Fatal(err)
	}
	got := Normalize(raw)
	if got.ID != "acp:claude:opus" || got.Backend.ACP.AgentID != "claude" || got.Backend.ACP.RemoteModelID != "Opus-V4" {
		t.Fatalf("Normalize() identities = %#v", got)
	}
	if got.Effort.DefaultEffort != "xhigh" || got.Effort.ACPConfigID != "thought_level" {
		t.Fatalf("Normalize() effort = %#v", got.Effort)
	}
	if wire, ok := got.WireEffort("xhigh"); !ok || wire != "very-High" {
		t.Fatalf("WireEffort(xhigh) = %q, %v", wire, ok)
	}
	if !reflect.DeepEqual(got.Backend.ACP.SessionDefaults, map[string]string{"mode": "Code"}) {
		t.Fatalf("SessionDefaults = %#v", got.Backend.ACP.SessionDefaults)
	}
}

func TestNormalizeOrdersEveryCanonicalEffort(t *testing.T) {
	raw := ModelProfile{
		ID: "provider:model", DisplayName: "Model",
		Backend: Backend{Provider: &ProviderBackend{ModelConfigID: "model"}},
		Effort: EffortCapability{
			DefaultEffort: "medium",
			Choices: []EffortChoice{
				{Canonical: "max", WireValue: "max"},
				{Canonical: "xhigh", WireValue: "xhigh"},
				{Canonical: "minimal", WireValue: "minimal"},
				{Canonical: "none", WireValue: "none"},
				{Canonical: "medium", WireValue: "medium"},
			},
		},
	}
	got := Normalize(raw)
	want := []string{"none", "minimal", "medium", "xhigh", "max"}
	gotCanonical := make([]string, 0, len(got.Effort.Choices))
	for _, choice := range got.Effort.Choices {
		gotCanonical = append(gotCanonical, choice.Canonical)
	}
	if !reflect.DeepEqual(gotCanonical, want) {
		t.Fatalf("Normalize() efforts = %#v, want order %#v", gotCanonical, want)
	}
}

func TestCatalogUpsertAndRemoveArePure(t *testing.T) {
	provider := ModelProfile{
		ID: "provider:one", DisplayName: "One",
		Backend: Backend{Provider: &ProviderBackend{ModelConfigID: "one"}},
		Effort:  EffortCapability{DefaultEffort: "high", Choices: []EffortChoice{{Canonical: "high", WireValue: "high"}}},
	}
	current := Configuration{DefaultProfileID: provider.ID, Profiles: []ModelProfile{provider}}
	replacement := provider
	replacement.DisplayName = "Replacement"
	next, err := Upsert(current, replacement)
	if err != nil {
		t.Fatal(err)
	}
	if next.Profiles[0].DisplayName != "Replacement" || current.Profiles[0].DisplayName == "Replacement" {
		t.Fatalf("Upsert() current=%#v next=%#v", current, next)
	}
	removed := Remove(next, provider.ID)
	if len(removed.Profiles) != 0 || removed.DefaultProfileID != "" {
		t.Fatalf("Remove() = %#v", removed)
	}
}

func TestACPProfileWithoutSelectorExposesOnlyNone(t *testing.T) {
	valid := ModelProfile{
		ID: "acp:helper:default", DisplayName: "Helper",
		Backend: Backend{ACP: &ACPBackend{AgentID: "helper", RemoteModelID: "default"}},
		Effort:  EffortCapability{DefaultEffort: "none", Choices: []EffortChoice{{Canonical: "none"}}},
	}
	if err := Validate(valid); err != nil {
		t.Fatal(err)
	}
	invalid := valid
	invalid.Effort.Choices = append(invalid.Effort.Choices, EffortChoice{Canonical: "high", WireValue: "high"})
	if err := Validate(invalid); err == nil || !strings.Contains(err.Error(), "only none") {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestACPProfileRejectsCollidingSessionDefaults(t *testing.T) {
	profile := ModelProfile{
		ID: "acp:helper:default", DisplayName: "Helper",
		Backend: Backend{ACP: &ACPBackend{
			AgentID: "helper", RemoteModelID: "default",
			SessionDefaults: map[string]string{"Mode": "code", " mode ": "chat"},
		}},
		Effort: EffortCapability{DefaultEffort: "none", Choices: []EffortChoice{{Canonical: "none"}}},
	}
	if err := Validate(profile); err == nil || !strings.Contains(err.Error(), "same ID") {
		t.Fatalf("Validate() error = %v, want normalized session configuration collision", err)
	}
}

func TestValidateRejectsBackendUnionAndEffortDefaultMismatch(t *testing.T) {
	provider := ModelProfile{
		ID: "provider:model", DisplayName: "Model",
		Backend: Backend{Provider: &ProviderBackend{ModelConfigID: "model"}},
		Effort:  EffortCapability{DefaultEffort: "high", Choices: []EffortChoice{{Canonical: "low", WireValue: "low"}}},
	}
	if err := Validate(provider); err == nil || !strings.Contains(err.Error(), "not selectable") {
		t.Fatalf("Validate(default mismatch) error = %v", err)
	}
	provider.Backend.ACP = &ACPBackend{AgentID: "a", RemoteModelID: "m"}
	if err := Validate(provider); err == nil || !strings.Contains(err.Error(), "exactly one backend") {
		t.Fatalf("Validate(union) error = %v", err)
	}
}

func TestConfigurationRejectsDuplicateAndUnknownDefault(t *testing.T) {
	profile := ModelProfile{
		ID: "provider:model", DisplayName: "Model",
		Backend: Backend{Provider: &ProviderBackend{ModelConfigID: "model"}},
		Effort:  EffortCapability{DefaultEffort: "high", Choices: []EffortChoice{{Canonical: "high", WireValue: "high"}}},
	}
	if err := ValidateConfiguration(Configuration{Profiles: []ModelProfile{profile, profile}}); err == nil || !strings.Contains(err.Error(), "duplicate profile") {
		t.Fatalf("ValidateConfiguration(duplicate) error = %v", err)
	}
	if err := ValidateConfiguration(Configuration{DefaultProfileID: "missing", Profiles: []ModelProfile{profile}}); err == nil || !strings.Contains(err.Error(), "unknown profile") {
		t.Fatalf("ValidateConfiguration(default) error = %v", err)
	}
}

func TestStableIDBuilders(t *testing.T) {
	if got := BuildProviderID(" Provider/Model "); got != "provider:provider/model" {
		t.Fatalf("BuildProviderID() = %q", got)
	}
	first := BuildACPID("Claude", "Opus-V4")
	if first == "" || first != BuildACPID(" claude ", "Opus-V4") || first == BuildACPID("claude", "opus-v4") {
		t.Fatalf("BuildACPID() identities = %q", first)
	}
}
