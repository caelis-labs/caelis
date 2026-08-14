package gatewayapp

import (
	"context"
	"errors"
	"os"
	"os/exec"
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
	if created.Connection.ID != "" || created.Connection.Launcher.Command != "" {
		t.Fatalf("planned preparation resolved the launcher before persistence: %#v", created.Connection)
	}

	needsAuth := testNeedsAuthACPPreparation(created)
	saved, err := store.Save(context.Background(), created.ContentDigest, needsAuth)
	if err != nil {
		t.Fatal(err)
	}
	if saved.State != controlagents.PreparationStateNeedsAuth || saved.ContentDigest == created.ContentDigest {
		t.Fatalf("Save(needs_auth) = %#v", saved)
	}
	if _, err := store.Save(context.Background(), created.ContentDigest, needsAuth); !errors.Is(err, errACPPreparationConflict) {
		t.Fatalf("Save(stale digest) error = %v", err)
	}

	wrongOwner := saved
	wrongOwner.PrincipalID = "another-principal"
	if _, err := store.Save(context.Background(), saved.ContentDigest, wrongOwner); !errors.Is(err, errACPPreparationOwner) {
		t.Fatalf("Save(changed owner) error = %v", err)
	}
	wrongRequest := saved
	wrongRequest.Request.ModelID = "another-model"
	if _, err := store.Save(context.Background(), saved.ContentDigest, wrongRequest); !errors.Is(err, errACPPreparationOwner) {
		t.Fatalf("Save(changed request) error = %v", err)
	}

	ready := testReadyACPPreparation(saved)
	saved, err = store.Save(context.Background(), saved.ContentDigest, ready)
	if err != nil {
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
	info, err := os.Stat(store.path(saved.Ref))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Fatalf("preparation permissions = %04o, want 0600", info.Mode().Perm())
	}
}

func TestACPPreparationStoreFindByIntent(t *testing.T) {
	t.Parallel()

	t.Run("match and principal isolation", func(t *testing.T) {
		store := newTestACPPreparationStore(t, t.TempDir())
		created, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "b"))
		if err != nil {
			t.Fatal(err)
		}
		created, err = store.Save(context.Background(), created.ContentDigest, testNeedsAuthACPPreparation(created))
		if err != nil {
			t.Fatal(err)
		}
		found, ok, err := store.FindByIntent(context.Background(), created.PrincipalID, created.OperationID, created.IntentDigest)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			t.Fatal("FindByIntent() did not find the matching preparation")
		}
		if !reflect.DeepEqual(found, created) {
			t.Fatalf("FindByIntent() = %#v, want %#v", found, created)
		}
		if _, ok, err := store.FindByIntent(context.Background(), "another-principal", created.OperationID, created.IntentDigest); err != nil || ok {
			t.Fatalf("FindByIntent(other principal) found=%v error=%v", ok, err)
		}
	})

	t.Run("expired is skipped", func(t *testing.T) {
		store := newTestACPPreparationStore(t, t.TempDir())
		now := time.Now().UTC().Round(0)
		store.now = func() time.Time { return now }
		created, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "c"))
		if err != nil {
			t.Fatal(err)
		}
		now = created.ExpiresAt.Add(time.Nanosecond)
		if _, ok, err := store.FindByIntent(context.Background(), created.PrincipalID, created.OperationID, created.IntentDigest); err != nil || ok {
			t.Fatalf("FindByIntent(expired) found=%v error=%v", ok, err)
		}
	})

	t.Run("duplicate fails closed", func(t *testing.T) {
		store := newTestACPPreparationStore(t, t.TempDir())
		record := testPlannedACPPreparation("principal", "operation", "d")
		first, err := store.CreatePlanned(context.Background(), record)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.CreatePlanned(context.Background(), record); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.FindByIntent(context.Background(), first.PrincipalID, first.OperationID, first.IntentDigest); !errors.Is(err, errACPPreparationAmbiguous) {
			t.Fatalf("FindByIntent(duplicate) error = %v", err)
		}
	})

	t.Run("corruption is not swallowed", func(t *testing.T) {
		store := newTestACPPreparationStore(t, t.TempDir())
		created, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "e"))
		if err != nil {
			t.Fatal(err)
		}
		corrupt := filepath.Join(store.root, strings.Repeat("f", 64)+".json")
		if err := os.WriteFile(corrupt, []byte("not-json"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := store.FindByIntent(context.Background(), created.PrincipalID, created.OperationID, created.IntentDigest); err == nil || !strings.Contains(err.Error(), "decode") {
			t.Fatalf("FindByIntent(corrupt record) error = %v", err)
		}
	})
}

