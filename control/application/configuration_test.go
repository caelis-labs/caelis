package application

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func pointer[T any](v T) *T { return &v }

func admittedRequest(revision uint64, profile Profile, id, turn string) RequestConfiguration {
	return RequestConfiguration{Revision: revision, RequestID: id, TurnID: turn,
		Model: profile.Model, ReasoningEffort: profile.ReasoningEffort,
		ServiceTier: profile.ServiceTier, ToolsVersion: profile.ToolsVersion}
}

func TestConfigurationCASReceiptAndAdmission(t *testing.T) {
	s, path := testStore(t)
	owner := testConnection(t, s, 1)
	other := testConnection(t, s, 2)
	binding := testBinding(t, s, owner, "session")
	initial, err := s.Configuration(t.Context(), owner.Scope, binding.SessionID)
	if err != nil || initial.Revision != 1 || !reflect.DeepEqual(initial.Profile, binding.Profile) {
		t.Fatalf("creation config %+v %v", initial, err)
	}
	_, err = s.Configuration(t.Context(), other.Scope, binding.SessionID)
	assertError(t, err, ErrNotFound)
	request := UpdateConfigurationRequest{OperationID: "update", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Instructions: pointer("new instructions"), ReasoningEffort: pointer("high"), ServiceTier: pointer("fast")}}
	updated, err := s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, request, nil)
	if err != nil || updated.Revision != 2 || updated.Profile.Instructions != "new instructions" || updated.Profile.ReasoningEffort != "high" || updated.Profile.ServiceTier != "fast" {
		t.Fatalf("update %+v %v", updated, err)
	}
	if err := s.AdmitRequest(t.Context(), owner.Scope, binding.SessionID, admittedRequest(1, initial.Profile, "stale", "turn")); !errors.Is(err, ErrConfigurationStale) {
		t.Fatalf("stale admission: %v", err)
	}
	admission := admittedRequest(2, updated.Profile, "request", "turn")
	invalid := admission
	invalid.Model = ""
	assertError(t, s.AdmitRequest(t.Context(), owner.Scope, binding.SessionID, invalid), ErrInvalid)
	invalid = admission
	invalid.ToolsVersion = "another-catalog"
	assertError(t, s.AdmitRequest(t.Context(), owner.Scope, binding.SessionID, invalid), ErrConflict)
	if err := s.AdmitRequest(t.Context(), owner.Scope, binding.SessionID, admission); err != nil {
		t.Fatal(err)
	}
	latest, err := s.Configuration(t.Context(), owner.Scope, binding.SessionID)
	if err != nil || latest.LastRequest == nil || *latest.LastRequest != admission {
		t.Fatalf("admission %+v %v", latest, err)
	}
	replay, err := s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, request, nil)
	if err != nil || !reflect.DeepEqual(replay, updated) {
		t.Fatalf("retry returned mutated latest %+v %v", replay, err)
	}
	replay, err = s.ConfigurationOperation(t.Context(), owner.Scope, request.OperationID)
	if err != nil || !reflect.DeepEqual(replay, updated) {
		t.Fatalf("operation receipt %+v %v", replay, err)
	}
	_, err = s.ConfigurationOperation(t.Context(), other.Scope, request.OperationID)
	assertError(t, err, ErrNotFound)
	changed := request
	changed.Patch.Instructions = pointer("changed")
	_, err = s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, changed, nil)
	assertError(t, err, ErrConflict)
	_, err = s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "stale", ExpectedConfigurationRevision: 1}, nil)
	assertError(t, err, ErrConfigurationStale)
	noOp := UpdateConfigurationRequest{OperationID: "noop", ExpectedConfigurationRevision: 2}
	same, err := s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, noOp, nil)
	if err != nil || !reflect.DeepEqual(same, latest) {
		t.Fatalf("no-op %+v %v", same, err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	got, err := reopened.ConfigurationOperation(t.Context(), owner.Scope, "noop")
	if err != nil || !reflect.DeepEqual(got, same) {
		t.Fatalf("no-op restart %+v %v", got, err)
	}
	got, err = reopened.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, request, nil)
	if err != nil || !reflect.DeepEqual(got, updated) {
		t.Fatalf("retry restart %+v %v", got, err)
	}
	gotBinding, err := reopened.GetBinding(t.Context(), owner.Scope, binding.SessionID)
	if err != nil || !reflect.DeepEqual(gotBinding.Profile, initial.Profile) {
		t.Fatalf("creation binding mutated %+v %v", gotBinding, err)
	}
}

