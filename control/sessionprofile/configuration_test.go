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

func containsJSONList(raw []byte, name string) bool {
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fields)
	return string(fields[name]) == "[]"
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
