package gatewayapp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	controlagents "github.com/caelis-labs/caelis/control/agents"
)

func TestACPPreparationStoreLifecycleAndRestart(t *testing.T) {
	t.Parallel()
	storeDir := t.TempDir()
	store := newTestACPPreparationStore(t, storeDir)
	created, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "a"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(created.Ref, "acpp_") || created.State != controlagents.PreparationStatePlanned ||
		created.ContentDigest == "" || !created.ExpiresAt.Equal(created.CreatedAt.Add(acpPreparationTTL)) {
		t.Fatalf("CreatePlanned() = %#v", created)
	}
	needsAuth := testNeedsAuthACPPreparation(created)
	saved, err := store.Save(context.Background(), created.ContentDigest, needsAuth)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(context.Background(), created.ContentDigest, needsAuth); !errors.Is(err, errACPPreparationConflict) {
		t.Fatalf("Save(stale digest) error = %v", err)
	}
	wrongOwner := saved
	wrongOwner.PrincipalID = "another-principal"
	if _, err := store.Save(context.Background(), saved.ContentDigest, wrongOwner); !errors.Is(err, errACPPreparationOwner) {
		t.Fatalf("Save(changed owner) error = %v", err)
	}
	saved, err = store.Save(context.Background(), saved.ContentDigest, testReadyACPPreparation(saved))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	restarted := newTestACPPreparationStore(t, storeDir)
	loaded, err := restarted.Get(context.Background(), saved.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded, saved) {
		t.Fatalf("Get() after restart = %#v, want %#v", loaded, saved)
	}
	info, err := os.Stat(controlStoreDatabasePath(storeDir))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("Control database permissions = %04o, want 0600", info.Mode().Perm())
	}
}

func TestACPPreparationStoreFindByIntent(t *testing.T) {
	t.Parallel()
	store := newTestACPPreparationStore(t, t.TempDir())
	created, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "b"))
	if err != nil {
		t.Fatal(err)
	}
	found, ok, err := store.FindByIntent(context.Background(), created.PrincipalID, created.OperationID, created.IntentDigest)
	if err != nil || !ok || !reflect.DeepEqual(found, created) {
		t.Fatalf("FindByIntent() = %#v, found=%v, error=%v", found, ok, err)
	}
	if _, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "b")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.FindByIntent(context.Background(), created.PrincipalID, created.OperationID, created.IntentDigest); !errors.Is(err, errACPPreparationAmbiguous) {
		t.Fatalf("FindByIntent(duplicate) error = %v", err)
	}
}

func TestACPPreparationStoreExactCASConcurrent(t *testing.T) {
	t.Parallel()
	storeDir := t.TempDir()
	creator := newTestACPPreparationStore(t, storeDir)
	created, err := creator.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "c"))
	if err != nil {
		t.Fatal(err)
	}
	ready := testReadyACPPreparation(created)
	const writers = 12
	var successes atomic.Int32
	var conflicts atomic.Int32
	errorsCh := make(chan error, writers)
	var wait sync.WaitGroup
	for range writers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			store, err := newACPPreparationStore(storeDir)
			if err != nil {
				errorsCh <- err
				return
			}
			defer store.Close()
			_, err = store.Save(context.Background(), created.ContentDigest, ready)
			switch {
			case err == nil:
				successes.Add(1)
			case errors.Is(err, errACPPreparationConflict):
				conflicts.Add(1)
			default:
				errorsCh <- err
			}
		}()
	}
	wait.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatalf("Save() unexpected error = %v", err)
	}
	if successes.Load() != 1 || conflicts.Load() != writers-1 {
		t.Fatalf("Save() successes=%d conflicts=%d, want 1/%d", successes.Load(), conflicts.Load(), writers-1)
	}
}

func TestACPPreparationStoreCancellationAndExpiration(t *testing.T) {
	t.Parallel()
	store := newTestACPPreparationStore(t, t.TempDir())
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.CreatePlanned(canceled, testPlannedACPPreparation("principal", "operation", "d")); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreatePlanned(canceled) error = %v", err)
	}
	now := time.Now().UTC().Round(0)
	store.now = func() time.Time { return now }
	created, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "e"))
	if err != nil {
		t.Fatal(err)
	}
	now = created.ExpiresAt
	if _, err := store.Get(context.Background(), created.Ref); !errors.Is(err, errACPPreparationExpired) {
		t.Fatalf("Get(expired) error = %v", err)
	}
	if _, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "next", "f")); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM control_acp_preparations WHERE ref = ?`, created.Ref).Scan(&count); err != nil || count != 0 {
		t.Fatalf("expired row count=%d error=%v", count, err)
	}
}

func TestACPPreparationStoreRejectsCorruptSQLiteIndexes(t *testing.T) {
	store := newTestACPPreparationStore(t, t.TempDir())
	created, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "indexed-corrupt", "7"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`
