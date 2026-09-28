package appserver

import (
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/application"
)

func TestApplicationContentCapabilitiesAreAdvertised(t *testing.T) {
	capabilities := (&ApplicationService{}).Capabilities()
	for _, capability := range []string{application.CapabilityToolResultContent, application.CapabilityMediaResources} {
		if !slices.Contains(capabilities, capability) {
			t.Fatalf("missing application capability %q: %v", capability, capabilities)
		}
	}
}
