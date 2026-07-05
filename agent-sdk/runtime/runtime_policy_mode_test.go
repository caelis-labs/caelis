package runtime

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestModeOptionsFromSessionReadsPolicyNetworkEnabledMetadata(t *testing.T) {
	t.Parallel()

	opts := modeOptionsFromSession(session.Session{}, agent.AgentSpec{
		Metadata: map[string]any{"policy_network_enabled": false},
	})
	if opts.NetworkEnabled == nil || *opts.NetworkEnabled {
		t.Fatalf("NetworkEnabled = %#v, want false from metadata", opts.NetworkEnabled)
	}

	opts = modeOptionsFromSession(session.Session{}, agent.AgentSpec{
		Metadata: map[string]any{"policy_network_enabled": "on"},
	})
	if opts.NetworkEnabled == nil || !*opts.NetworkEnabled {
		t.Fatalf("NetworkEnabled = %#v, want true from string metadata", opts.NetworkEnabled)
	}
}
