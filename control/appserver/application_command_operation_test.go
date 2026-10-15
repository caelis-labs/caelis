package appserver

import (
	"errors"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/application"
)

func TestApplicationCommandHasOnePermanentReceipt(t *testing.T) {
	store, path := applicationTestStore(t)
	principal := enrollTestApplication(t, store, "owner", "enroll", "a")
	scope, _ := ApplicationScope(principal)
	request := CreateWorkerRequest{WriteBase: WriteBase{OperationID: "create"}, CWD: t.TempDir()}
	backend := &applicationBoundaryBackend{result: CommandResult{Outcome: OutcomeCommitted, SessionID: application.WorkerSessionID(scope, request.OperationID), Revision: 17}}
	ledger := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	commands := newTestCommandService(t, allowAuthorizer{}, ledger, backend)
	service, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: &Client{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CreateWorker(t.Context(), principal, request)
	if err != nil || result.Outcome != OutcomeCommitted {
		t.Fatalf("create: %+v %v", result, err)
	}
	repeated, err := service.CreateWorker(t.Context(), principal, request)
	if err != nil || repeated != result || backend.effects != 1 {
		t.Fatalf("repeat: %+v %v effects=%d", repeated, err, backend.effects)
	}
	var permanent, ordinary int
	if err := ledger.db.QueryRow(`SELECT count(*) FROM app_operations WHERE connection=? AND operation=? AND result IS NOT NULL`, scope.ConnectionID, request.OperationID).Scan(&permanent); err != nil {
		t.Fatal(err)
	}
	if err := ledger.db.QueryRow(`SELECT count(*) FROM control_operations WHERE operation_id=?`, request.OperationID).Scan(&ordinary); err != nil {
		t.Fatal(err)
	}
	if permanent != 1 || ordinary != 0 {
		t.Fatalf("receipt authorities: permanent=%d ordinary=%d", permanent, ordinary)
	}
	changed := request
	changed.Title = "different"
	if _, err := service.CreateWorker(t.Context(), principal, changed); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("changed intent: %v", err)
	}
	if backend.effects != 1 {
		t.Fatalf("changed request dispatched: %d", backend.effects)
	}
}

func TestApplicationAdmittedIntentStillUsesCommandAuthorization(t *testing.T) {
	store, path := applicationTestStore(t)
	principal := enrollTestApplication(t, store, "owner", "enroll", "a")
	request := CreateWorkerRequest{WriteBase: WriteBase{OperationID: "create"}, CWD: t.TempDir()}
	backend := &applicationBoundaryBackend{}
	ledger := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	commands := newTestCommandService(t, denyAuthorizer{}, ledger, backend)
	service, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: &Client{}})
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.CreateWorker(t.Context(), principal, request)
	if errorcode.CodeOf(err) != errorcode.PermissionDenied || result.ErrorCode != errorcode.PermissionDenied || backend.effects != 0 {
		t.Fatalf("authorization bypassed: %+v %v effects=%d", result, err, backend.effects)
	}
	observed, err := service.Operation(t.Context(), principal, request.OperationID)
	if err != nil || observed.Result == nil || *observed.Result != result {
		t.Fatalf("rejected receipt: %+v %v", observed, err)
	}
}

func TestApplicationCommandCrossEntryOperationIdentity(t *testing.T) {
	for _, first := range []string{"prompt", "cancel", "approval"} {
		t.Run(first, func(t *testing.T) {
			store, path := applicationTestStore(t)
			p := enrollTestApplication(t, store, "owner", "enroll", "a")
			other := enrollTestApplication(t, store, "owner", "other", "b")
			scope, _ := ApplicationScope(p)
			if err := store.PutBinding(t.Context(), application.Binding{Scope: scope, SessionID: "session", Profile: applicationTestProfile(), CreationDigest: "digest"}); err != nil {
				t.Fatal(err)
			}
			target := TurnTarget{HandleID: "handle", RunID: "run", TurnID: "turn"}
			backend := &applicationBoundaryBackend{result: CommandResult{Outcome: OutcomeCommitted, SessionID: "session", Target: target}}
			ledger := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
			commands := newTestCommandService(t, allowAuthorizer{}, ledger, backend)
			commands.config.Applications = store
			service, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: &Client{}})
			if err != nil {
				t.Fatal(err)
			}
			base := WriteBase{OperationID: "shared-id", SessionID: "session"}
			invoke := func(kind string, principal Principal) (CommandResult, error) {
				switch kind {
				case "prompt":
					return service.Prompt(t.Context(), principal, ApplicationPromptRequest{PromptRequest: PromptRequest{WriteBase: base, Input: "fixture"}, SourceKind: "user"})
				case "cancel":
					return commands.Cancel(t.Context(), principal, CancelRequest{WriteBase: base, Target: target})
				default:
					return commands.ResolveApproval(t.Context(), principal, ResolveApprovalRequest{WriteBase: base, Target: target, ApprovalRequestID: "approval"})
				}
			}
			result, err := invoke(first, p)
			if err != nil || result.Outcome != OutcomeCommitted {
				t.Fatalf("first: %+v %v", result, err)
			}
			if retry, err := invoke(first, p); err != nil || retry != result || backend.effects != 1 {
				t.Fatalf("retry: %+v %v effects=%d", retry, err, backend.effects)
			}
			for _, second := range []string{"prompt", "cancel", "approval"} {
				if second == first {
					continue
				}
				if _, err := invoke(second, p); !errors.Is(err, ErrOperationConflict) && !errors.Is(err, application.ErrConflict) {
					t.Fatalf("%s after %s: %v", second, first, err)
				}
			}
			observed, err := service.Operation(t.Context(), p, base.OperationID)
			if err != nil || observed.Result == nil || *observed.Result != result || backend.effects != 1 {
				t.Fatalf("original receipt: %+v %v effects=%d", observed, err, backend.effects)
			}
			if result, err := invoke("cancel", other); err != nil || result.Outcome != OutcomeCommitted || backend.effects != 2 {
				t.Fatalf("different connection: %+v %v effects=%d", result, err, backend.effects)
			}
			var permanent, ordinary int
			if err := ledger.db.QueryRow(`SELECT count(*) FROM app_operations WHERE operation=? AND result IS NOT NULL`, base.OperationID).Scan(&permanent); err != nil {
				t.Fatal(err)
			}
			if err := ledger.db.QueryRow(`SELECT count(*) FROM control_operations WHERE operation_id=?`, base.OperationID).Scan(&ordinary); err != nil {
				t.Fatal(err)
			}
			if permanent != 2 || ordinary != 0 {
				t.Fatalf("receipt authorities: permanent=%d ordinary=%d", permanent, ordinary)
			}
		})
	}
}

