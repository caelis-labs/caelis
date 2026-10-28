package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver"
)

func TestCLIPluginStatusRendersMCPRefreshStatus(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		refreshing, stale, failed bool
		want                      string
	}{
		{"refreshing", true, true, false, "refreshing, stale"},
		{"failed", false, true, true, "stale, refresh failed"},
		{"ready", false, false, false, "running"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := pluginCommandOutput{Action: "status", ID: "docs", Plugin: &appserver.PluginSnapshot{Root: "/plugins/docs", Version: "1", Enabled: true, MCPServers: []appserver.MCPServerSnapshot{{Name: "documents", Status: "running", Refreshing: tc.refreshing, Stale: tc.stale, RefreshFailed: tc.failed, Warning: "catalog warning"}}}}
			var output bytes.Buffer
			if err := writePluginCommandText(&output, result); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"status docs:", "documents", "running", tc.want, "catalog warning"} {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("missing %q in CLI output: %s", want, output.String())
				}
			}
		})
	}
}
