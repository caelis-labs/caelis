package application

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestApplicationReviewerValidationAndLegacyDigest(t *testing.T) {
	legacy := Profile{Version: "v1", Model: "main", ToolsVersion: "v1", Execution: "tools-only"}
	raw, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"version":"v1","instructions":"","model":"main","tools_version":"v1","execution":"tools-only","inherit":{"cwd_instructions":false,"mcp":false,"skills":false,"workspace_memory":false}}`
	if string(raw) != want || EffectiveApprovalMode(legacy) != "manual" {
		t.Fatalf("legacy creation identity/default changed: %s", raw)
	}
	for _, tc := range []struct {
		name, mode string
		reviewer   *Reviewer
		want       error
	}{
		{name: "legacy"},
		{name: "explicit manual", mode: "manual"},
		{name: "no reviewer", mode: "auto-review", want: ErrInvalid},
		{name: "explicit guardian", mode: "auto-review", reviewer: &Reviewer{Kind: "guardian", Model: "review-model"}},
		{name: "no model", mode: "auto-review", reviewer: &Reviewer{Kind: "guardian"}, want: ErrInvalid},
		{name: "unknown reviewer", mode: "auto-review", reviewer: &Reviewer{Kind: "bot", Model: "model"}, want: ErrUnsupported},
		{name: "unused reviewer", mode: "manual", reviewer: &Reviewer{Kind: "guardian", Model: "model"}, want: ErrInvalid},
		{name: "never", mode: "never", want: ErrUnsupported},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile := legacy
			profile.Permissions.ApprovalMode, profile.Reviewer = tc.mode, tc.reviewer
			if err := ValidateProfile(profile); !errors.Is(err, tc.want) {
				t.Fatalf("validation = %v; want %v", err, tc.want)
			}
		})
	}
	for _, raw := range []string{`{"reviewer":{}}`, `{"reviewer":{"model":"other"}}`, `{"permissions":{"approval_mode":"manual"}}`} {
		var patch ConfigurationPatch
		if json.Unmarshal([]byte(raw), &patch) == nil {
			t.Fatalf("creation authority accepted in hot update: %s", raw)
		}
	}
}

func TestApplicationReviewerPersistsWithoutHotModelInheritance(t *testing.T) {
	store, path := testStore(t)
	owner := testConnection(t, store, 1)
	binding := Binding{Scope: owner.Scope, SessionID: "review-session", CreationDigest: "create-digest", Profile: Profile{
		Version: "v1", Model: "main", ToolsVersion: "v1", Execution: "tools-only",
		Permissions: Permissions{ApprovalMode: "auto-review"}, Reviewer: &Reviewer{Kind: "guardian", Model: "reviewer"},
	}}
	if err := store.PutBinding(t.Context(), binding); err != nil {
		t.Fatal(err)
	}
	config, err := store.UpdateConfiguration(t.Context(), owner.Scope, binding.SessionID, UpdateConfigurationRequest{
		OperationID: "main-update", ExpectedConfigurationRevision: 1,
		Patch: ConfigurationPatch{Model: pointer("other-main")},
	}, nil)
	if err != nil || !reflect.DeepEqual(config.Profile.Reviewer, binding.Profile.Reviewer) {
		t.Fatalf("hot update changed reviewer: %+v, %v", config, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.Configuration(t.Context(), owner.Scope, binding.SessionID)
	if err != nil || !reflect.DeepEqual(got, config) {
		t.Fatalf("restart lost review policy: %+v, %v", got, err)
	}
	original, err := reopened.GetBinding(t.Context(), owner.Scope, binding.SessionID)
	if err != nil || !reflect.DeepEqual(original, binding) {
		t.Fatalf("creation authority changed: %+v, %v", original, err)
	}
}
