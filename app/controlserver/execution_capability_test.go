package controlserver

import (
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
)

func TestExecutionConfigurationAdvertisedWithoutApplicationServiceButNotRequiredBaseline(t *testing.T) {
	info := applicationServerInfo(appserver.ServerInfo{}, appserver.AppServerServices{})
	if !slices.Contains(info.Capabilities, appserver.CapabilityExecutionConfiguration) {
		t.Fatal("ordinary-only HTTP Host omitted optional execution configuration capability")
	}
	if slices.Contains(appserver.RequiredManagedHostCapabilities(), appserver.CapabilityExecutionConfiguration) {
		t.Fatal("feature capability changed the required baseline")
	}
	info = applicationServerInfo(info, appserver.AppServerServices{})
	if count := len(info.Capabilities); count != len(slices.Compact(slices.Clone(info.Capabilities))) {
		t.Fatalf("Host duplicated feature capability: %+v", info.Capabilities)
	}
}
