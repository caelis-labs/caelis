package kernel

import (
	"context"
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type fixedApprovalModeResolver struct {
	staticResolver
	mode ApprovalMode
	err  error
}

func (r fixedApprovalModeResolver) ResolveApprovalMode(context.Context, session.SessionRef) (ApprovalMode, error) {
	return r.mode, r.err
}

func TestCreationBoundApprovalModeDoesNotUseOrdinarySessionOverrides(t *testing.T) {
	for _, mode := range []ApprovalMode{ApprovalModeManual, ApprovalModeAutoReview} {
		gateway := &Gateway{resolver: fixedApprovalModeResolver{mode: mode}}
		got, err := gateway.currentApprovalMode(t.Context(), session.SessionRef{SessionID: "application"})
		if err != nil || got != mode {
			t.Fatalf("creation-bound mode = %s, %v; want %s", got, err, mode)
		}
	}
	for _, resolver := range []fixedApprovalModeResolver{{mode: "never"}, {mode: ApprovalModeManual, err: errors.New("unavailable binding")}} {
		gateway := &Gateway{resolver: resolver, defaultApprovalMode: ApprovalModeAutoReview}
		if _, err := gateway.currentApprovalMode(t.Context(), session.SessionRef{}); err == nil {
			t.Fatal("unavailable/invalid immutable mode fell through to ordinary default")
		}
	}
	active := session.Session{SessionRef: session.SessionRef{SessionID: "ordinary"}}
	gateway := &Gateway{sessions: staticSessionService{session: active}, resolver: staticResolver{}, defaultApprovalMode: ApprovalModeManual}
	if got, err := gateway.currentApprovalMode(t.Context(), active.SessionRef); err != nil || got != ApprovalModeManual {
		t.Fatalf("ordinary mode changed: %s, %v", got, err)
	}
}
