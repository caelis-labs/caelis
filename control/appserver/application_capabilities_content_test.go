package appserver

import (
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestApplicationContentCapabilitiesAreAdvertised(t *testing.T) {
	capabilities := (&SessionProfileService{}).Capabilities()
	for _, capability := range []string{sessionprofile.CapabilityToolResultContent, sessionprofile.CapabilityMediaResources} {
		if !slices.Contains(capabilities, capability) {
			t.Fatalf("missing application capability %q: %v", capability, capabilities)
		}
	}
}