func TestACPPreparationStoreExactCASConcurrent(t *testing.T) {
	t.Parallel()

	storeDir := t.TempDir()
	creator := newTestACPPreparationStore(t, storeDir)
	created, err := creator.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "f"))
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
			store := newTestACPPreparationStore(t, storeDir)
			_, err := store.Save(context.Background(), created.ContentDigest, ready)
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

	storeDir := t.TempDir()
	store := newTestACPPreparationStore(t, storeDir)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.CreatePlanned(canceled, testPlannedACPPreparation("principal", "operation", "1")); !errors.Is(err, context.Canceled) {
		t.Fatalf("CreatePlanned(canceled) error = %v", err)
	}
	if _, err := os.Stat(store.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("canceled CreatePlanned created root: %v", err)
	}

	now := time.Now().UTC().Round(0)
	store.now = func() time.Time { return now }
	created, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "2"))
	if err != nil {
		t.Fatal(err)
	}
	now = created.ExpiresAt
	if _, err := store.Get(context.Background(), created.Ref); !errors.Is(err, errACPPreparationExpired) {
		t.Fatalf("Get(expired) error = %v", err)
	}
	if _, err := store.Save(context.Background(), created.ContentDigest, testReadyACPPreparation(created)); !errors.Is(err, errACPPreparationExpired) {
		t.Fatalf("Save(expired) error = %v", err)
	}
	if _, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation-next", "3")); err != nil {
		t.Fatalf("CreatePlanned(after expiry) error = %v", err)
	}
	if _, err := os.Stat(store.path(created.Ref)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired preparation was not pruned: %v", err)
	}
}

func TestACPPreparationStoreFailsClosedOnSymlinkAndPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission and symlink semantics")
	}

	t.Run("symlink", func(t *testing.T) {
		store := newTestACPPreparationStore(t, t.TempDir())
		created, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "3"))
		if err != nil {
			t.Fatal(err)
		}
		path := store.path(created.Ref)
		target := filepath.Join(t.TempDir(), "target.json")
		if err := os.WriteFile(target, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(context.Background(), created.Ref); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("Get(symlink) error = %v", err)
		}
		if _, err := store.Save(context.Background(), created.ContentDigest, testReadyACPPreparation(created)); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("Save(symlink) error = %v", err)
		}
		payload, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if string(payload) != "preserve" {
			t.Fatalf("symlink target changed: %q", payload)
		}
	})

	t.Run("permissions", func(t *testing.T) {
		store := newTestACPPreparationStore(t, t.TempDir())
		created, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "4"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(store.path(created.Ref), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Get(context.Background(), created.Ref); err == nil || !strings.Contains(err.Error(), "unsafe permissions") {
			t.Fatalf("Get(unsafe permissions) error = %v", err)
		}
	})
}

