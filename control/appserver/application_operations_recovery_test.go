package appserver

import (
	"errors"
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/control/application"
)

func TestApplicationCommittedPromptSurvivesRevocation(t *testing.T) {
	store, path := applicationTestStore(t)
	p := enrollTestApplication(t, store, "owner", "enroll", "d")
	scope, _ := ApplicationScope(p)
	if err := store.PutBinding(t.Context(), application.Binding{Scope: scope, SessionID: "session", Profile: applicationTestProfile(), CreationDigest: "digest"}); err != nil {
		t.Fatal(err)
	}
	backend := &applicationBoundaryBackend{result: CommandResult{Outcome: OutcomeCommitted, SessionID: "session", Target: TurnTarget{RunID: "run", TurnID: "turn"}}}
	backend.afterEffect = func() {
		if err := store.Revoke(t.Context(), scope); err != nil {
			t.Fatal(err)
		}
	}
	commands := newTestCommandService(t, allowAuthorizer{}, NewMemoryOperationStore(), backend)
	svc, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: &Client{}})
	if err != nil {
		t.Fatal(err)
	}
	req := ApplicationPromptRequest{PromptRequest: PromptRequest{WriteBase: WriteBase{OperationID: "prompt", SessionID: "session"}, Input: "fixture"}, SourceKind: "user"}
	result, err := svc.Prompt(t.Context(), p, req)
	if err != nil || result.Outcome != OutcomeCommitted || backend.effects != 1 {
		t.Fatalf("committed prompt = %+v, %v, effects = %d", result, err, backend.effects)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := application.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	commands.config.Operations = NewMemoryOperationStore()
	recovered, err := NewApplicationService(ApplicationServiceConfig{Store: reopened, Commands: commands, Sessions: &Client{}})
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := recovered.Operation(t.Context(), p, req.OperationID)
	if err != nil || receipt.Outcome != OutcomeCommitted || !reflect.DeepEqual(receipt.Result, &result) {
		t.Fatalf("revoked read after restart = %+v, %v; want %+v", receipt, err, result)
	}
	if _, err := recovered.Prompt(t.Context(), p, req); !errors.Is(err, application.ErrRevoked) {
		t.Fatalf("revoked prompt re-admitted: %v", err)
	}
	if backend.effects != 1 {
		t.Fatalf("native effects = %d", backend.effects)
	}
}
