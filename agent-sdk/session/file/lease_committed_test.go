package file

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestFileSessionLeaseAcquireReturnsDurableLeaseAfterCommittedError(t *testing.T) {
	t.Parallel()

	store, ref, indexPath := newCommittedLeaseTestStore(t, "lease-acquire-committed")
	breakSessionIndexAfterDocumentRename(t, indexPath)

	lease, err := store.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: ref, OwnerID: "host-a", TTL: time.Minute,
	})
	if !session.IsCommitted(err) {
		t.Fatalf("AcquireSessionLease() error = %v, want session.CommittedError", err)
	}
	if lease.LeaseID == "" || lease.Revision != 1 || lease.FencingToken == 0 {
		t.Fatalf("AcquireSessionLease() lease = %#v, want committed durable lease", lease)
	}

	restoreSessionIndex(t, indexPath)
	durable, err := store.SessionLease(context.Background(), ref)
	if err != nil {
		t.Fatalf("SessionLease() error = %v", err)
	}
	assertLeaseEquivalent(t, durable, lease)
}

func TestFileSessionLeaseHeartbeatReturnsNewRevisionAfterCommittedError(t *testing.T) {
	t.Parallel()

	store, ref, indexPath := newCommittedLeaseTestStore(t, "lease-heartbeat-committed")
	lease, err := store.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: ref, OwnerID: "host-a", TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("AcquireSessionLease() error = %v", err)
	}
	breakSessionIndexAfterDocumentRename(t, indexPath)

	heartbeat, err := store.HeartbeatSessionLease(context.Background(), session.HeartbeatSessionLeaseRequest{
		SessionRef: ref, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID,
		ExpectedLeaseRevision: lease.Revision, TTL: time.Minute,
	})
	if !session.IsCommitted(err) {
		t.Fatalf("HeartbeatSessionLease() error = %v, want session.CommittedError", err)
	}
	if heartbeat.Revision != 2 || heartbeat.LeaseID != lease.LeaseID || heartbeat.FencingToken != lease.FencingToken {
		t.Fatalf("HeartbeatSessionLease() lease = %#v, want committed revision 2", heartbeat)
	}

	restoreSessionIndex(t, indexPath)
	durable, err := store.SessionLease(context.Background(), ref)
	if err != nil {
		t.Fatalf("SessionLease() error = %v", err)
	}
	assertLeaseEquivalent(t, durable, heartbeat)
	if err := store.ReleaseSessionLease(context.Background(), session.ReleaseSessionLeaseRequest{
		SessionRef: ref, LeaseID: heartbeat.LeaseID, OwnerID: heartbeat.OwnerID,
		ExpectedLeaseRevision: heartbeat.Revision,
	}); err != nil {
		t.Fatalf("ReleaseSessionLease(revision 2) error = %v", err)
	}
}

func TestFileSessionLeaseReleaseClassifiesCommittedError(t *testing.T) {
	t.Parallel()

	store, ref, indexPath := newCommittedLeaseTestStore(t, "lease-release-committed")
	lease, err := store.AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: ref, OwnerID: "host-a", TTL: time.Minute,
	})
	if err != nil {
		t.Fatalf("AcquireSessionLease() error = %v", err)
	}
	breakSessionIndexAfterDocumentRename(t, indexPath)

	err = store.ReleaseSessionLease(context.Background(), session.ReleaseSessionLeaseRequest{
		SessionRef: ref, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID,
		ExpectedLeaseRevision: lease.Revision,
	})
	if !session.IsCommitted(err) {
		t.Fatalf("ReleaseSessionLease() error = %v, want session.CommittedError", err)
	}

	restoreSessionIndex(t, indexPath)
	durable, err := store.SessionLease(context.Background(), ref)
	if err != nil {
		t.Fatalf("SessionLease() error = %v", err)
	}
	if durable.LeaseID != "" {
		t.Fatalf("durable lease = %#v, want released", durable)
	}
}

func newCommittedLeaseTestStore(t *testing.T, sessionID string) (*Store, session.SessionRef, string) {
	t.Helper()
	root := t.TempDir()
	store := NewStore(Config{RootDir: root})
	created, err := store.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "user", PreferredSessionID: sessionID,
	})

	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	return store, created.SessionRef, filepath.Join(root, indexFilename)
}

func breakSessionIndexAfterDocumentRename(t *testing.T, indexPath string) {
	t.Helper()
	if err := os.Remove(indexPath); err != nil {
		t.Fatalf("Remove(index) error = %v", err)
	}
	if err := os.Mkdir(indexPath, 0o700); err != nil {
		t.Fatalf("Mkdir(index path) error = %v", err)
	}
}

func restoreSessionIndex(t *testing.T, indexPath string) {
	t.Helper()
	if err := os.Remove(indexPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Remove(broken index) error = %v", err)
	}
}

func assertLeaseEquivalent(t *testing.T, got, want session.SessionLease) {
	t.Helper()
	got.AcquiredAt = got.AcquiredAt.UTC()
	got.HeartbeatAt = got.HeartbeatAt.UTC()
	got.ExpiresAt = got.ExpiresAt.UTC()
	want.AcquiredAt = want.AcquiredAt.UTC()
	want.HeartbeatAt = want.HeartbeatAt.UTC()
	want.ExpiresAt = want.ExpiresAt.UTC()
	if got != want {
		t.Fatalf("durable lease = %#v, returned lease = %#v", got, want)
	}
}
