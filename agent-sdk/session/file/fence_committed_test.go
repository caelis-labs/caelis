package file

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestFileSessionFenceReadsLegacyV2EncodingAndKeepsStorageKeys(t *testing.T) {
	t.Parallel()

	store, ref, indexPath := newCommittedFenceTestStore(t, "fence-legacy-v2")
	path, err := store.resolveDocumentPath(ref.SessionID, ref.WorkspaceKey)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["lease"] = json.RawMessage(`{"session_ref":{"app_name":"caelis","user_id":"user","session_id":"fence-legacy-v2"},"lease_id":"legacy-fence","owner_id":"legacy-host","revision":7,"fencing_token":42,"acquired_at":"2026-08-23T00:00:00Z"}`)
	document["lease_epoch"] = json.RawMessage(`42`)
	raw, err = json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	fence, err := store.SessionFence(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if fence.FenceID != "legacy-fence" || fence.OwnerID != "legacy-host" || fence.FencingToken != 42 {
		t.Fatalf("legacy v2 fence = %#v", fence)
	}
	if err := store.ReleaseSessionFence(context.Background(), session.ReleaseSessionFenceRequest{
		SessionRef: ref, FenceID: fence.FenceID, OwnerID: fence.OwnerID,
	}); !errors.Is(err, session.ErrFenceConflict) {
		t.Fatalf("release unclaimed legacy v2 fence = %v, want ErrFenceConflict", err)
	}

	_, priorHostFences := NewStoreWithPriorHostFences(Config{RootDir: filepath.Dir(indexPath)}, func(context.Context) (func(), bool) {
		return func() {}, true
	})
	written, err := priorHostFences.ReplacePriorHostSessionFence(context.Background(), session.AcquireSessionFenceRequest{SessionRef: ref, OwnerID: "new-host"})
	if err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, key := range []string{`"lease":`, `"lease_id":`, `"lease_epoch":`, `"revision": 1`, `"claim_digest":`} {
		if !strings.Contains(text, key) {
			t.Fatalf("stored fence document missing legacy key %s: %s", key, text)
		}
	}
	if strings.Contains(text, `"fence":`) || strings.Contains(text, `"fence_id":`) {
		t.Fatalf("stored fence document unexpectedly changed v2 keys: %s", text)
	}
	if written.FenceID == "" || written.FencingToken != 43 {
		t.Fatalf("new fence after legacy v2 release = %#v", written)
	}
}

func TestFileSessionFenceAcquireDoesNotDependOnSessionIndex(t *testing.T) {
	t.Parallel()

	store, ref, indexPath := newCommittedFenceTestStore(t, "fence-acquire-committed")
	breakSessionIndexAfterDocumentRename(t, indexPath)

	fence, err := store.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: ref, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatalf("AcquireSessionFence() error = %v", err)
	}
	if fence.FenceID == "" || fence.FencingToken == 0 {
		t.Fatalf("AcquireSessionFence() fence = %#v, want committed durable fence", fence)
	}

	restoreSessionIndex(t, indexPath)
	durable, err := store.SessionFence(context.Background(), ref)
	if err != nil {
		t.Fatalf("SessionFence() error = %v", err)
	}
	assertFenceEquivalent(t, durable, fence)
}

func TestFileSessionFenceReleaseDoesNotDependOnSessionIndex(t *testing.T) {
	t.Parallel()

	store, ref, indexPath := newCommittedFenceTestStore(t, "fence-release-committed")
	fence, err := store.AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: ref, OwnerID: "host-a",
	})
	if err != nil {
		t.Fatalf("AcquireSessionFence() error = %v", err)
	}
	breakSessionIndexAfterDocumentRename(t, indexPath)

	err = store.ReleaseSessionFence(context.Background(), session.SessionFenceReleaseRequest(fence))
	if err != nil {
		t.Fatalf("ReleaseSessionFence() error = %v", err)
	}

	restoreSessionIndex(t, indexPath)
	durable, err := store.SessionFence(context.Background(), ref)
	if err != nil {
		t.Fatalf("SessionFence() error = %v", err)
	}
	if durable.FenceID != "" {
		t.Fatalf("durable fence = %#v, want released", durable)
	}
}

func newCommittedFenceTestStore(t *testing.T, sessionID string) (*Store, session.SessionRef, string) {
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

func assertFenceEquivalent(t *testing.T, got, want session.SessionFence) {
	t.Helper()
	if session.NormalizeSessionRef(got.SessionRef) != session.NormalizeSessionRef(want.SessionRef) ||
		got.FenceID != want.FenceID || got.OwnerID != want.OwnerID ||
		got.FencingToken != want.FencingToken || !got.AcquiredAt.Equal(want.AcquiredAt) {
		t.Fatalf("durable fence = %#v, returned fence = %#v", got, want)
	}
}
