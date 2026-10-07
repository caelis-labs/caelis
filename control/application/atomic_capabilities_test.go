package application

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
)

func TestAtomicCapabilitiesConfigurationAndCollision(t *testing.T) {
	store, path := testStore(t)
	owner := testConnection(t, store, 1)
	binding := testBinding(t, store, owner, "atomic")
	base, err := store.Configuration(t.Context(), owner.Scope, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	services := []MCPServer{{Name: "documents", Transport: "stdio", Command: "synthetic-server", WorkDir: t.TempDir()}}
	dirs := []string{t.TempDir()}
	roots := []string{filepath.Join(t.TempDir(), "one")}
	update := UpdateConfigurationRequest{OperationID: "enable-atomic", ExpectedConfigurationRevision: base.Revision,
		Patch: ConfigurationPatch{MCPServers: &services, SkillDirs: &dirs, SkillRoots: &roots}}
	enabled, err := store.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, update, nil)
	if err != nil || enabled.Revision != 2 || !reflect.DeepEqual(enabled.Profile.MCPServers, services) || !reflect.DeepEqual(enabled.Profile.SkillDirs, dirs) || !reflect.DeepEqual(enabled.Profile.SkillRoots, roots) {
		t.Fatalf("enabled = %+v, %v", enabled, err)
	}
	if err := store.AdmitRequest(t.Context(), owner.Scope, binding.SessionID, admittedRequest(base.Revision, base.Profile, "old", "turn")); !errors.Is(err, ErrConfigurationStale) {
		t.Fatalf("old request admission = %v", err)
	}
	if err := store.AdmitRequest(t.Context(), owner.Scope, binding.SessionID, admittedRequest(enabled.Revision, enabled.Profile, "new", "turn")); err != nil {
		t.Fatal(err)
	}
	for _, payload := range []string{`{"mcp_servers":null}`, `{"skill_dirs":null}`, `{"skill_roots":null}`} {
		var patch ConfigurationPatch
		if err := json.Unmarshal([]byte(payload), &patch); err == nil {
			t.Fatalf("accepted null patch %s", payload)
		}
	}
	for _, payload := range []string{`{"mcp_servers":null}`, `{"skill_dirs":null}`, `{"skill_roots":null}`, `{"mcp_servers":[{"name":"docs","transport":"stdio","command":"x","work_dir":"/tmp","headers":{"Authorization":"secret"}}]}`} {
		var profile Profile
		if err := json.Unmarshal([]byte(payload), &profile); err == nil {
			t.Fatalf("accepted invalid creation profile %s", payload)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, err := reopened.ConfigurationOperation(t.Context(), owner.Scope, update.OperationID)
	if err != nil || !reflect.DeepEqual(recovered, enabled) {
		t.Fatalf("recovered operation = %+v, %v", recovered, err)
	}
	emptyServices := []MCPServer{}
	emptyPaths := []string{}
	disabled, err := reopened.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, UpdateConfigurationRequest{
		OperationID: "disable-atomic", ExpectedConfigurationRevision: enabled.Revision,
		Patch: ConfigurationPatch{MCPServers: &emptyServices, SkillDirs: &emptyPaths, SkillRoots: &emptyPaths},
	}, nil)
	if err != nil || disabled.Revision != 3 || len(disabled.Profile.MCPServers)+len(disabled.Profile.SkillDirs)+len(disabled.Profile.SkillRoots) != 0 {
		t.Fatalf("disabled = %+v, %v", disabled, err)
	}
	if binding.Profile.MCPServers != nil || binding.Profile.SkillDirs != nil {
		t.Fatal("creation binding mutated")
	}

	for _, name := range []string{"Skill", "ToolSearch", "documents__lookup"} {
		profile := enabled.Profile
		profile.Tools = []ToolDefinition{{Name: name, InputSchema: map[string]any{"type": "object"}}}
		if err := ValidateProfile(profile); !errors.Is(err, ErrInvalid) {
			t.Fatalf("collision %q = %v", name, err)
		}
	}
	profile := enabled.Profile
	profile.MCPServers = append(profile.MCPServers, services[0])
	if err := ValidateProfile(profile); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate MCP service = %v", err)
	}
}
