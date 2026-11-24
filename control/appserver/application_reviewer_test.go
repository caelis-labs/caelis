package appserver

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/caelis-labs/caelis/control/sessionprofile"
)

func TestApplicationReviewerStateScopedCreationBindingAndCapability(t *testing.T) {
	store, _ := applicationTestStore(t)
	owner := Principal{ID: "owner"}
	foreign := Principal{ID: "other"}
	scope, _ := SessionScope(owner)
	profile := applicationTestProfile()
	profile.Reviewer = &sessionprofile.Reviewer{Kind: "guardian", Model: "guardian-model"}
	profile.Permissions.ApprovalMode = "auto-review"
	if err := store.PutBinding(t.Context(), sessionprofile.Binding{Scope: scope, SessionID: "owned", Profile: profile, CreationDigest: "digest"}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	service := &SessionProfileService{config: SessionProfileServiceConfig{Store: store, ReviewerState: func(_ context.Context, got sessionprofile.Profile) (sessionprofile.ReviewerState, error) {
		calls++
		if got.Reviewer == nil || got.Reviewer.Model != "guardian-model" {
			t.Fatal("read must use creation-bound reviewer, not another profile")
		}
		return sessionprofile.ReviewerState{ApprovalMode: "auto-review", Reviewer: got.Reviewer, Status: "ready"}, nil
	}}}
	if !slices.Contains(service.Capabilities(), sessionprofile.CapabilityGuardianReview) {
		t.Fatal("bound reviewer not advertised")
	}
	for _, principal := range []Principal{foreign, {}} {
		if _, err := service.SessionReviewerState(t.Context(), principal, "owned"); err == nil {
			t.Fatal("cross-scope reviewer state disclosed")
		}
	}
	if calls != 0 {
		t.Fatal("unauthorized request called reviewer")
	}
	state, err := service.SessionReviewerState(t.Context(), owner, "owned")
	if err != nil || state.SessionID != "owned" || state.Status != "ready" || calls != 1 {
		t.Fatalf("reviewer state: %+v, %v; calls %d", state, err, calls)
	}
	service.config.ReviewerState = nil
	if slices.Contains(service.Capabilities(), sessionprofile.CapabilityGuardianReview) {
		t.Fatal("unbound reviewer advertised")
	}
	if _, err := service.SessionReviewerState(t.Context(), owner, "owned"); !errors.Is(err, sessionprofile.ErrUnsupported) {
		t.Fatalf("unbound reviewer state: %v", err)
	}

}
