package local

import (
	"encoding/json"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/app/gatewayapp"
)

func TestPluginMCPRefreshStatusProjection(t *testing.T) {
	for _, flags := range [][3]bool{{true, true, false}, {false, true, true}, {false, false, false}} {
		info := mcp.MCPServerInfo{Name: "documents", Status: "running", Refreshing: flags[0], Stale: flags[1], RefreshFailed: flags[2], Warning: "catalog unavailable"}
		got := toRuntimePluginSnapshot(gatewayapp.PluginInfo{MCPServers: []mcp.MCPServerInfo{info}}).MCPServers[0]
		raw, err := json.Marshal(got)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(raw, &fields); err != nil {
			t.Fatal(err)
		}
		for i, name := range []string{"refreshing", "stale", "refresh_failed"} {
			if fields[name] != flags[i] {
				t.Errorf("%s = %v, want %v; JSON %s", name, fields[name], flags[i], raw)
			}
		}
		if got.Status != "running" || got.Warning != info.Warning {
			t.Fatalf("connection status or warning lost: %+v", got)
		}
	}
}
