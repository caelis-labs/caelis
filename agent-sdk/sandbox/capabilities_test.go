package sandbox_test

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestValidateCapabilitiesRejectsMissingExecutorFeature(t *testing.T) {
	t.Parallel()

	err := sandbox.ValidateCapabilities(sandbox.Descriptor{
		Backend:      sandbox.BackendHost,
		Capabilities: sandbox.CapabilitySet{CommandExec: true},
	}, sandbox.CapabilitySet{CommandExec: true, AsyncSessions: true})
	var capabilityErr *sandbox.CapabilityError
	if !errors.As(err, &capabilityErr) {
		t.Fatalf("error = %v, want *sandbox.CapabilityError", err)
	}
	if got, want := capabilityErr.Capability, sandbox.CapabilityAsyncSessions; got != want {
		t.Fatalf("capability = %q, want %q", got, want)
	}
}