func TestConfigurationPatchDefaultsAndCatalogVersion(t *testing.T) {
	s, _ := testStore(t)
	owner := testConnection(t, s, 1)
	binding := testBinding(t, s, owner, "session")
	for _, payload := range []string{`{"model":null}`, `{"tools":null}`, `{"native_tools":null}`, `{"instructions":null}`, `{"service_tier":null}`, `{"unknown":"value"}`} {
		var patch ConfigurationPatch
		if err := json.Unmarshal([]byte(payload), &patch); err == nil {
			t.Fatalf("accepted invalid patch %s", payload)
		}
	}
	var unsafeProfile Profile
	if err := json.Unmarshal([]byte(`{"native_tools":null}`), &unsafeProfile); err == nil {
		t.Fatal("creation null native_tools implicitly granted the default set")
	}
	_, err := s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "invalid-model", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Model: pointer("")}}, nil)
	assertError(t, err, ErrInvalid)
	_, err = s.ConfigurationOperation(t.Context(), owner.Scope, "invalid-model")
	assertError(t, err, ErrNotFound)
	toolList := []ToolDefinition{}
	nativeList := []string{}
	req := UpdateConfigurationRequest{OperationID: "clear", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{ReasoningEffort: pointer(""), ServiceTier: pointer(""), Tools: &toolList, NativeTools: &nativeList}}
	_, err = s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, req, nil)
	assertError(t, err, ErrConflict) // Changed catalog cannot reuse its version.
	req.OperationID = "clear-new-version"
	req.Patch.ToolsVersion = pointer("tools-v2")
	updated, err := s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, req, nil)
	if err != nil || updated.Revision != 2 || len(updated.Profile.Tools) != 0 || updated.Profile.NativeTools == nil {
		t.Fatalf("clear %+v %v", updated, err)
	}
	raw, err := json.Marshal(updated.Profile)
	if err != nil || !json.Valid(raw) || !containsJSONList(raw, "native_tools") || containsJSONList(raw, "tools") {
		t.Fatalf("empty list semantics %s %v", raw, err)
	}
	var parsed Profile
	if err = json.Unmarshal(raw, &parsed); err != nil || len(parsed.Tools) != 0 || parsed.NativeTools == nil {
		t.Fatalf("roundtrip lists %+v %v", parsed, err)
	}
	same, err := s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "already-empty", ExpectedConfigurationRevision: 2, Patch: ConfigurationPatch{Tools: &toolList}}, nil)
	if err != nil || same.Revision != 2 {
		t.Fatalf("empty callback no-op %+v %v", same, err)
	}
	_, err = s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "reuse", ExpectedConfigurationRevision: 2, Patch: ConfigurationPatch{ToolsVersion: pointer("tools-v1")}}, nil)
	assertError(t, err, ErrConflict)
	_, err = s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "invalid-list", ExpectedConfigurationRevision: 2, Patch: ConfigurationPatch{NativeTools: pointer[[]string](nil)}}, nil)
	assertError(t, err, ErrInvalid)
}

func containsJSONList(raw []byte, name string) bool {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	return string(fields[name]) == "[]"
}

