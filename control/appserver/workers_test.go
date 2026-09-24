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
	for _, action := range []Action{ActionSessionInspect, ActionPrompt, ActionSteer, ActionCancel, ActionApprovalResolve} {
		if err := auth.Authorize(t.Context(), a, action, w.SessionID); err != nil {
			t.Fatalf("owner %s: %v", action, err)
		}
		if err := auth.Authorize(t.Context(), Principal{ID: "owner"}, action, w.SessionID); err != nil {
			t.Fatalf("user %s: %v", action, err)
		}
		if err := auth.Authorize(t.Context(), b, action, w.SessionID); err == nil {
			t.Fatalf("other app acquired %s", action)
		}
	}
	if err := store.Revoke(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	if err := auth.Authorize(t.Context(), a, ActionSteer, w.SessionID); !errors.Is(err, application.ErrRevoked) {
		t.Fatalf("revoked steer: %v", err)
	}
	if err := auth.Authorize(t.Context(), Principal{ID: "owner"}, ActionSteer, w.SessionID); err != nil {
		t.Fatalf("revoked app removed user's access: %v", err)
	}
}
