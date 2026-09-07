package appserver

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestSessionOperationSQLiteRestartFaultBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name           string
		backendErr     error
		receiptFailure bool
		want           Outcome
	}{
		{name: "committed", want: OutcomeCommitted},
		{name: "report-failed", backendErr: NewOutcomeError(OutcomeCommitted, errors.New("report failed")), want: OutcomeCommitted},
		{name: "unknown-effect", backendErr: errors.New("effect connection lost"), want: OutcomeUnknown},
		{name: "receipt-failed", receiptFailure: true, want: OutcomeUnknown},
		{name: "rejected-before-effect", backendErr: NewOutcomeError(OutcomeRejected, errors.New("not admitted")), want: OutcomeRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			path := filepath.Join(t.TempDir(), "control.sqlite")
			store := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
			var operations OperationStore = store
			if tc.receiptFailure {
				operations = &failFirstCompleteStore{OperationStore: store, failLeft: 1}
			}
			backend := &recordingCommandBackend{err: tc.backendErr}
			svc := newTestCommandService(t, allowAuthorizer{}, operations, backend)
			principal := Principal{ID: "owner"}
			req := PromptRequest{WriteBase: WriteBase{OperationID: "stable-prompt", SessionID: "session-1"}, Input: "hello"}
			first, err := svc.Prompt(ctx, principal, req)
			if first.Outcome != tc.want || (err != nil) != (tc.backendErr != nil || tc.receiptFailure) {
				t.Fatalf("first = %#v, %v", first, err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			reopened := newTestSQLiteOperationStore(t, path, OperationRetentionConfig{})
			replayBackend := &recordingCommandBackend{}
			replay := newTestCommandService(t, allowAuthorizer{}, reopened, replayBackend)
			got, err := replay.Prompt(ctx, principal, req)
			if err != nil || got.Outcome != tc.want {
				t.Fatalf("requery = %#v, %v", got, err)
			}
			if !tc.receiptFailure && !sameCommandResult(got, first) {
				t.Fatalf("receipt changed: %#v != %#v", got, first)
			}
			req.Input = "changed payload"
			conflict, err := replay.Prompt(ctx, principal, req)
			if !errors.Is(err, ErrOperationConflict) || conflict.Outcome != OutcomeConflicted {
				t.Fatalf("conflict = %#v, %v", conflict, err)
			}
			if backend.calls != 1 || replayBackend.calls != 0 {
				t.Fatalf("dispatch counts = %d/%d", backend.calls, replayBackend.calls)
			}
		})
	}
}

func TestSessionOperationUnreadableReceiptNeverClaimsRejection(t *testing.T) {
	for _, payload := range []string{`{`, `{"version":999}`} {
		t.Run(payload, func(t *testing.T) {
			ctx := context.Background()
			store := newTestSQLiteOperationStore(t, filepath.Join(t.TempDir(), "control.sqlite"), OperationRetentionConfig{})
			backend := &recordingCommandBackend{}
			service := newTestCommandService(t, allowAuthorizer{}, store, backend)
			req := PromptRequest{WriteBase: WriteBase{OperationID: "committed-op", SessionID: "session-1"}, Input: "hello"}
			principal := Principal{ID: "owner"}
			first, err := service.Prompt(ctx, principal, req)
			if err != nil || first.Outcome != OutcomeCommitted {
				t.Fatalf("first = %#v, %v", first, err)
			}
			if _, err := store.db.Exec(`UPDATE control_operations SET record_json = ? WHERE operation_id = ?`, []byte(payload), req.OperationID); err != nil {
				t.Fatal(err)
			}
			result, err := service.Prompt(ctx, principal, req)
			if err == nil || result.Outcome != OutcomeUnknown {
				t.Fatalf("unreadable committed receipt = %#v, %v; must remain unknown", result, err)
			}
			if backend.calls != 1 {
				t.Fatalf("effect repeated %d times", backend.calls)
			}
		})
	}
}