func TestConfigurationHistoricalCallbackCatalog(t *testing.T) {
	s, _ := testStore(t)
	owner := testConnection(t, s, 1)
	binding := testBinding(t, s, owner, "session")
	prior, err := s.Configuration(t.Context(), owner.Scope, binding.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	oldTools, err := s.ToolsForConfiguration(t.Context(), binding, prior, Source{Kind: "user", OperationID: "old"})
	if err != nil || len(oldTools) != 1 {
		t.Fatalf("old tools %d %v", len(oldTools), err)
	}
	newDefs := []ToolDefinition{{Name: "WriteNote", Description: "Updated schema", InputSchema: map[string]any{"type": "object", "properties": map[string]any{"entry": map[string]any{"type": "integer"}}, "required": []any{"entry"}, "additionalProperties": false}}}
	updated, err := s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "catalog", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{ToolsVersion: pointer("tools-v2"), Tools: &newDefs}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	newTools, err := s.ToolsForConfiguration(t.Context(), binding, updated, Source{Kind: "user", OperationID: "new"})
	if err != nil || len(newTools) != 1 {
		t.Fatalf("new tools %d %v", len(newTools), err)
	}
	oldContext := testCall(binding)
	oldContext.ConfigurationRevision = 1
	oldID, err := s.enqueue(t.Context(), oldContext, "WriteNote", json.RawMessage(`{"note":"hello"}`))
	if err != nil {
		t.Fatal(err)
	}
	newContext := oldContext
	newContext.ConfigurationRevision = 2
	newContext.ToolsVersion = "tools-v2"
	newContext.ItemID = "item-2"
	newID, err := s.enqueue(t.Context(), newContext, "WriteNote", json.RawMessage(`{"entry":2}`))
	if err != nil || oldID == newID {
		t.Fatalf("new catalog was misrouted: %s %s %v", oldID, newID, err)
	}
	_, err = s.enqueue(t.Context(), newContext, "WriteNote", json.RawMessage(`{"note":"hello"}`))
	assertError(t, err, ErrInvalid)
	_, err = s.enqueue(t.Context(), oldContext, "WriteNote", json.RawMessage(`{"entry":2}`))
	assertError(t, err, ErrInvalid)
	oldReceipt, err := s.ClaimCall(t.Context(), owner.Scope, binding.SessionID, oldID)
	if err != nil || oldReceipt.ConfigurationRevision != 1 || oldReceipt.ToolsVersion != prior.Profile.ToolsVersion {
		t.Fatalf("old pinned receipt %+v %v", oldReceipt, err)
	}
	_, err = s.ClaimCall(t.Context(), owner.Scope, binding.SessionID, oldID)
	assertError(t, err, ErrAlreadyClaimed)
	newReceipt, err := s.ClaimCall(t.Context(), owner.Scope, binding.SessionID, newID)
	if err != nil || newReceipt.ConfigurationRevision != 2 || newReceipt.ToolsVersion != updated.Profile.ToolsVersion {
		t.Fatalf("new pinned receipt %+v %v", newReceipt, err)
	}
	if err = s.CompleteCall(t.Context(), owner.Scope, binding.SessionID, oldID, testResult()); err != nil {
		t.Fatal(err)
	}
	if err = s.CompleteCall(t.Context(), owner.Scope, binding.SessionID, newID, testResult()); err != nil {
		t.Fatal(err)
	}
	pinned, err := s.ToolsForConfiguration(t.Context(), binding, prior, Source{Kind: "user", OperationID: "late"})
	if err != nil || len(pinned) != 1 {
		t.Fatalf("historical callbacks expired on update %d %v", len(pinned), err)
	}
	forged := prior
	forged.Profile.ToolsVersion = "tools-v2"
	_, err = s.ToolsForConfiguration(t.Context(), binding, forged, Source{Kind: "user", OperationID: "forged"})
	assertError(t, err, ErrConflict)
}

func TestConfigurationCASDoesNotBlockAdmissionDuringValidation(t *testing.T) {
	s, _ := testStore(t)
	owner := testConnection(t, s, 1)
	binding := testBinding(t, s, owner, "session")
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	var stale error
	go func() {
		defer wg.Done()
		_, stale = s.UpdateConfiguration(context.Background(), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "first", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Instructions: pointer("first")}}, func(context.Context, Profile) error {
			close(entered)
			<-proceed
			return nil
		})
	}()
	<-entered
	if err := s.AdmitRequest(t.Context(), owner.Scope, binding.SessionID, admittedRequest(1, binding.Profile, "request", "turn")); err != nil {
		t.Fatal(err)
	}
	second, err := s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "second", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Instructions: pointer("second")}}, nil)
	if err != nil || second.LastRequest == nil || second.Revision != 2 {
		t.Fatalf("admission lost during validation %+v %v", second, err)
	}
	close(proceed)
	wg.Wait()
	assertError(t, stale, ErrConfigurationStale)
}

func TestConfigurationUpdateRejectsNativeCallbackCollision(t *testing.T) {
	s, _ := testStore(t)
	owner := testConnection(t, s, 1)
	profile := testProfile()
	profile.Execution = "workspace-write"
	binding := Binding{Scope: owner.Scope, SessionID: "native", CreationDigest: "created", Profile: profile}
	if err := s.PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	colliding := []ToolDefinition{{Name: "RunCommand", InputSchema: map[string]any{"type": "object"}}}
	_, err := s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, UpdateConfigurationRequest{
		OperationID: "collide", ExpectedConfigurationRevision: 1,
		Patch: ConfigurationPatch{ToolsVersion: pointer("tools-v2"), Tools: &colliding},
	}, nil)
	assertError(t, err, ErrInvalid)
	_, err = s.ConfigurationOperation(t.Context(), owner.Scope, "collide")
	assertError(t, err, ErrNotFound)
}

