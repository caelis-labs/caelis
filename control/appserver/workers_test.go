package appserver

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/control/application"
)

func TestWorkerGrantSurvivesRestartAndScopesAllCommands(t *testing.T) {
	store, path := applicationTestStore(t)
	a := enrollTestApplication(t, store, "owner", "a", "a")
	b := enrollTestApplication(t, store, "owner", "b", "b")
	scope, _ := ApplicationScope(a)
	w, err := store.PutWorker(t.Context(), scope, "worker-create")
	if err != nil {
		t.Fatal(err)
	}
	sessions := inmemory.NewStore(inmemory.Config{})
	_, err = sessions.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "owner", PreferredSessionID: w.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = application.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	auth := SessionAuthorizer{Sessions: sessions, Applications: store}
	for _, action := range []Action{ActionSessionInspect, ActionParticipantList, ActionPrompt, ActionSteer, ActionCancel, ActionApprovalResolve,
		ActionSessionCompact, ActionSessionModel, ActionParticipantStart, ActionParticipantPrompt, ActionParticipantCancel, ActionSubagentInput,
		ActionSessionClose, ActionSessionApprovalMode, ActionSessionControllerMode, ActionSessionPresentationMode, ActionSessionPresentationConfig, ActionControllerHandoff} {
		if err := auth.Authorize(t.Context(), a, action, w.SessionID); err != nil {
			t.Fatalf("owner %s: %v", action, err)
		}
		if err := auth.Authorize(t.Context(), Principal{ID: "owner"}, action, w.SessionID); err != nil {
			t.Fatalf("user %s: %v", action, err)
		}
		if err := auth.Authorize(t.Context(), b, action, w.SessionID); err == nil {
			t.Fatalf("other app acquired %s", action)
		}
		wrongConnection := a
		wrongConnection.ConnectionID = b.ConnectionID
		foreign := a
		foreign.ID = "other-user"
		for _, principal := range []Principal{wrongConnection, foreign} {
			if err := auth.Authorize(t.Context(), principal, action, w.SessionID); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("mismatched scope acquired %s: %v", action, err)
			}
		}
	}
	if err := store.Revoke(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	for _, action := range []Action{ActionSessionInspect, ActionParticipantList} {
		if err := auth.Authorize(t.Context(), a, action, w.SessionID); err != nil {
			t.Fatalf("revoked retained %s: %v", action, err)
		}
	}
	if err := auth.Authorize(t.Context(), a, ActionSteer, w.SessionID); !errors.Is(err, application.ErrRevoked) {
		t.Fatalf("revoked steer: %v", err)
	}
	if err := auth.Authorize(t.Context(), Principal{ID: "owner"}, ActionSteer, w.SessionID); err != nil {
		t.Fatalf("revoked app removed user's access: %v", err)
	}
	for _, action := range []Action{ActionSessionCompact, ActionSessionModel, ActionParticipantStart, ActionParticipantPrompt, ActionParticipantCancel, ActionSubagentInput,
		ActionSessionClose, ActionSessionApprovalMode, ActionSessionControllerMode, ActionSessionPresentationMode, ActionSessionPresentationConfig, ActionControllerHandoff} {
		if err := auth.Authorize(t.Context(), a, action, w.SessionID); !errors.Is(err, application.ErrRevoked) {
			t.Fatalf("revoked %s: %v", action, err)
		}
	}
}

func TestWorkerGrantClosedSessionRetainsObservationAndRejectsNativeMutation(t *testing.T) {
	store, _ := applicationTestStore(t)
	principal := enrollTestApplication(t, store, "owner", "worker", "a")
	scope, _ := ApplicationScope(principal)
	worker, err := store.PutWorker(t.Context(), scope, "create")
	if err != nil {
		t.Fatal(err)
	}
	sessions := inmemory.NewStore(inmemory.Config{})
	active, err := sessions.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "owner", PreferredSessionID: worker.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CloseSession(t.Context(), sessions, active, "closed"); err != nil {
		t.Fatal(err)
	}
	auth := SessionAuthorizer{Sessions: sessions, Applications: store}
	for _, action := range []Action{ActionSessionInspect, ActionParticipantList} {
		if err := auth.Authorize(t.Context(), principal, action, active.SessionID); err != nil {
			t.Fatalf("closed read %s: %v", action, err)
		}
	}
	for _, action := range []Action{ActionSessionCompact, ActionSessionModel, ActionParticipantStart, ActionParticipantPrompt, ActionParticipantCancel, ActionSubagentInput, ActionCancel,
		ActionSessionApprovalMode, ActionSessionControllerMode, ActionSessionPresentationMode, ActionSessionPresentationConfig, ActionControllerHandoff} {
		if err := auth.Authorize(t.Context(), principal, action, active.SessionID); !errors.Is(err, ErrSessionClosed) {
			t.Fatalf("closed mutation %s: %v", action, err)
		}
	}
}
