package gatewayapp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRestoreStoreReentersRollbackAfterEachComponentRename(t *testing.T) {
	for _, relative := range []string{
		"config.json",
		filepath.Join("control", controlStoreDatabaseFile),
		"sessions",
	} {
		relative := relative
		t.Run(relative, func(t *testing.T) {
			source := makeStoreBackupFixture(t, "reentry-source")
			var archive bytes.Buffer
			if _, err := WriteStoreBackup(t.Context(), StoreBackupOptions{
				StoreDir: source,
				MemoryBackup: func(_ context.Context, output io.Writer) error {
					_, err := io.WriteString(output, "memory-snapshot")
					return err
				},
			}, &archive); err != nil {
				t.Fatal(err)
			}
			target := makeStoreBackupFixture(t, "reentry-target")
			var err error
			originalDigests := make(map[string]string)
			for _, component := range []string{
				"config.json",
				filepath.Join("control", controlStoreDatabaseFile),
				"sessions",
			} {
				originalDigests[component], err = storeRestorePathDigest(filepath.Join(target, component))
				if err != nil {
					t.Fatalf("capture original %s: %v", component, err)
				}
			}
			injected := false
			setStoreRestoreHooks(t, func(oldPath, newPath string) error {
				if !injected && strings.Contains(filepath.Clean(oldPath), storeRestoreRollbackPrefix) &&
					filepath.Clean(newPath) == filepath.Join(target, relative) {
					if err := os.Rename(oldPath, newPath); err != nil {
						return err
					}
					injected = true
					return errors.New("simulated process exit after rollback rename")
				}
				return os.Rename(oldPath, newPath)
			}, nil)
			_, err = RestoreStore(t.Context(), StoreRestoreOptions{
				StoreDir: target,
				Backup:   bytes.NewReader(archive.Bytes()),
				MemoryRestore: func(context.Context, io.Reader) error {
					return errors.New("stop after component install")
				},
				MemoryCommit:   func(context.Context) error { return nil },
				MemoryRollback: func(context.Context) error { return nil },
			})
			if err == nil || !strings.Contains(err.Error(), "simulated process exit") {
				t.Fatalf("RestoreStore() error = %v, want post-rename crash evidence", err)
			}
			if !injected {
				t.Fatal("rollback rename fault was not injected")
			}
			clearStoreRestoreHooks()
			_, retryErr := RestoreStore(t.Context(), StoreRestoreOptions{
				StoreDir: target,
				Backup:   strings.NewReader("not-an-archive"),
				MemoryRestore: func(context.Context, io.Reader) error {
					t.Fatal("MemoryRestore() ran before archive validation")
					return nil
				},
				MemoryCommit:   func(context.Context) error { t.Fatal("MemoryCommit() ran before archive validation"); return nil },
				MemoryRollback: func(context.Context) error { return nil },
			})
			if retryErr == nil || !strings.Contains(retryErr.Error(), "open Store restore archive") {
				t.Fatalf("RestoreStore() retry error = %v, want archive validation after recovery", retryErr)
			}
			for component, want := range originalDigests {
				if got, err := storeRestorePathDigest(filepath.Join(target, component)); err != nil || got != want {
					t.Fatalf("recovered %s digest = %q, %v; want %q", component, got, err, want)
				}
			}
			if _, err := os.Stat(filepath.Join(target, storeRestoreJournalName)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("restore journal after reentrant recovery = %v", err)
			}
		})
	}
}

func TestPrepareStoreUpgradeRejectsFutureControlSchemaBeforeMemory(t *testing.T) {
	storeDir := stoppedStoreUpgradeFixture(t, "future-control")
	database, _, err := openControlStoreDatabase(storeDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`PRAGMA user_version = 99`); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	assertPrepareStoreUpgradeRejectedBeforeJournal(t, storeDir, "unsupported operation store schema version")
}

func TestPrepareStoreUpgradeRejectsFutureSessionDocumentBeforeMemory(t *testing.T) {
	storeDir := stoppedStoreUpgradeFixture(t, "future-session-document")
	path := filepath.Join(storeDir, "sessions", "rollout-future.json")
	if err := os.WriteFile(path, []byte(`{"kind":"caelis.sdk.session","version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	assertPrepareStoreUpgradeRejectedBeforeJournal(t, storeDir, "unsupported document")
}

func TestPrepareStoreUpgradeRejectsFutureSessionEventBeforeMemory(t *testing.T) {
	storeDir := stoppedStoreUpgradeFixture(t, "future-session-event")
	path := filepath.Join(storeDir, "sessions", "rollout-future.events.jsonl")
	if err := os.WriteFile(path, []byte(`{"schema":99}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertPrepareStoreUpgradeRejectedBeforeJournal(t, storeDir, "future schema")
}

func TestPrepareStoreUpgradeRejectsMalformedSessionDocumentBeforeMemory(t *testing.T) {
	storeDir := stoppedStoreUpgradeFixture(t, "malformed-session-document")
	path := filepath.Join(storeDir, "sessions", "rollout-invalid.json")
	if err := os.WriteFile(path, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertPrepareStoreUpgradeRejectedBeforeJournal(t, storeDir, "decode Session document")
}

func TestPrepareStoreUpgradeRejectsMalformedSessionJSONBeforeMemory(t *testing.T) {
	storeDir := stoppedStoreUpgradeFixture(t, "malformed-session-event")
	path := filepath.Join(storeDir, "sessions", "rollout-invalid.events.jsonl")
	if err := os.WriteFile(path, []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	assertPrepareStoreUpgradeRejectedBeforeJournal(t, storeDir, "decode Session event log")
}

func stoppedStoreUpgradeFixture(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	storeDir := filepath.Join(root, name, "store")
	workspace := filepath.Join(root, name, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "upgrade-p1-test", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "upgrade-p1-workspace", WorkspaceCWD: workspace,
		Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	return storeDir
}

func assertPrepareStoreUpgradeRejectedBeforeJournal(t *testing.T, storeDir, want string) {
	t.Helper()
	if _, err := PrepareStoreUpgrade(t.Context(), storeDir); err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
		t.Fatalf("PrepareStoreUpgrade() error = %v, want %q", err, want)
	}
	if _, err := os.Stat(filepath.Join(storeDir, storeUpgradeJournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("upgrade journal after preflight rejection = %v", err)
	}
}