func TestConfigurationSelectedNativeNamesAndExactCallbackIdentity(t *testing.T) {
	profile := testProfile()
	profile.Execution = "workspace-write"
	profile.Tools[0].Name = "Read"
	assertError(t, ValidateProfile(profile), ErrInvalid) // fresh nil means the full native set
	profile.NativeTools = []string{"RunCommand", "Task"}
	if err := ValidateProfile(profile); err != nil {
		t.Fatalf("deselected Read should remain a valid application callback: %v", err)
	}
	profile.Tools = append(profile.Tools, ToolDefinition{Name: "read", InputSchema: map[string]any{"type": "object"}})
	if err := ValidateProfile(profile); err != nil {
		t.Fatalf("exact-case SDK callback identity changed: %v", err)
	}
	profile.Tools[1].Name = "Read"
	assertError(t, ValidateProfile(profile), ErrInvalid)
	profile.Tools[1].Name = "ReadResource"
	assertError(t, ValidateProfile(profile), ErrInvalid) // resource bridge always present
	profile.Tools = profile.Tools[:1]
	profile.NativeTools = []string{"Read"}
	assertError(t, ValidateProfile(profile), ErrInvalid)
	profile.NativeTools = []string{}
	if err := ValidateProfile(profile); err != nil {
		t.Fatalf("explicitly empty native selection still reserved Read: %v", err)
	}
}

func TestConfigurationCreationOperationRetainsSchemaOneDigest(t *testing.T) {
	s, _ := testStore(t)
	owner := testConnection(t, s, 1)
	// This is the complete pre-upgrade appserver creation request: WriteBase
	// exposes only operation_id, followed by the exact old Profile field order.
	const legacy = `{"operation_id":"create-v1","profile":{"version":"v1","instructions":"legacy","model":"test/model","tools_version":"tools-v1","execution":"tools-only","inherit":{"cwd_instructions":false,"mcp":false,"skills":false,"workspace_memory":false}}}`
	old, fresh, err := s.BeginOperation(t.Context(), owner.Scope, "create-v1", json.RawMessage(legacy))
	if err != nil || !fresh || string(old.Request) != legacy {
		t.Fatalf("legacy creation intent %+v %v %v", old, fresh, err)
	}
	// Embed the same fields as appserver.CreateApplicationSessionRequest,
	// without importing appserver back into its Store dependency.
	current := struct {
		OperationID string  `json:"operation_id"`
		Profile     Profile `json:"profile"`
	}{OperationID: "create-v1", Profile: Profile{Version: "v1", Instructions: "legacy", Model: "test/model", ToolsVersion: "tools-v1", Execution: "tools-only"}}
	encoded, err := json.Marshal(current)
	if err != nil || string(encoded) != legacy {
		t.Fatalf("new serializer changed old creation digest: %s, want %s (%v)", encoded, legacy, err)
	}
	retry, fresh, err := s.BeginOperation(t.Context(), owner.Scope, "create-v1", current)
	if err != nil || fresh || !reflect.DeepEqual(retry, old) {
		t.Fatalf("old public creation retry conflicted: %+v %v %v", retry, fresh, err)
	}
}

