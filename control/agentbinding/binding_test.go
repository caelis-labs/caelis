package agentbinding

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestBindRequiresExplicitSupportedEffort(t *testing.T) {
	profiles := testProfiles()
	if _, err := Bind(Configuration{}, Binding{Handle: HandleOrbit, ProfileID: "provider:model"}, profiles); err == nil || !strings.Contains(err.Error(), "explicit effort") {
		t.Fatalf("Bind(missing effort) error = %v", err)
	}
	if _, err := Bind(Configuration{}, Binding{Handle: HandleOrbit, ProfileID: "provider:model", Effort: "xhigh"}, profiles); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("Bind(unsupported effort) error = %v", err)
	}
	got, err := Bind(Configuration{}, Binding{Handle: " Orbit ", ProfileID: " PROVIDER:MODEL ", Effort: " HIGH "}, profiles)
	if err != nil {
		t.Fatal(err)
	}
	want := Configuration{Bindings: []Binding{{Handle: HandleOrbit, ProfileID: "provider:model", Effort: "high"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Bind() = %#v, want %#v", got, want)
	}
}

func TestPrepareProfileRemovalDropsOrdinaryBindingsButProtectsSystemBindings(t *testing.T) {
	current := Configuration{Bindings: []Binding{
		{Handle: HandleOrbit, ProfileID: "provider:model", Effort: "high"},
		{Handle: HandleReviewer, ProfileID: "provider:model", Effort: "high"},
	}}
	_, err := PrepareProfileRemoval(current, "provider:model")
	var inUse *ProfileInUseError
	if !errors.As(err, &inUse) || inUse.Handle != HandleReviewer {
		t.Fatalf("PrepareProfileRemoval(system) error = %v", err)
	}
	ordinary := Configuration{Bindings: current.Bindings[:1]}
	next, err := PrepareProfileRemoval(ordinary, "provider:model")
	if err != nil || len(next.Bindings) != 0 {
		t.Fatalf("PrepareProfileRemoval(ordinary) = %#v, %v", next, err)
	}
}

func TestSystemHandleRejectsACPBeforeMutation(t *testing.T) {
	current := Configuration{Bindings: []Binding{{Handle: HandleGuardian, ProfileID: "provider:model", Effort: "high"}}}
	_, err := Bind(current, Binding{Handle: HandleGuardian, ProfileID: "acp:claude:opus", Effort: "xhigh"}, testProfiles())
	var unsupported *UnsupportedBackendError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Bind(ACP guardian) error = %v, want UnsupportedBackendError", err)
	}
	if unsupported.Handle != HandleGuardian || unsupported.ProfileID != "acp:claude:opus" {
		t.Fatalf("UnsupportedBackendError = %#v", unsupported)
	}
}

func TestSelfCannotBePersisted(t *testing.T) {
	_, err := Bind(Configuration{}, Binding{Handle: HandleSelf, ProfileID: "provider:model", Effort: "high"}, testProfiles())
	if err == nil || !strings.Contains(err.Error(), "cannot be bound") {
		t.Fatalf("Bind(self) error = %v", err)
	}
}

func TestRemoveProfileBindingsReportsEveryAffectedHandle(t *testing.T) {
	current := Configuration{Bindings: []Binding{
		{Handle: HandleBreeze, ProfileID: "provider:model", Effort: "low"},
		{Handle: HandleOrbit, ProfileID: "acp:claude:opus", Effort: "xhigh"},
		{Handle: HandleReviewer, ProfileID: "provider:model", Effort: "high"},
	}}
	next, removed := RemoveProfileBindings(current, "provider:model")
	if want := []Handle{HandleBreeze, HandleReviewer}; !reflect.DeepEqual(removed, want) {
		t.Fatalf("removed = %#v, want %#v", removed, want)
	}
	if len(next.Bindings) != 1 || next.Bindings[0].Handle != HandleOrbit {
		t.Fatalf("remaining = %#v", next)
	}
}

func testProfiles() modelprofile.Configuration {
	return modelprofile.Configuration{Profiles: []modelprofile.ModelProfile{
		{
			ID: "provider:model", DisplayName: "Provider Model",
			Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{ModelConfigID: "model"}},
			Effort:  modelprofile.EffortCapability{DefaultEffort: "high", Choices: []modelprofile.EffortChoice{{Canonical: "low", WireValue: "low"}, {Canonical: "high", WireValue: "high"}}},
		},
		{
			ID: "acp:claude:opus", DisplayName: "Claude Opus",
			Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{AgentID: "claude", RemoteModelID: "opus"}},
			Effort:  modelprofile.EffortCapability{DefaultEffort: "xhigh", ACPConfigID: "thought_level", Choices: []modelprofile.EffortChoice{{Canonical: "high", WireValue: "high"}, {Canonical: "xhigh", WireValue: "very-high"}}},
		},
	}}
}