func TestApplicationCommandRetainedLegacyConflict(t *testing.T) {
	store, path := applicationTestStore(t)
	p := enrollTestApplication(t, store, "owner", "enroll", "a")
	scope, _ := ApplicationScope(p)
	if err := store.PutBinding(t.Context(), application.Binding{Scope: scope, SessionID: "session", Profile: applicationTestProfile(), CreationDigest: "digest"}); err != nil {
		t.Fatal(err)
	}
	ledger := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	backend := &applicationBoundaryBackend{}
	commands := newTestCommandService(t, allowAuthorizer{}, ledger, backend)
	commands.config.Applications = store
	service, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: &Client{}})
	if err != nil {
		t.Fatal(err)
	}
	base := WriteBase{OperationID: "old-id", SessionID: "session"}
	cancel := CancelRequest{WriteBase: base, Target: TurnTarget{HandleID: "handle", RunID: "run", TurnID: "turn"}}
	intent, err := commandOperationIntent(p, ActionCancel, base, turnTargetKey(cancel.Target), cancel)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ledger.Begin(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	want := CommandResult{OperationID: base.OperationID, SessionID: base.SessionID, Outcome: OutcomeCommitted}
	if _, err := ledger.Complete(t.Context(), intent, want); err != nil {
		t.Fatal(err)
	}
	if retry, err := commands.Cancel(t.Context(), p, cancel); err != nil || retry != want {
		t.Fatalf("legacy retry: %+v %v", retry, err)
	}
	if _, err := service.Prompt(t.Context(), p, ApplicationPromptRequest{PromptRequest: PromptRequest{WriteBase: base, Input: "fixture"}, SourceKind: "user"}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("legacy cancel then prompt: %v", err)
	}
	if backend.effects != 0 {
		t.Fatalf("legacy operation redispatched: %d", backend.effects)
	}
}

func TestApplicationCommandAdmissionExcludesConcurrentCrossEntry(t *testing.T) {
	for _, firstPrompt := range []bool{true, false} {
		t.Run(map[bool]string{true: "prompt-in-flight", false: "cancel-in-flight"}[firstPrompt], func(t *testing.T) {
			store, path := applicationTestStore(t)
			p := enrollTestApplication(t, store, "owner", "enroll", "a")
			scope, _ := ApplicationScope(p)
			if err := store.PutBinding(t.Context(), application.Binding{Scope: scope, SessionID: "session", Profile: applicationTestProfile(), CreationDigest: "digest"}); err != nil {
				t.Fatal(err)
			}
			entered, finish := make(chan struct{}), make(chan struct{})
			backend := &applicationBoundaryBackend{result: CommandResult{Outcome: OutcomeCommitted}, afterEffect: func() { close(entered); <-finish }}
			commands := newTestCommandService(t, allowAuthorizer{}, newTestSQLiteOperationStore(t, path, OperationRetentionConfig{}), backend)
			commands.config.Applications = store
			service, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: &Client{}})
			if err != nil {
				t.Fatal(err)
			}
			base := WriteBase{OperationID: "racing-id", SessionID: "session"}
			invoke := func(prompt bool) error {
				if prompt {
					_, err := service.Prompt(t.Context(), p, ApplicationPromptRequest{PromptRequest: PromptRequest{WriteBase: base, Input: "fixture"}, SourceKind: "user"})
					return err
				}
				_, err := commands.Cancel(t.Context(), p, CancelRequest{WriteBase: base, Target: TurnTarget{HandleID: "handle", RunID: "run", TurnID: "turn"}})
				return err
			}
			done := make(chan error, 1)
			go func() { done <- invoke(firstPrompt) }()
			<-entered
			conflict := invoke(!firstPrompt)
			close(finish)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if !errors.Is(conflict, ErrOperationConflict) && !errors.Is(conflict, application.ErrConflict) {
				t.Fatalf("concurrent conflict: %v", conflict)
			}
			if backend.effects != 1 {
				t.Fatalf("effects=%d", backend.effects)
			}
		})
	}
}