func TestACPPreparationStoreCrossProcessLock(t *testing.T) {
	if os.Getenv("CAELIS_ACP_PREPARATION_LOCK_HELPER") == "1" {
		runACPPreparationLockHelper()
		return
	}

	storeDir := t.TempDir()
	store := newTestACPPreparationStore(t, storeDir)
	created, err := store.CreatePlanned(context.Background(), testPlannedACPPreparation("principal", "operation", "5"))
	if err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(t.TempDir(), "ready")
	releasePath := filepath.Join(t.TempDir(), "release")
	command := exec.Command(os.Args[0], "-test.run=^TestACPPreparationStoreCrossProcessLock$")
	command.Env = append(os.Environ(),
		"CAELIS_ACP_PREPARATION_LOCK_HELPER=1",
		"CAELIS_ACP_PREPARATION_STORE_DIR="+storeDir,
		"CAELIS_ACP_PREPARATION_READY="+readyPath,
		"CAELIS_ACP_PREPARATION_RELEASE="+releasePath,
	)
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		_ = os.WriteFile(releasePath, []byte("release"), 0o600)
		if !waited {
			_ = command.Wait()
		}
	}()
	waitForACPPreparationPath(t, readyPath, 5*time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := store.Get(ctx, created.Ref); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Get(held cross-process lock) error = %v", err)
	}
	if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err != nil {
		t.Fatal(err)
	}
	waited = true
}

func runACPPreparationLockHelper() {
	store, err := newACPPreparationStore(os.Getenv("CAELIS_ACP_PREPARATION_STORE_DIR"))
	if err != nil {
		os.Exit(2)
	}
	if err := ensureACPPreparationRoot(store.root); err != nil {
		os.Exit(3)
	}
	lock, err := acquireACPPreparationFileLock(context.Background(), filepath.Join(store.root, acpPreparationLockFilename))
	if err != nil {
		os.Exit(4)
	}
	defer lock.Close()
	if err := os.WriteFile(os.Getenv("CAELIS_ACP_PREPARATION_READY"), []byte("ready"), 0o600); err != nil {
		os.Exit(5)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(os.Getenv("CAELIS_ACP_PREPARATION_RELEASE")); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	os.Exit(6)
}

func waitForACPPreparationPath(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func newTestACPPreparationStore(t *testing.T, storeDir string) *acpPreparationStore {
	t.Helper()
	if runtime.GOOS != "windows" {
		if err := os.Chmod(storeDir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	store, err := newACPPreparationStore(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testPlannedACPPreparation(principalID, operationID, digestFill string) controlagents.ACPPreparation {
	return controlagents.ACPPreparation{
		State:        controlagents.PreparationStatePlanned,
		PrincipalID:  principalID,
		OperationID:  operationID,
		IntentDigest: strings.Repeat(digestFill, 64),
		Request: controlagents.ACPPrepareRequest{
			AdapterID: "codex", Launcher: controlagents.LauncherChoiceNPX, ModelID: "mimo", CWD: "/workspace",
		},
		ObservedRevision: 7,
	}
}

func testNeedsAuthACPPreparation(planned controlagents.ACPPreparation) controlagents.ACPPreparation {
	planned.State = controlagents.PreparationStateNeedsAuth
	planned.Connection = controlagents.Connection{
		ID: "codex", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindPackageExec, Command: "npx", Args: []string{"codex-acp"}},
	}
	planned.AuthenticationMethods = []controlagents.AuthenticationChallengeMethod{{
		ID: "login", Name: "Log in", Type: controlagents.AuthenticationTerminal,
	}}
	return planned
}

func testReadyACPPreparation(planned controlagents.ACPPreparation) controlagents.ACPPreparation {
	planned.State = controlagents.PreparationStateReady
	planned.Connection = controlagents.Connection{
		ID: "codex", Launcher: controlagents.Launcher{Kind: controlagents.LaunchKindPackageExec, Command: "npx", Args: []string{"codex-acp"}},
	}
	if len(planned.AuthenticationMethods) != 0 {
		planned.SelectedAuthentication = controlagents.Authentication{MethodID: "login", Type: controlagents.AuthenticationTerminal}
		planned.Connection.Authentication = planned.SelectedAuthentication
	}
	planned.Discovery = controlagents.DiscoverySnapshot{
		ConnectionID:      planned.Connection.ID,
		LaunchFingerprint: controlagents.LaunchFingerprint(planned.Connection.Launcher),
		SelectedModelID:   planned.Request.ModelID,
		Authentication:    planned.SelectedAuthentication,
		DiscoveredAt:      planned.CreatedAt.Add(time.Minute),
	}
	return planned
}
