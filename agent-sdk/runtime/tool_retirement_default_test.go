package runtime

import (
	"errors"
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
)

func TestCheckpointCustomDeferredSourceRequiresCatalogCapability(t *testing.T) {
	store, _ := newTestSessionService(t, "catalog-capability")
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := rt.withMCPCheckpointAuthority(agent.ModelRequestSnapshot{DeferredTools: &runtimeDeferredSource{}})
	if snapshot.Validate == nil {
		t.Fatal("custom deferred source bypassed checkpoint capability validation")
	}
	release, err := snapshot.Validate(t.Context())
	if release != nil {
		release()
		t.Fatal("missing capability returned a release guard")
	}
	var capabilityErr *CapabilityError
	if !errors.As(err, &capabilityErr) || capabilityErr.Capability != "tool.CatalogSource" {
		t.Fatalf("error = %T %v, want *runtime.CapabilityError", err, err)
	}
	if errors.Is(err, agent.ErrModelRequestSnapshotStale) {
		t.Fatal("missing capability classified as stale")
	}
}