func TestConfigurationMigratesSchemaOneNativeCatalogAndReadCallback(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := testConnection(t, s, 1)
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	// A schema-one workspace-write Session allowed a callback named Read:
	// its native execution catalog contained RunCommand/Task, not Read.
	const profile = `{"version":"v1","instructions":"legacy","model":"test/model","tools_version":"tools-v1","tools":[{"name":"Read","description":"Read application note","input_schema":{"type":"object"}}],"execution":"workspace-write","inherit":{"cwd_instructions":false,"mcp":false,"skills":false,"workspace_memory":false}}`
	bindingRaw := fmt.Sprintf(`{"principal_id":%q,"application_id":%q,"connection_id":%q,"session_id":"legacy-native","profile":%s,"creation_digest":"creation-legacy","archived":false}`, owner.PrincipalID, owner.ApplicationID, owner.ConnectionID, profile)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`INSERT INTO app_bindings(session,principal,application,connection,body) VALUES(?,?,?,?,?); UPDATE app_schema SET version=1`, "legacy-native", owner.PrincipalID, owner.ApplicationID, owner.ConnectionID, []byte(bindingRaw)); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	binding, err := s.GetBinding(t.Context(), owner.Scope, "legacy-native")
	if err != nil || binding.Profile.NativeTools != nil || binding.Profile.Tools[0].Name != "Read" {
		t.Fatalf("creation profile was rewritten: %+v %v", binding, err)
	}
	configuration, err := s.Configuration(t.Context(), owner.Scope, binding.SessionID)
	if err != nil || !reflect.DeepEqual(configuration.Profile.NativeTools, []string{"RunCommand", "Task"}) {
		t.Fatalf("schema-one native selection %+v %v", configuration, err)
	}
	if err = ValidateProfile(configuration.Profile); err != nil {
		t.Fatalf("pinned old Read callback is invalid: %v", err)
	}
	if err := s.PutBinding(t.Context(), binding); err != nil {
		t.Fatalf("unchanged migrated binding retry: %v", err)
	}
	callbacks, err := s.ToolsForConfiguration(t.Context(), binding, configuration, Source{Kind: "user", OperationID: "legacy-request"})
	if err != nil || len(callbacks) != 1 || callbacks[0].Definition().Name != "Read" {
		t.Fatalf("legacy callback unavailable: %d %v", len(callbacks), err)
	}
	selected := []string{"Task"}
	repaired, err := s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "select-native", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{NativeTools: &selected}}, nil)
	if err != nil || repaired.Revision != 2 || !reflect.DeepEqual(repaired.Profile.NativeTools, selected) {
		t.Fatalf("native selection update %+v %v", repaired, err)
	}
	if _, err = s.ToolsForConfiguration(t.Context(), binding, configuration, Source{Kind: "user", OperationID: "old-callback-late"}); err != nil {
		t.Fatalf("catalog update revoked historical callback: %v", err)
	}
	fresh := testProfile()
	fresh.Execution = "workspace-write"
	fresh.Tools = nil
	if err = s.PutBinding(t.Context(), Binding{Scope: owner.Scope, SessionID: "fresh-v2", CreationDigest: "fresh", Profile: fresh}); err != nil {
		t.Fatalf("fresh native profile: %v", err)
	}
	defaultConfig, err := s.Configuration(t.Context(), owner.Scope, "fresh-v2")
	if err != nil || defaultConfig.Profile.NativeTools != nil {
		t.Fatalf("fresh revision lost full native default: %+v %v", defaultConfig, err)
	}
}

func TestConfigurationMigratesSchemaOneBindings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "control.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	owner := testConnection(t, s, 1)
	binding := testBinding(t, s, owner, "legacy")
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	// Construct pre-followup v1 JSON without invoking Profile.MarshalJSON:
	// old persisted bindings had neither native_tools nor workspace/permissions.
	legacyJSON, err := json.MarshalIndent(map[string]any{
		"principal_id": binding.PrincipalID, "application_id": binding.ApplicationID,
		"connection_id": binding.ConnectionID, "session_id": binding.SessionID,
		"creation_digest": binding.CreationDigest, "archived": false,
		"profile": map[string]any{
			"version": binding.Profile.Version, "instructions": binding.Profile.Instructions,
			"model": binding.Profile.Model, "tools_version": binding.Profile.ToolsVersion,
			"tools": binding.Profile.Tools, "execution": binding.Profile.Execution,
			"inherit": binding.Profile.Inherit,
		},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(`DELETE FROM app_configurations; UPDATE app_bindings SET body=? WHERE session='legacy'; UPDATE app_schema SET version=1`, legacyJSON); err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	if err = s.PutBinding(t.Context(), binding); err != nil {
		t.Fatalf("creation retry after historical JSON migration: %v", err)
	}
	baseline, err := s.Configuration(t.Context(), owner.Scope, binding.SessionID)
	if err != nil || baseline.Revision != 1 || !reflect.DeepEqual(baseline.Profile, binding.Profile) {
		t.Fatalf("migration %+v %v", baseline, err)
	}
	old := testCall(binding)
	if _, err = s.enqueue(t.Context(), old, "WriteNote", json.RawMessage(`{"note":"old"}`)); err != nil {
		t.Fatalf("legacy callback: %v", err)
	}
}
