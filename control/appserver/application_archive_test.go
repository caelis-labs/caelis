package appserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/control/application"
)

type applicationArchiveSessions struct {
	Service
	native     *file.Store
	active     session.Session
	afterClose func()
	closes     int
}

func (s *applicationArchiveSessions) CloseSession(ctx context.Context, _ Principal, req CloseSessionRequest) (CommandResult, error) {
	s.closes++
	updated, err := CloseSession(ctx, s.native, s.active, "application archive")
	if err != nil {
		return CommandResult{}, err
	}
	if s.afterClose != nil {
		s.afterClose()
	}
	return CommandResult{OperationID: req.OperationID, SessionID: updated.SessionID, Outcome: OutcomeCommitted, Revision: updated.Revision}, nil
}

func newApplicationArchiveFixture(t *testing.T) (*ApplicationService, Principal, *applicationArchiveSessions, string, string) {
	t.Helper()
	store, path := applicationTestStore(t)
	p := enrollTestApplication(t, store, "owner", "enroll", "e")
	scope, _ := ApplicationScope(p)
	root := t.TempDir()
	native := file.NewStore(file.Config{RootDir: root})
	active, err := native.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: p.ID, PreferredSessionID: "archive-session"})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.PutBinding(t.Context(), application.Binding{Scope: scope, SessionID: active.SessionID, Profile: applicationTestProfile(), CreationDigest: "digest"}); err != nil {
		t.Fatal(err)
	}
	sessions := &applicationArchiveSessions{native: native, active: active}
	commands := newTestCommandService(t, allowAuthorizer{}, NewMemoryOperationStore(), &applicationBoundaryBackend{})
	svc, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: sessions})
	if err != nil {
		t.Fatal(err)
	}
	return svc, p, sessions, path, root
}

func reopenApplicationArchiveFixture(t *testing.T, svc *ApplicationService, sessions *applicationArchiveSessions, path, root string) *ApplicationService {
	t.Helper()
	if err := svc.Store().Close(); err != nil {
		t.Fatal(err)
	}
	store, err := application.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	sessions.native = file.NewStore(file.Config{RootDir: root})
	// Recovery must not depend on the shared command ledger's retention.
	commands := newTestCommandService(t, allowAuthorizer{}, NewMemoryOperationStore(), &applicationBoundaryBackend{})
	recovered, err := NewApplicationService(ApplicationServiceConfig{Store: store, Commands: commands, Sessions: sessions})
	if err != nil {
		t.Fatal(err)
	}
	return recovered
}

