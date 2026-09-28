package application

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/sandbox"
)

func TestExecutionConfigurationCreationDefaultsAndValidation(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"execution_config":null}`,
		`{"execution_config":{}}`,
		`{"execution_config":{"environment":null,"shell":null}}`,
		`{"execution_config":{"environment":{"inherit":null,"set":null,"unset":null}}}`,
		`{"execution_config":{"environment":{"set":{},"unset":[]}}}`,
	} {
		var p Profile
		if err := json.Unmarshal([]byte(raw), &p); err != nil {
			t.Fatalf("default shape rejected: %s: %v", raw, err)
		}
		p.Version, p.Model, p.ToolsVersion, p.Execution = "v1", "test/model", "v1", "workspace-write"
		if err := ValidateProfile(p); err != nil {
			t.Fatalf("default configuration rejected: %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"execution_config":{"extra":true}}`,
		`{"execution_config":{"environment":{"extra":true}}}`,
		`{"execution_config":{"shell":{"extra":true}}}`,
	} {
		var p Profile
		if err := json.Unmarshal([]byte(raw), &p); err == nil {
			t.Fatal("accepted unknown execution configuration")
		}
	}
	p := testProfile()
	p.ExecutionConfig = &sandbox.ExecutionConfig{}
	assertError(t, ValidateProfile(p), ErrInvalid)
	p.Execution = "workspace-write"
	p.ExecutionConfig.Environment.Set = map[string]string{"INVALID=NAME": "synthetic-secret"}
	err := ValidateProfile(p)
	assertError(t, err, ErrInvalid)
	if strings.Contains(err.Error(), "synthetic-secret") {
		t.Fatal("validation disclosed an environment value")
	}
	for _, raw := range []string{`{"execution_config":{}}`, `{"execution_config":null}`} {
		var patch ConfigurationPatch
		if err := json.Unmarshal([]byte(raw), &patch); err == nil {
			t.Fatal("hot update admitted creation-bound execution configuration")
		}
	}
}

func TestExecutionConfigurationSurvivesUpdatesAndStoreRestart(t *testing.T) {
	s, path := testStore(t)
	owner := testConnection(t, s, 1)
	profile := testProfile()
	profile.Execution = "workspace-write"
	profile.ExecutionConfig = &sandbox.ExecutionConfig{Environment: sandbox.EnvironmentConfig{
		Inherit: pointer(false), Set: map[string]string{"HOME": "/synthetic/home", "EMPTY": ""}, Unset: []string{"DROP"},
	}}
	binding := Binding{Scope: owner.Scope, SessionID: "configured-session", Profile: profile, CreationDigest: "configured-digest"}
	if err := s.PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	request := UpdateConfigurationRequest{OperationID: "change-instructions", ExpectedConfigurationRevision: 1, Patch: ConfigurationPatch{Instructions: pointer("updated")}}
	updated, err := s.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, request, nil)
	if err != nil || !reflect.DeepEqual(updated.Profile.ExecutionConfig, profile.ExecutionConfig) {
		t.Fatalf("hot update changed execution configuration: %v", err)
	}
	// Readback values are detached from durable state.
	updated.Profile.ExecutionConfig.Environment.Set["HOME"] = "mutated-client-copy"
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.GetBinding(t.Context(), owner.Scope, binding.SessionID)
	if err != nil || !reflect.DeepEqual(got.Profile.ExecutionConfig, profile.ExecutionConfig) {
		t.Fatalf("restart lost immutable process configuration: %v", err)
	}
	configuration, err := reopened.Configuration(t.Context(), owner.Scope, binding.SessionID)
	if err != nil || !reflect.DeepEqual(configuration.Profile.ExecutionConfig, profile.ExecutionConfig) {
		t.Fatalf("restart lost current process configuration: %v", err)
	}
	receipt, err := reopened.ConfigurationOperation(t.Context(), owner.Scope, request.OperationID)
	if err != nil || !reflect.DeepEqual(receipt.Profile.ExecutionConfig, profile.ExecutionConfig) {
		t.Fatalf("restart lost receipt process configuration: %v", err)
	}
	if err := reopened.PutBinding(t.Context(), binding); err != nil {
		t.Fatalf("unchanged creation retry failed: %v", err)
	}
	binding.Profile.ExecutionConfig.Environment.Set["HOME"] = "different"
	assertError(t, reopened.PutBinding(t.Context(), binding), ErrConflict)
}
