package appserver

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/application"
)

func TestApplicationReviewerStateScopedCreationBindingAndCapability(t *testing.T) {
	store, _ := applicationTestStore(t)
	owner := enrollTestApplication(t, store, "owner", "review-a", "a")
	foreign := enrollTestApplication(t, store, "owner", "review-b", "b")
	scope, _ := ApplicationScope(owner)
	profile := applicationTestProfile()
	profile.Reviewer = &application.Reviewer{Kind: "guardian", Model: "guardian-model"}
	profile.Permissions.ApprovalMode = "auto-review"
	if err := store.PutBinding(t.Context(), application.Binding{Scope: scope, SessionID: "owned", Profile: profile, CreationDigest: "digest"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := &ApplicationService{config: ApplicationServiceConfig{Store: store, ReviewerState: func(_ context.Context, got application.Profile) (application.ReviewerState, error) {
		calls++
		if got.Reviewer == nil || got.Reviewer.Model != "guardian-model" {
			t.Fatal("read must use creation-bound reviewer, not another profile")
		}
		return application.ReviewerState{ApprovalMode: "auto-review", Reviewer: got.Reviewer, Status: "ready"}, nil
	}}}
	if !slices.Contains(service.Capabilities(), application.CapabilityGuardianReview) {
		t.Fatal("bound reviewer not advertised")
	}
	for _, principal := range []Principal{foreign, {ID: "owner"}, {}} {
		if _, err := service.ApplicationReviewerState(t.Context(), principal, "owned"); err == nil {
			t.Fatal("cross-scope reviewer state disclosed")
		}
	}
	if calls != 0 {
		t.Fatal("unauthorized request called reviewer")
	}
	state, err := service.ApplicationReviewerState(t.Context(), owner, "owned")
	if err != nil || state.SessionID != "owned" || state.Status != "ready" || calls != 1 {
		t.Fatalf("reviewer state: %+v, %v; calls %d", state, err, calls)
	}
	service.config.ReviewerState = nil
	if slices.Contains(service.Capabilities(), application.CapabilityGuardianReview) {
		t.Fatal("unbound reviewer advertised")
	}
	if _, err := service.ApplicationReviewerState(t.Context(), owner, "owned"); !errors.Is(err, application.ErrUnsupported) {
		t.Fatalf("unbound reviewer state: %v", err)
	}
	if _, err := service.Create(t.Context(), owner, CreateApplicationSessionRequest{WriteBase: WriteBase{OperationID: "unsupported-review"}, Profile: profile}); !errors.Is(err, application.ErrUnsupported) {
		t.Fatalf("unbound reviewer creation: %v", err)
	}
	if _, err := store.GetOperation(t.Context(), scope, "unsupported-review"); err == nil {
		t.Fatal("unsupported creation recorded an intent")
	}
	manual := applicationTestProfile()
	manual.Tools = []application.ToolDefinition{{Name: "SensitiveAction", InputSchema: map[string]any{"type": "object"}, ApprovalPolicy: "required"}}
	if _, err := service.Create(t.Context(), owner, CreateApplicationSessionRequest{WriteBase: WriteBase{OperationID: "unsupported-callback"}, Profile: manual}); !errors.Is(err, application.ErrUnsupported) {
		t.Fatalf("manual required callback creation on unsupported service: %v", err)
	}
	if _, err := store.GetOperation(t.Context(), scope, "unsupported-callback"); err == nil {
		t.Fatal("unsupported callback creation recorded an intent")
	}
	service.config.ValidateProfile = func(context.Context, application.Profile) error { return nil }
	patch := application.ConfigurationPatch{Tools: &manual.Tools}
	if _, err := service.UpdateApplicationConfiguration(t.Context(), owner, "owned", application.UpdateConfigurationRequest{OperationID: "unsupported-update", ExpectedConfigurationRevision: 1, Patch: patch}); !errors.Is(err, application.ErrUnsupported) {
		t.Fatalf("required callback configuration update on unsupported service: %v", err)
	}
}
