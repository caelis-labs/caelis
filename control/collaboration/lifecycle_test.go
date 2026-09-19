package collaboration

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"
)

func TestCleanupClosedSessionAfterMailboxDrained(t *testing.T) {
	b := &batchBackend{unavailable: true}
	s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.Send(t.Context(), Identity{"work", "a"}, "b", "retained history", ""); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Receive(t.Context(), Identity{"work", "b"}); err != nil || len(got) != 1 {
		t.Fatalf("drain = %v, %v", got, err)
	}
	if _, err := s.ReadMessages(t.Context(), Identity{"work", "a"}, nil, 32); err != nil {
		t.Fatal(err)
	}
	for _, query := range []string{
		`INSERT INTO collaboration_setups(session,remote) VALUES('work','remote')`,
		`INSERT INTO collaboration_retention(session,trimmed) VALUES('work',0)`,
		`INSERT INTO collaboration_user_inputs(id,user_id,session,participant,body,state,updated) VALUES('input','user','work','b','{}','delivered',0)`,
	} {
		if _, err := s.db.ExecContext(t.Context(), query); err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range sessionTables {
		want := 1
		if table == "collaboration_mailbox" {
			want = 0
		}
		assertCollaborationRows(t, s, table, "work", want)
	}
	b.closed = true
	for range 2 { // Explicit cleanup is safe to repeat after committed closure.
		if err := s.CleanupClosedSession(t.Context(), "work"); err != nil {
			t.Fatal(err)
		}
		for _, table := range sessionTables {
			assertCollaborationRows(t, s, table, "work", 0)
		}
	}
	// Admission/idempotency receipts have a different lifecycle owner.
	assertCollaborationRows(t, s, "collaboration_user_inputs", "work", 1)
}

func TestCleanupClosedSessionRequiresPermanentClosure(t *testing.T) {
	unavailable := errors.New("lifecycle store unavailable")
	for _, tc := range []struct {
		name   string
		closed bool
		cancel bool
		err    error
	}{
		{name: "active"},
		{name: "unavailable", err: unavailable},
		{name: "cancelled", closed: true, cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := &batchBackend{closed: tc.closed, failure: tc.err}
			s, err := Open(filepath.Join(t.TempDir(), "control.sqlite"), b)
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if _, err := s.db.ExecContext(t.Context(), `INSERT INTO collaboration_setups(session,remote) VALUES('work','remote')`); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			wantErr := tc.err
			if tc.cancel {
				cancel()
				wantErr = context.Canceled
			}
			if err := s.CleanupClosedSession(ctx, "work"); !errors.Is(err, wantErr) {
				t.Fatalf("cleanup = %v, want %v", err, wantErr)
			}
			assertCollaborationRows(t, s, "collaboration_setups", "work", 1)
		})
	}
}

type cleanupBackend struct {
	batchBackend
}

func (b *cleanupBackend) List(_ context.Context, id string) ([]Thread, error) {
	switch id {
	case "active":
		return []Thread{{Handle: "parent"}}, nil
	case "unavailable":
		return nil, errors.New("lifecycle unavailable")
	default:
		return nil, ErrSessionClosed
	}
}

func TestRunReconcilesClosedSessionResidueAfterRestart(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "control.sqlite")
		b := &cleanupBackend{}
		s, err := Open(path, b)
		if err != nil {
			t.Fatal(err)
		}
		// Each kind of retained row is independently sufficient for discovery;
		// none of these historical Sessions has any pending delivery.
		queries := []string{
			`INSERT INTO collaboration_messages(session,id,body) VALUES(?,'message','{}')`,
			`INSERT INTO collaboration_readers(session,instance,cursor) VALUES(?,'reader',0)`,
			`INSERT INTO collaboration_seen(session,instance,id) VALUES(?,'reader','message')`,
			`INSERT INTO collaboration_retention(session,trimmed) VALUES(?,0)`,
			`INSERT INTO collaboration_setups(session,remote) VALUES(?,'remote')`,
		}
		for i, query := range queries {
			if _, err := s.db.ExecContext(t.Context(), query, fmt.Sprintf("orphan-%d", i)); err != nil {
				t.Fatal(err)
			}
		}
		// Cross the bounded sweep's page boundary, with non-deletable entries
		// before and after it. An unavailable Session must not starve later IDs.
		ids := []string{"active", "unavailable", "z-closed"}
		for i := range 130 {
			ids = append(ids, fmt.Sprintf("closed-%03d", i))
		}
		for _, id := range ids {
			if _, err := s.db.ExecContext(t.Context(), queries[4], id); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		s, err = Open(path, b)
		if err != nil {
			t.Fatal(err)
		}
		defer s.Close()
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan struct{})
		var reported []error
		go func() {
			defer close(done)
			s.Run(ctx, func(err error) { reported = append(reported, err) })
		}()
		synctest.Wait()
		for i, table := range []string{"collaboration_messages", "collaboration_readers", "collaboration_seen", "collaboration_retention", "collaboration_setups"} {
			assertCollaborationRows(t, s, table, fmt.Sprintf("orphan-%d", i), 0)
		}
		for _, id := range ids {
			want := 0
			if id == "active" || id == "unavailable" {
				want = 1
			}
			assertCollaborationRows(t, s, "collaboration_setups", id, want)
		}
		if len(reported) != 1 || reported[0].Error() != "lifecycle unavailable" {
			t.Fatalf("cleanup reports = %v", reported)
		}
		// A late setup writer racing the close hook is covered by the next sweep.
		if _, err := s.db.ExecContext(t.Context(), queries[4], "late"); err != nil {
			t.Fatal(err)
		}
		time.Sleep(closedSessionSweepInterval)
		synctest.Wait()
		assertCollaborationRows(t, s, "collaboration_setups", "late", 0)
		cancel()
		<-done // Run joins cleanup before the database can be closed.
	})
}

func assertCollaborationRows(t *testing.T, s *Service, table, id string, want int) {
	t.Helper()
	var got int
	if err := s.db.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+table+" WHERE session=?", id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s rows for %s = %d, want %d", table, id, got, want)
	}
}
