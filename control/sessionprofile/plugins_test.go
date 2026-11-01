package sessionprofile

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNativePluginsConfigurationPersistenceAndCreationEvidence(t *testing.T) {
	store, path := testStore(t)
	owner := testOwner(t, store, 1)
	binding := testBinding(t, store, owner, "native-plugins")
	before, _ := json.Marshal(binding.Profile)
	selected := []Plugin{{Root: t.TempDir(), MCPServers: []MCPServer{{Name: "desktop-world", Transport: "stdio", Command: "verified-proxy"}}}}
	selected[0].MCPServers[0].WorkDir = selected[0].Root
	enabled, err := store.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "select-native", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Plugins: &selected}}, nil)
	if err != nil || enabled.Revision != 2 || !reflect.DeepEqual(enabled.Profile.Plugins, selected) {
		t.Fatalf("native selection lost: %+v %v", enabled, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	recovered, err := restarted.Configuration(t.Context(), owner.Scope, binding.SessionID)
	if err != nil || !reflect.DeepEqual(recovered.Profile.Plugins, selected) {
		t.Fatalf("native selection did not survive recovery: %+v %v", recovered, err)
	}
	original, err := restarted.GetBinding(t.Context(), owner.Scope, binding.SessionID)
	after, _ := json.Marshal(original.Profile)
	if err != nil || string(before) != string(after) || len(original.Profile.Plugins) != 0 {
		t.Fatal("creation evidence mutated")
	}
	empty := []Plugin{}
	cleared, err := restarted.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "clear-native", ExpectedConfigurationRevision: 2, Patch: ConfigurationPatch{Plugins: &empty}}, nil)
	if err != nil || cleared.Revision != 3 || len(cleared.Profile.Plugins) != 0 {
		t.Fatalf("cannot clear native selection: %+v %v", cleared, err)
	}
}

func TestNativePluginsRejectNullAndAuthorityClaims(t *testing.T) {
	for _, raw := range []string{`{"plugins":null}`, `{"plugins":[null]}`, `{"plugins":[{"root":null}]}`, `{"plugins":[{"root":"/tmp/package","source_trusted":true}]}`, `{"plugins":[{"root":"/tmp/package","mcp_servers":null}]}`} {
		var patch ConfigurationPatch
		if err := json.Unmarshal([]byte(raw), &patch); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
}
