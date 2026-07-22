package agents

import (
	"testing"

	"github.com/caelis-labs/caelis/control/agentbinding"
)

func TestDirectRunSourceRoundTripUsesAgentBindingHandle(t *testing.T) {
	t.Parallel()

	for _, handle := range agentbinding.DirectRunHandles() {
		source := DirectRunSource(handle)
		got, ok := DirectRunHandleFromSource(source)
		if !ok || got != handle {
			t.Fatalf("DirectRunHandleFromSource(%q) = %q, %v; want %q, true", source, got, ok, handle)
		}
	}
	for _, handle := range []agentbinding.Handle{agentbinding.HandleSelf, agentbinding.HandleGuardian, agentbinding.HandleReviewer, "unknown"} {
		if source := DirectRunSource(handle); source != "" {
			t.Errorf("DirectRunSource(%q) = %q, want empty", handle, source)
		}
	}
	for _, source := range []string{"", "orbit", "slash_profile_self", "slash_profile_guardian", "slash_profile_unknown"} {
		if handle, ok := DirectRunHandleFromSource(source); ok {
			t.Errorf("DirectRunHandleFromSource(%q) = %q, true; want rejection", source, handle)
		}
	}
}

func TestDirectRunFromParticipantRequiresACPSidecar(t *testing.T) {
	t.Parallel()

	source := DirectRunSource(agentbinding.HandleOrbit)
	got := DirectRunFromParticipant("lina", "acp", "sidecar", source)
	if got.Name != "orbit(lina)" || got.Agent != "orbit" || !got.Addressable {
		t.Fatalf("DirectRunFromParticipant() = %#v", got)
	}
	if DirectRunFromParticipant("lina", "model", "sidecar", source).Addressable {
		t.Fatal("model participant was addressable as a direct ACP run")
	}
	if DirectRunFromParticipant("lina", "acp", "controller", source).Addressable {
		t.Fatal("controller participant was addressable as a direct sidecar run")
	}
}
