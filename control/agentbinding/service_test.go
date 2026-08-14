package agentbinding

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/modelprofile"
)

func TestProjectBoundDirectNamesUsesStatusAsSingleProjectionSource(t *testing.T) {
	status := Status{Handles: []HandleStatus{
		{
			Definition: Definition{Handle: HandleBreeze, Class: HandleClassDelegation, Configurable: true},
			Binding:    Binding{Handle: HandleBreeze, ProfileID: "provider:model", Effort: "high"},
		},
		{
			Definition: Definition{Handle: HandleOrbit, Class: HandleClassDelegation, Configurable: true},
		},
		{
			Definition: Definition{Handle: "research", Class: HandleClassDelegation, Configurable: true, Custom: true},
			Binding:    Binding{Handle: "research", ProfileID: "acp:claude:opus", Effort: "xhigh"},
		},
	}}

	got := ProjectBoundDirectNames(
		[]string{"help", "breeze", "orbit", "zenith", "status", "help"},
		status,
	)
	want := []string{"help", "breeze", "status", "research"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ProjectBoundDirectNames() = %#v, want %#v", got, want)
	}
}

func TestSupportsProfileAllowsACPReviewerButKeepsGuardianProviderOnly(t *testing.T) {
	provider := modelprofile.ModelProfile{Backend: modelprofile.Backend{Provider: &modelprofile.ProviderBackend{ModelConfigID: "provider/model"}}}
	acp := modelprofile.ModelProfile{Backend: modelprofile.Backend{ACP: &modelprofile.ACPBackend{AgentID: "claude", RemoteModelID: "opus"}}}

	if !SupportsProfile(HandleReviewer, provider) || !SupportsProfile(HandleReviewer, acp) {
		t.Fatal("Reviewer did not accept both provider and ACP profiles")
	}
	if !SupportsProfile(HandleGuardian, provider) || SupportsProfile(HandleGuardian, acp) {
		t.Fatal("Guardian profile eligibility is not provider-only")
	}
}

func TestBoundDirectHandlesReturnsDetachedCanonicalOrder(t *testing.T) {
	status := Status{Handles: []HandleStatus{
		{
			Definition: Definition{Handle: " Research ", Class: HandleClassDelegation, Configurable: true, Custom: true},
			Binding:    Binding{ProfileID: "acp:claude:opus", Effort: "high"},
		},
		{
			Definition: Definition{Handle: HandleReviewer, Class: HandleClassSystem, Configurable: true},
			Binding:    Binding{ProfileID: "provider:model", Effort: "high"},
		},
	}}
	got := BoundDirectHandles(status)
	if len(got) != 1 || got[0].Definition.Handle != "research" {
		t.Fatalf("BoundDirectHandles() = %#v", got)
	}
	if status.Handles[0].Definition.Handle != " Research " {
		t.Fatalf("BoundDirectHandles mutated source status: %#v", status.Handles)
	}
	if !IsBoundDirectHandle(status, "research") || IsBoundDirectHandle(status, HandleReviewer) {
		t.Fatalf("IsBoundDirectHandle() did not distinguish bound delegation from system handle")
	}
}