UPDATE control_acp_preparations
SET principal_id = ?, expires_at_ns = ?
WHERE ref = ?`, "another-principal", created.CreatedAt.Add(-time.Minute).UnixNano(), created.Ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(context.Background(), created.Ref); err == nil || !strings.Contains(err.Error(), "indexes do not match") {
		t.Fatalf("Get(corrupt indexes) error = %v", err)
	}
	if _, _, err := store.FindByIntent(context.Background(), "another-principal", created.OperationID, created.IntentDigest); err == nil || !strings.Contains(err.Error(), "indexes do not match") {
		t.Fatalf("FindByIntent(corrupt indexes) error = %v", err)
	}
	if referenced, err := store.referencesLauncherRoot(context.Background(), filepath.Join(t.TempDir(), legacyACPAgentDirectory)); err == nil || referenced || !strings.Contains(err.Error(), "indexes do not match") {
		t.Fatalf("referencesLauncherRoot(corrupt indexes) = %v, %v", referenced, err)
	}
	if _, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "after-corruption", "8")); err == nil || !strings.Contains(err.Error(), "indexes do not match") {
		t.Fatalf("CreatePlanned(corrupt indexes) error = %v", err)
	}
}

func TestACPPreparationStoreCapacityIsAtomicAcrossInstances(t *testing.T) {
	storeDir := t.TempDir()
	first := newTestACPPreparationStore(t, storeDir)
	second := newTestACPPreparationStore(t, storeDir)
	now := time.Now().UTC().Round(0)
	tx, err := first.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := range maxACPPreparationDocuments - 1 {
		preparation := testPlannedACPPreparation("principal", fmt.Sprintf("seed-%d", index), "9")
		preparation.Ref, err = newACPPreparationRef()
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		preparation.CreatedAt = now
		preparation.ExpiresAt = now.Add(acpPreparationTTL)
		preparation, err = controlagents.SealACPPreparation(preparation)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		raw, err := encodeACPPreparation(preparation)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		if _, err := tx.Exec(`
INSERT INTO control_acp_preparations
	(ref, principal_id, operation_id, intent_digest, content_digest, expires_at_ns, record_json)
VALUES (?, ?, ?, ?, ?, ?, ?)`, preparation.Ref, preparation.PrincipalID, preparation.OperationID,
			preparation.IntentDigest, preparation.ContentDigest, preparation.ExpiresAt.UnixNano(), raw); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	for index, store := range []*acpPreparationStore{first, second} {
		go func(index int, store *acpPreparationStore) {
			<-start
			_, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", fmt.Sprintf("contender-%d", index), "a"))
			errs <- err
		}(index, store)
	}
	close(start)
	var succeeded, full int
	for range 2 {
		err := <-errs
		switch {
		case err == nil:
			succeeded++
		case strings.Contains(err.Error(), "capacity"):
			full++
		default:
			t.Fatalf("CreatePlanned() error = %v", err)
		}
	}
	if succeeded != 1 || full != 1 {
		t.Fatalf("capacity outcomes = %d succeeded/%d full, want 1/1", succeeded, full)
	}
	var count int
	if err := first.db.QueryRow(`SELECT COUNT(*) FROM control_acp_preparations`).Scan(&count); err != nil || count != maxACPPreparationDocuments {
		t.Fatalf("preparation count = %d, %v; want %d", count, err, maxACPPreparationDocuments)
	}
}

func newTestACPPreparationStore(t *testing.T, storeDir string) *acpPreparationStore {
	t.Helper()
	store, err := newACPPreparationStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func testPlannedACPPreparation(principalID, operationID, digestFill string) controlagents.ACPPreparation {
	return controlagents.ACPPreparation{
		State: controlagents.PreparationStatePlanned, PrincipalID: principalID, OperationID: operationID,
		IntentDigest: strings.Repeat(digestFill, 64),
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "codex", Launcher: controlagents.LauncherChoiceHosted,
			ModelID: "mimo", CWD: "/workspace",
		},
		ObservedRevision: 7,
	}
}

func testNeedsAuthACPPreparation(planned controlagents.ACPPreparation) controlagents.ACPPreparation {
	planned.State = controlagents.PreparationStateNeedsAuth
	planned.Connection = controlagents.Connection{
		ID: "codex", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindHostedAdapter, AdapterID: "codex"},
	}
	planned.AuthenticationMethods = []controlagents.AuthenticationChallengeMethod{{
		ID: "login", Name: "Log in", Type: controlagents.AuthenticationTerminal,
	}}
	return planned
}

func testReadyACPPreparation(planned controlagents.ACPPreparation) controlagents.ACPPreparation {
	planned.State = controlagents.PreparationStateReady
	planned.Connection = controlagents.Connection{
		ID: "codex", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindHostedAdapter, AdapterID: "codex"},
	}
	if len(planned.AuthenticationMethods) != 0 {
		planned.SelectedAuthentication = controlagents.Authentication{MethodID: "login", Type: controlagents.AuthenticationTerminal}
		planned.Connection.Authentication = planned.SelectedAuthentication
	}
	planned.Discovery = controlagents.DiscoverySnapshot{
		ConnectionID: planned.Connection.ID, LaunchFingerprint: controlagents.LaunchFingerprint(planned.Connection.Launcher),
		SelectedModelID: planned.Request.ModelID, Authentication: planned.SelectedAuthentication,
		DiscoveredAt: planned.CreatedAt.Add(time.Minute),
	}
	return planned
}
