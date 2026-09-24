package appserver

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/control/application"
)

func TestWorkerCreationRecoversCommittedCommandAfterApplicationReceiptLoss(t *testing.T) {
	store, path := applicationTestStore(t)
	p := enrollTestApplication(t, store, "owner", "enroll", "a")
	scope, _ := ApplicationScope(p)
	req := CreateWorkerRequest{WriteBase: WriteBase{OperationID: "create"}, CWD: t.TempDir(), Model: "provider-model"}
	sid := application.WorkerSessionID(scope, req.OperationID)
	backend := &applicationBoundaryBackend{result: CommandResult{Outcome: OutcomeCommitted, SessionID: sid, Revision: 9}}
	ledger := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	commands := newTestCommandService(t, allowAuthorizer{}, ledger, backend)
	svc, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: &Client{}})
	if err != nil {
		t.Fatal(err)
	}
	backend.afterEffect = func() {
		// The separate SQLite command connection can still persist its result,
		// but the outer permanent application receipt write is unavailable.
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
	}
	result, err := svc.CreateWorker(t.Context(), p, req)
	if errorcode.CodeOf(err) != errorcode.UnknownOutcome || result.Outcome != OutcomeUnknown || backend.effects != 1 {
		t.Fatalf("lost outer receipt = %+v, %v, effects = %d", result, err, backend.effects)
	}
	if err := ledger.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = application.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	svc.config.Store = store
	commands.config.Operations = newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
	backend.afterEffect = nil
	want := backend.result
	want.OperationID = req.OperationID
	repeated, err := svc.CreateWorker(t.Context(), p, req)
	if err != nil || repeated != want || backend.effects != 1 {
		t.Fatalf("repeated Worker after restart = %+v, %v, effects = %d; want %+v", repeated, err, backend.effects, want)
	}
	observed, err := svc.Operation(t.Context(), p, req.OperationID)
	if err != nil || observed.Result == nil || *observed.Result != want || observed.Outcome != OutcomeCommitted {
		t.Fatalf("recovered Worker receipt = %+v, %v; want %+v", observed, err, want)
	}
	// Recovery promotes the existing command receipt to the permanent anchor.
	// The ordinary command ledger may subsequently expire or be swept.
	commands.config.Operations = NewMemoryOperationStore()
	if observed, err := svc.Operation(t.Context(), p, req.OperationID); err != nil || observed.Result == nil || *observed.Result != want {
		t.Fatalf("permanent recovered receipt = %+v, %v", observed, err)
	}
	changed := req
	changed.Model = "another-model"
	if _, err := svc.CreateWorker(t.Context(), p, changed); !errors.Is(err, application.ErrConflict) {
		t.Fatalf("changed Worker request = %v", err)
	}
}

func TestWorkerCreationRecoveryRequiresExactCompleteReceipt(t *testing.T) {
	for _, boundary := range []string{"missing", "intent-only", "expired", "different-action", "different-request", "different-scope", "different-session", "unknown", "committed"} {
		t.Run(boundary, func(t *testing.T) {
			store, _ := applicationTestStore(t)
			p := enrollTestApplication(t, store, "owner", "enroll", "a")
			scope, _ := ApplicationScope(p)
			req := CreateWorkerRequest{WriteBase: WriteBase{OperationID: "create"}, CWD: t.TempDir(), Model: "provider-model"}
			if _, fresh, err := store.BeginOperation(t.Context(), scope, req.OperationID, workerCreateOperation{Kind: ActionWorkerCreate, Request: req}); err != nil || !fresh {
				t.Fatalf("application intent = %v, %v", fresh, err)
			}
			w, err := store.PutWorker(t.Context(), scope, req.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			// Even a granted, inspectable Session cannot prove that model selection
			// succeeded. Recovery must only use the complete command receipt.
			sessions := &applicationRecoverySessions{state: SessionState{SessionID: w.SessionID, Revision: 7}}
			ledger := newTestSQLiteOperationStore(t, filepath.Join(t.TempDir(), "control.sqlite"), OperationRetentionConfig{})
			backend := &applicationBoundaryBackend{}
			commands := newTestCommandService(t, allowAuthorizer{}, ledger, backend)
			svc, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: sessions})
			if err != nil {
				t.Fatal(err)
			}
			commandReq, commandPrincipal, action := req, p, ActionWorkerCreate
			switch boundary {
			case "different-action":
				action = ActionSessionCreate
			case "different-request":
				commandReq.Model = "other-model"
			case "different-scope":
				commandPrincipal = enrollTestApplication(t, store, "owner", "other", "b")
			}
			intent, err := commandOperationIntent(commandPrincipal, action, commandReq.WriteBase, "", commandReq)
			if err != nil {
				t.Fatal(err)
			}
			if boundary != "missing" {
				if _, _, err = ledger.Begin(t.Context(), intent); err != nil {
					t.Fatal(err)
				}
				if boundary != "intent-only" {
					result := CommandResult{OperationID: req.OperationID, Outcome: OutcomeCommitted, SessionID: w.SessionID, Revision: 9}
					if boundary == "different-session" {
						result.SessionID = "unrelated-session"
					}
					if boundary == "unknown" {
						result.Outcome = OutcomeUnknown
					}
					if _, err := ledger.Complete(t.Context(), intent, result); err != nil {
						t.Fatal(err)
					}
				}
			}
			if boundary == "expired" {
				ledger.now = func() time.Time { return time.Now().Add(ledger.effectiveRetention + time.Hour) }
			}
			observed, err := svc.Operation(t.Context(), p, req.OperationID)
			conflict := boundary == "different-action" || boundary == "different-request" || boundary == "different-session"
			if conflict {
				if !errors.Is(err, ErrOperationConflict) && !errors.Is(err, application.ErrConflict) {
					t.Fatalf("mismatched receipt = %+v, %v", observed, err)
				}
			} else {
				wantOutcome := OutcomeUnknown
				if boundary == "committed" {
					wantOutcome = OutcomeCommitted
				}
				if err != nil || observed.Outcome != wantOutcome {
					t.Fatalf("observed receipt = %+v, %v; want %s", observed, err, wantOutcome)
				}
				// Exercise retry as well as the read API: neither may dispatch.
				result, err := svc.CreateWorker(t.Context(), p, req)
				if err != nil || result.Outcome != wantOutcome {
					t.Fatalf("retried receipt = %+v, %v; want %s", result, err, wantOutcome)
				}
			}
			if backend.effects != 0 || sessions.reads != 0 {
				t.Fatalf("recovery touched native state: effects = %d, reads = %d", backend.effects, sessions.reads)
			}
		})
	}
}
