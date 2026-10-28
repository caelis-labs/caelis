package sessionprofile

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

func pointer[T any](v T) *T { return &v }

func admittedRequest(revision uint64, profile Profile, id, turn string) RequestConfiguration {
	return RequestConfiguration{Revision: revision, RequestID: id, TurnID: turn,
		Model: profile.Model, ReasoningEffort: profile.ReasoningEffort,
		ServiceTier: profile.ServiceTier, ToolsVersion: profile.ToolsVersion}
}

func TestConfigurationPatchDefaultsAndCatalogVersion(t *testing.T) {
	s, _ := testStore(t)
	owner := testOwner(t, s, 1)
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
	_, err := s.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "invalid-model", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Model: pointer("")}}, nil)
	assertError(t, err, ErrInvalid)
	// Rejected patch left the desired snapshot unchanged.
	if current, e := s.Configuration(t.Context(), owner.Scope, binding.SessionID); e != nil || current.Revision != 1 {
		t.Fatalf("invalid patch mutated state: %+v %v", current, e)
	}
	toolList := []ToolDefinition{}
	nativeList := []string{}
	req := UpdateConfigurationRequest{OperationID: "clear", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{ReasoningEffort: pointer(""), ServiceTier: pointer(""), Tools: &toolList, NativeTools: &nativeList}}
	_, err = s.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, req, nil)
	assertError(t, err, ErrConflict) // Changed catalog cannot reuse its version.
	req.OperationID = "clear-new-version"
	req.Patch.ToolsVersion = pointer("tools-v2")
	updated, err := s.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, req, nil)
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
	same, err := s.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "already-empty", ExpectedConfigurationRevision: 2, Patch: ConfigurationPatch{Tools: &toolList}}, nil)
	if err != nil || same.Revision != 2 {
		t.Fatalf("empty callback no-op %+v %v", same, err)
	}
	_, err = s.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "reuse", ExpectedConfigurationRevision: 2, Patch: ConfigurationPatch{ToolsVersion: pointer("tools-v1")}}, nil)
	assertError(t, err, ErrConflict)
	_, err = s.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "invalid-list", ExpectedConfigurationRevision: 2, Patch: ConfigurationPatch{NativeTools: pointer[[]string](nil)}}, nil)
	assertError(t, err, ErrInvalid)
}

func containsJSONList(raw []byte, name string) bool {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	return string(fields[name]) == "[]"
}

func TestConfigurationHistoricalCallbackCatalog(t *testing.T) {
	s, _ := testStore(t)
	owner := testOwner(t, s, 1)
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
	updated, err := s.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "catalog", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{ToolsVersion: pointer("tools-v2"), Tools: &newDefs}}, nil)
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
	owner := testOwner(t, s, 1)
	binding := testBinding(t, s, owner, "session")
	entered := make(chan struct{})
	proceed := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	var stale error
	go func() {
		defer wg.Done()
		_, stale = s.UpdateConfiguration(mutationContext(context.Background()), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "first", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Instructions: pointer("first")}}, func(context.Context, Profile) error {
			close(entered)
			<-proceed
			return nil
		})
	}()
	<-entered
	if err := s.AdmitRequest(t.Context(), owner.Scope, binding.SessionID, admittedRequest(1, binding.Profile, "request", "turn")); err != nil {
		t.Fatal(err)
	}
	second, err := s.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, UpdateConfigurationRequest{OperationID: "second", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Instructions: pointer("second")}}, nil)
	if err != nil || second.LastRequest == nil || second.Revision != 2 {
		t.Fatalf("admission lost during validation %+v %v", second, err)
	}
	close(proceed)
	wg.Wait()
	assertError(t, stale, ErrConfigurationStale)
}

func TestConfigurationUpdateRejectsNativeCallbackCollision(t *testing.T) {
	s, _ := testStore(t)
	owner := testOwner(t, s, 1)
	profile := testProfile()
	profile.Execution = "workspace-write"
	binding := Binding{Scope: owner.Scope, SessionID: "native", CreationDigest: "created", Profile: profile}
	if err := s.PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	colliding := []ToolDefinition{{Name: "RunCommand", InputSchema: map[string]any{"type": "object"}}}
	_, err := s.UpdateConfiguration(mutationContext(t.Context()), owner.Scope, binding.SessionID, UpdateConfigurationRequest{
		OperationID: "collide", ExpectedConfigurationRevision: 1,
		Patch: ConfigurationPatch{ToolsVersion: pointer("tools-v2"), Tools: &colliding},
	}, nil)
	assertError(t, err, ErrInvalid)
	if current, e := s.Configuration(t.Context(), owner.Scope, binding.SessionID); e != nil || current.Revision != 1 {
		t.Fatalf("collision changed configuration: %+v %v", current, e)
	}
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

func TestIdleRequestAdmissionUsesJobWithoutCompletedTurn(t *testing.T) {
	store, _ := testStore(t)
	owner := testOwner(t, store, 1)
	binding := testBinding(t, store, owner, "idle-session")
	req := admittedRequest(1, binding.Profile, "idle-request", "")
	req.JobID = "idle-job"
	if err := store.AdmitRequest(t.Context(), owner.Scope, binding.SessionID, req); err != nil {
		t.Fatal(err)
	}
	current, err := store.Configuration(t.Context(), owner.Scope, binding.SessionID)
	if err != nil || current.LastRequest == nil || current.LastRequest.JobID != "idle-job" || current.LastRequest.TurnID != "" {
		t.Fatalf("attribution=%+v %v", current.LastRequest, err)
	}
	req.TurnID = "completed-turn"
	assertError(t, store.AdmitRequest(t.Context(), owner.Scope, binding.SessionID, req), ErrInvalid)
	req.TurnID = ""
	req.JobID = ""
	assertError(t, store.AdmitRequest(t.Context(), owner.Scope, binding.SessionID, req), ErrInvalid)
}
