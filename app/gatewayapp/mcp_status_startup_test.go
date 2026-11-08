package gatewayapp

import (
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/control/plugin"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestConfiguredMCPStartupFailureRemainsQueryableWithoutPluginSource(t *testing.T) {
	specs := []mcp.ServerSpec{{Name: "context7", PluginID: "user", Command: "/missing/private-token", WorkDir: t.TempDir()}}
	manager, err := mcp.NewManager(t.Context(), specs, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	<-manager.Initialized()
	status := nativePluginStatus(&sessionprofile.Configuration{Revision: 7}, plugin.Contributions{}, manager, specs)(7)
	if len(status.Servers) != 1 || status.Servers[0].Name != "context7" || status.Servers[0].Status != "failed" || status.Servers[0].Warning != "transport: failed" {
		t.Fatalf("status=%#v", status)
	}
	if strings.Contains(status.Servers[0].Warning, "private") || len(status.Plugins) != 0 {
		t.Fatal("private cause or invented plugin Source")
	}
	if got := nativePluginStatus(&sessionprofile.Configuration{Revision: 7}, plugin.Contributions{}, manager, specs)(8); len(got.Servers) != 0 {
		t.Fatal("old assembly crossed revision")
	}
}
