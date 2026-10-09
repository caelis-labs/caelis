package local

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/tool/mcp"
	"github.com/caelis-labs/caelis/app/gatewayapp"
)

func TestPluginMCPToolDetailsProjection(t *testing.T) {
	info := gatewayapp.PluginInfo{MCPServers: []mcp.MCPServerInfo{{
		Name: "documents", Status: "running", Tools: []string{"lookup"},
		ToolDetails: []mcp.MCPToolDetail{{Name: "lookup", Description: "Look up a synthetic document"}},
	}}}
	snapshot := toRuntimePluginSnapshot(info)
	if len(snapshot.MCPServers) != 1 || !reflect.DeepEqual(snapshot.MCPServers[0].Tools, []string{"lookup"}) || !reflect.DeepEqual(snapshot.MCPServers[0].ToolDetails, info.MCPServers[0].ToolDetails) {
		t.Fatalf("public ordinary Session plugin snapshot lost ready metadata: %+v", snapshot.MCPServers)
	}
	raw, err := json.Marshal(snapshot.MCPServers[0])
	if err != nil {
		t.Fatal(err)
	}
	var oldClient struct {
		Tools []string `json:"tools"`
	}
	if err := json.Unmarshal(raw, &oldClient); err != nil || !reflect.DeepEqual(oldClient.Tools, []string{"lookup"}) {
		t.Fatalf("old plugin client cannot read names: %+v, %v", oldClient, err)
	}
}
