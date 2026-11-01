package wirev1

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/appserver/wirev1/generated"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestNativePluginPublicJSONAndGeneratedClient(t *testing.T) {
	selected := []sessionprofile.Plugin{{Root: "/verified/desktop-world-0.2.0", MCPServers: []sessionprofile.MCPServer{{Name: "desktop-world", Transport: "stdio", Command: "verified-bot", Args: []string{"--plugin-mcp", "generation-1"}, WorkDir: "/verified/desktop-world-0.2.0"}}}}
	profile := sessionprofile.Profile{Version: "fixture/1", Model: "custom/fixture", ToolsVersion: "fixture-tools/1", Execution: "tools-only", Plugins: selected}
	validateWireValue(t, "SessionProfile", profile)
	validateWireValue(t, "SessionConfigurationPatch", sessionprofile.ConfigurationPatch{Plugins: &selected})
	validateWireValue(t, "SessionMCPStatus", sessionprofile.MCPStatus{SessionID: "fixture", ConfigurationRevision: "1", Servers: []sessionprofile.MCPServerStatus{{PluginID: "desktop-world", Name: "desktop-world", Status: "running"}}, Skills: []sessionprofile.SkillStatus{}, Plugins: []sessionprofile.PluginStatus{{ID: "desktop-world", Root: selected[0].Root, Version: "0.2.0", Status: "assembled"}}})
	raw, _ := json.Marshal(profile)
	var wire generated.SessionProfile
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(wire)
	var roundtrip sessionprofile.Profile
	if err := json.Unmarshal(raw, &roundtrip); err != nil || !reflect.DeepEqual(roundtrip.Plugins, selected) {
		t.Fatalf("native selection lost on generated client roundtrip: %+v %v", roundtrip.Plugins, err)
	}
	empty := []sessionprofile.Plugin{}
	raw, _ = json.Marshal(sessionprofile.ConfigurationPatch{Plugins: &empty})
	var patch generated.SessionConfigurationPatch
	if err := json.Unmarshal(raw, &patch); err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(patch)
	if string(raw) != `{"plugins":[]}` {
		t.Fatalf("generated native clear lost presence: %s", raw)
	}
}
