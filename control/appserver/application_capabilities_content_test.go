package appserver

import (
	"context"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestNativePluginsCapabilityRequiresHostAssembly(t *testing.T) {
	service := &SessionProfileService{config: SessionProfileServiceConfig{ValidateProfile: func(context.Context, sessionprofile.Profile) error { return nil }, MCPStatus: func(context.Context, string, uint64) sessionprofile.MCPStatus { return sessionprofile.MCPStatus{} }}}
	if slices.Contains(service.Capabilities(), sessionprofile.CapabilityNativePlugins) {
		t.Fatal("generic MCP/Skill callbacks advertised native package assembly")
	}
	service.config.NativePlugins = true
	if !slices.Contains(service.Capabilities(), sessionprofile.CapabilityNativePlugins) {
		t.Fatal("native package Host assembly not advertised")
	}
}