// The cancellation case is extracted from the release-review overlay. The
// trigger exercises the separate durable receipt/binding failure boundary.
func TestApplicationArchiveCompletionSurvivesCancellationAndRestart(t *testing.T) {
	for _, mode := range []string{"cancel", "revoke", "write-failure-query", "write-failure-retry", "write-failure-revoked-query"} {
		t.Run(mode, func(t *testing.T) {
			svc, p, sessions, path, root := newApplicationArchiveFixture(t)
			scope, _ := ApplicationScope(p)
			req := CloseSessionRequest{WriteBase: WriteBase{OperationID: "archive", SessionID: sessions.active.SessionID}}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			writeFailure := mode != "cancel" && mode != "revoke"
			if writeFailure {
				applicationArchiveSQL(t, path, `CREATE TRIGGER fail_archive BEFORE UPDATE ON app_bindings BEGIN SELECT RAISE(FAIL, 'archive write failed'); END`)
			}
			sessions.afterClose = func() {
				cancel()
				if mode == "revoke" || mode == "write-failure-revoked-query" {
					if err := svc.Store().Revoke(t.Context(), scope); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := svc.Archive(ctx, p, req)
			if writeFailure {
				if result.Outcome != OutcomeUnknown || errorcode.CodeOf(err) != errorcode.UnknownOutcome {
					t.Fatalf("failed archive completion = %+v, %v", result, err)
				}
			} else if err != nil || result.Outcome != OutcomeCommitted {
				t.Fatalf("detached completion = %+v, %v", result, err)
			}
			binding, err := svc.Store().GetBinding(t.Context(), scope, req.SessionID)
			if err != nil || binding.Archived == writeFailure {
				t.Fatalf("binding before restart = %+v, %v", binding, err)
			}
			original, err := svc.Store().GetOperation(t.Context(), scope, req.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			var exact CommandResult
			if err := json.Unmarshal(original.Result, &exact); err != nil || exact.Outcome != OutcomeCommitted || exact.SessionID != req.SessionID {
				t.Fatalf("native receipt was lost: %s, %v", original.Result, err)
			}
			if writeFailure {
				if _, err := svc.Operation(t.Context(), p, req.OperationID); errorcode.CodeOf(err) != errorcode.UnknownOutcome {
					t.Fatalf("query reported completed archive while write still fails: %v", err)
				}
				applicationArchiveSQL(t, path, `DROP TRIGGER fail_archive`)
			}
			svc = reopenApplicationArchiveFixture(t, svc, sessions, path, root)
			if closed, err := IsSessionClosed(t.Context(), sessions.native, sessions.active.SessionRef); err != nil || !closed {
				t.Fatalf("canonical close after restart = %t, %v", closed, err)
			}
			if mode == "write-failure-retry" {
				retry, err := svc.Archive(t.Context(), p, req)
				if err != nil || !reflect.DeepEqual(retry, exact) {
					t.Fatalf("same-ID recovery = %+v, %v; want %+v", retry, err, exact)
				}
			}
			observed, err := svc.Operation(t.Context(), p, req.OperationID)
			if err != nil || observed.Outcome != OutcomeCommitted || !reflect.DeepEqual(observed.Result, &exact) {
				t.Fatalf("observed = %+v, %v; want %+v", observed, err, exact)
			}
			binding, err = svc.Store().GetBinding(t.Context(), scope, req.SessionID)
			if err != nil || !binding.Archived || sessions.closes != 1 {
				t.Fatalf("recovery binding = %+v, closes = %d, err = %v", binding, sessions.closes, err)
			}
			after, err := svc.Store().GetOperation(t.Context(), scope, req.OperationID)
			if err != nil || string(after.Result) != string(original.Result) {
				t.Fatalf("recovery changed immutable receipt: %s, %v", after.Result, err)
			}
			if mode == "revoke" || mode == "write-failure-revoked-query" {
				if _, err := svc.Archive(t.Context(), p, req); !errors.Is(err, application.ErrRevoked) {
					t.Fatalf("revoked archive admission restored: %v", err)
				}
			} else {
				changed := req
				changed.ExpectedRevision = new(uint64)
				if _, err := svc.Archive(t.Context(), p, changed); !errors.Is(err, application.ErrConflict) {
					t.Fatalf("changed close intent accepted: %v", err)
				}
			}
			page, err := sessions.native.EventsPage(t.Context(), session.EventPageRequest{SessionRef: sessions.active.SessionRef, Visibility: session.EventPageClientReplay})
			if err != nil || len(page.Events) != 1 || page.Events[0].Lifecycle == nil || page.Events[0].Lifecycle.Status != "closed" {
				t.Fatalf("canonical lifecycle changed during recovery: %+v, %v", page, err)
			}
		})
	}
}

func TestApplicationArchiveRecoveryRacesCompletionWithoutRedispatch(t *testing.T) {
	svc, p, sessions, _, _ := newApplicationArchiveFixture(t)
	req := CloseSessionRequest{WriteBase: WriteBase{OperationID: "archive", SessionID: sessions.active.SessionID}}
	closed, release := make(chan struct{}), make(chan struct{})
	sessions.afterClose = func() { close(closed); <-release }
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var completed CommandResult
	var completionErr error
	var wg sync.WaitGroup
	wg.Go(func() { completed, completionErr = svc.Archive(ctx, p, req) })
	<-closed
	// The native effect has committed, but there is no application receipt yet.
	observed, err := svc.Operation(t.Context(), p, req.OperationID)
	if err != nil || observed.Outcome != OutcomeUnknown {
		t.Errorf("observation raced unrecorded completion: %+v, %v", observed, err)
	}
	repeated, err := svc.Archive(t.Context(), p, req)
	if err != nil || repeated.Outcome != OutcomeUnknown {
		t.Errorf("retry raced unrecorded completion: %+v, %v", repeated, err)
	}
	cancel()
	close(release)
	for range 8 {
		wg.Go(func() {
			if _, err := svc.Operation(t.Context(), p, req.OperationID); err != nil {
				t.Errorf("concurrent observation: %v", err)
			}
		})
	}
	wg.Wait()
	if completionErr != nil || completed.Outcome != OutcomeCommitted || sessions.closes != 1 {
		t.Fatalf("completion = %+v, %v, native closes = %d", completed, completionErr, sessions.closes)
	}
	observed, err = svc.Operation(t.Context(), p, req.OperationID)
	if err != nil || !reflect.DeepEqual(observed.Result, &completed) {
		t.Fatalf("final receipt = %+v, %v", observed, err)
	}
}

func applicationArchiveSQL(t *testing.T, path, statement string) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.ExecContext(t.Context(), statement); err != nil {
		t.Fatal(err)
	}
}

func TestApplicationArchiveRecoveryRequiresOriginalReceipt(t *testing.T) {
	for _, evidence := range []string{"intent-only", "unknown", "rejected", "other-operation", "other-session", "other-request-id"} {
		t.Run(evidence, func(t *testing.T) {
			svc, p, sessions, path, root := newApplicationArchiveFixture(t)
			scope, _ := ApplicationScope(p)
			req := CloseSessionRequest{WriteBase: WriteBase{OperationID: "archive", SessionID: sessions.active.SessionID}}
			admitted := req
			if evidence == "other-request-id" {
				admitted.OperationID = "another-operation"
			}
			if _, fresh, err := svc.Store().BeginOperation(t.Context(), scope, req.OperationID, applicationArchiveRequest{"archive", admitted}); err != nil || !fresh {
				t.Fatalf("intent = %t, %v", fresh, err)
			}
			// A close from another operation cannot prove this admitted intent.
			if _, err := CloseSession(t.Context(), sessions.native, sessions.active, "unrelated close"); err != nil {
				t.Fatal(err)
			}
			if evidence != "intent-only" {
				result := CommandResult{OperationID: req.OperationID, SessionID: req.SessionID, Outcome: OutcomeCommitted}
				switch evidence {
				case "unknown":
					result.Outcome = OutcomeUnknown
				case "rejected":
					result.Outcome = OutcomeRejected
				case "other-operation":
					result.OperationID = "another-operation"
				case "other-session":
					result.SessionID = "another-session"
				}
				data, err := json.Marshal(result)
				if err != nil {
					t.Fatal(err)
				}
				if err := svc.Store().CompleteOperation(t.Context(), scope, req.OperationID, data); err != nil {
					t.Fatal(err)
				}
			}
			svc = reopenApplicationArchiveFixture(t, svc, sessions, path, root)
			observed, err := svc.Operation(t.Context(), p, req.OperationID)
			switch evidence {
			case "other-operation", "other-session", "other-request-id":
				if !errors.Is(err, application.ErrConflict) {
					t.Fatalf("mismatched receipt = %+v, %v", observed, err)
				}
			default:
				if err != nil || observed.Outcome == OutcomeCommitted {
					t.Fatalf("unproven receipt = %+v, %v", observed, err)
				}
			}
			if evidence == "intent-only" {
				retry, err := svc.Archive(t.Context(), p, req)
				if err != nil || retry.Outcome != OutcomeUnknown {
					t.Fatalf("unproven retry = %+v, %v", retry, err)
				}
			}
			binding, err := svc.Store().GetBinding(t.Context(), scope, req.SessionID)
			if err != nil || binding.Archived || sessions.closes != 0 {
				t.Fatalf("unproven archive changed state: %+v, closes = %d, %v", binding, sessions.closes, err)
			}
		})
	}
}
