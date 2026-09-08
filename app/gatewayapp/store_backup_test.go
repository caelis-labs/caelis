package gatewayapp

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/internal/hostownership"
)

func TestWriteStoreBackupPublishesManifestAndExcludesSecrets(t *testing.T) {
	storeDir := makeStoreBackupFixture(t, "source")
	var archive bytes.Buffer
	manifest, err := WriteStoreBackup(t.Context(), StoreBackupOptions{
		StoreDir: storeDir,
		MemoryBackup: func(_ context.Context, output io.Writer) error {
			_, err := io.WriteString(output, "SQLite format 3\\x00-memory")
			return err
		},
	}, &archive)
	if err != nil {
		t.Fatalf("WriteStoreBackup() error = %v", err)
	}
	if manifest.Format != StoreBackupFormat || manifest.MinimumReader != StoreBackupMinimumReader {
		t.Fatalf("manifest identity = %#v", manifest)
	}
	if _, err := ReadStoreBackupManifest(bytes.NewReader(archive.Bytes()), int64(archive.Len())); err != nil {
		t.Fatalf("ReadStoreBackupManifest() error = %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range reader.File {
		if strings.Contains(entry.Name, "providers") || strings.Contains(entry.Name, "management.token") || strings.Contains(entry.Name, "steward-worker.token") {
			t.Fatalf("backup leaked secret path %q", entry.Name)
		}
	}
	if len(manifest.Components) < 4 {
		t.Fatalf("manifest components = %d, want config/control/session/Memory", len(manifest.Components))
	}
}

func TestStackWriteStoreBackupUsesQuiescedOwnerSnapshot(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "backup-test", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "backup-workspace", WorkspaceCWD: workspace,
		Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	manifest, backupErr := stack.WriteStoreBackup(t.Context(), &archive)
	closeErr := stack.Close()
	if backupErr != nil {
		t.Fatalf("Stack.WriteStoreBackup() error = %v", backupErr)
	}
	if closeErr != nil {
		t.Fatalf("Stack.Close() after Store backup error = %v", closeErr)
	}
	if _, err := ReadStoreBackupManifest(bytes.NewReader(archive.Bytes()), int64(archive.Len())); err != nil {
		t.Fatalf("ReadStoreBackupManifest() error = %v", err)
	}
	if manifest.QuiesceBoundary != "host-admission-closed-and-all-producers-drained" {
		t.Fatalf("Stack backup quiesce boundary = %q", manifest.QuiesceBoundary)
	}
	reader, err := zip.NewReader(bytes.NewReader(archive.Bytes()), int64(archive.Len()))
	if err != nil {
		t.Fatal(err)
	}
	memory := findZipEntry(reader, storeBackupComponentRoot+"memory/memory.db")
	if memory == nil {
		t.Fatal("Stack backup omitted the Memory owner snapshot")
	}
	rawMemory, err := readZipEntry(memory)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(rawMemory, []byte("SQLite format 3\x00")) {
		t.Fatalf("Memory owner snapshot header = %q", rawMemory[:min(16, len(rawMemory))])
	}
}

func TestWriteStoreBackupDoesNotPublishPartialArchiveOnMemoryFailure(t *testing.T) {
	storeDir := makeStoreBackupFixture(t, "failed")
	var archive bytes.Buffer
	wantErr := errors.New("owner snapshot failed")
	if _, err := WriteStoreBackup(t.Context(), StoreBackupOptions{
		StoreDir:     storeDir,
		MemoryBackup: func(context.Context, io.Writer) error { return wantErr },
	}, &archive); !errors.Is(err, wantErr) {
		t.Fatalf("WriteStoreBackup() error = %v, want %v", err, wantErr)
	}
	if archive.Len() != 0 {
		t.Fatalf("failed backup published %d bytes", archive.Len())
	}
}

func TestWriteStoreBackupRejectsUnknownConfigSchemaBeforeOwnerSnapshot(t *testing.T) {
	storeDir := makeStoreBackupFixture(t, "future-config")
	if err := os.WriteFile(filepath.Join(storeDir, "config.json"), []byte(`{"schema_version":99}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	ownerCalled := false
	if _, err := WriteStoreBackup(t.Context(), StoreBackupOptions{
		StoreDir: storeDir,
		MemoryBackup: func(context.Context, io.Writer) error {
			ownerCalled = true
			return nil
		},
	}, &archive); err == nil || !strings.Contains(err.Error(), "unsupported Store AppConfig schema version") {
		t.Fatalf("WriteStoreBackup() error = %v, want unsupported schema", err)
	}
	if ownerCalled || archive.Len() != 0 {
		t.Fatalf("unknown config published ownerCalled=%v archiveBytes=%d", ownerCalled, archive.Len())
	}
}

func TestValidateStoreBackupManifestRejectsKindPathMismatch(t *testing.T) {
	base := StoreBackupManifest{
		Format:          StoreBackupFormat,
		MinimumReader:   StoreBackupMinimumReader,
		QuiesceBoundary: "host-admission-closed-and-all-producers-drained",
		Atomicity:       "independent-component-snapshots-under-one-quiesce-boundary",
	}
	tests := []struct {
		name string
		kind string
		path string
	}{
		{name: "memory path", kind: "memory-owner-snapshot", path: storeBackupComponentRoot + "other.db"},
		{name: "control path", kind: "sqlite", path: storeBackupComponentRoot + "control.sqlite"},
		{name: "config path", kind: "config", path: storeBackupComponentRoot + "sessions/config"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := base
			manifest.Components = []StoreBackupComponent{
				{ArchivePath: test.path, Kind: test.kind, Size: 0, SHA256: strings.Repeat("0", 64), Required: true},
				{ArchivePath: storeBackupComponentRoot + "config", Kind: "config", Size: 0, SHA256: strings.Repeat("0", 64), Required: true},
				{ArchivePath: storeBackupComponentRoot + "control", Kind: "sqlite", Size: 0, SHA256: strings.Repeat("0", 64), Required: true},
				{ArchivePath: storeBackupComponentRoot + "memory/memory.db", Kind: "memory-owner-snapshot", Size: 0, SHA256: strings.Repeat("0", 64), Required: true},
				{ArchivePath: storeBackupComponentRoot + "sessions/", Kind: "session-canonical", Size: 0, SHA256: strings.Repeat("0", 64), Required: true},
			}
			if err := validateStoreBackupManifest(manifest); err == nil {
				t.Fatalf("validateStoreBackupManifest() accepted %s at %s", test.kind, test.path)
			}
		})
	}
}

func TestRestoreStoreRejectsSessionControlFilesFromArchive(t *testing.T) {
	for _, relative := range []string{".sessions.lock", ".sessions.transactions.pending", "rollout.tmp"} {
		if validStoreSessionRelativePath(relative) {
			t.Fatalf("validStoreSessionRelativePath(%q) = true", relative)
		}
	}
	if !validStoreSessionRelativePath(".sessions.index.sqlite") {
		t.Fatal("Session lookup index was rejected")
	}
}

func TestRestoreStoreReplacesAuthoritiesAndCommitsMemoryLast(t *testing.T) {
	source := makeStoreBackupFixture(t, "source")
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
	target := makeStoreBackupFixture(t, "target")
	if err := os.WriteFile(filepath.Join(target, "config.json"), []byte("old-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "sessions", "stale.jsonl"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	var restoredMemory []byte
	committed := false
	report, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir: target,
		Backup:   bytes.NewReader(archive.Bytes()),
		MemoryRestore: func(_ context.Context, input io.Reader) error {
			var err error
			restoredMemory, err = io.ReadAll(input)
			return err
		},
		MemoryCommit: func(context.Context) error { committed = true; return nil },
		MemoryRollback: func(context.Context) error {
			t.Fatalf("MemoryRollback() called after successful restore")
			return nil
		},
	})
	if err != nil {
		t.Fatalf("RestoreStore() error = %v", err)
	}
	if !committed || string(restoredMemory) != "memory-snapshot" {
		t.Fatalf("Memory lifecycle committed=%v snapshot=%q", committed, restoredMemory)
	}
	if got := readStoreBackupFile(t, filepath.Join(target, "config.json")); got != `{"schema_version":2}` {
		t.Fatalf("restored config = %q", got)
	}
	if got := readStoreBackupFile(t, controlStoreDatabasePath(target)); got != "control-source" {
		t.Fatalf("restored Control = %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "sessions", "stale.jsonl")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale Session survived restore: %v", err)
	}
	if got := readStoreBackupFile(t, filepath.Join(target, "sessions", "accepted.jsonl")); got != "accepted-source" {
		t.Fatalf("restored Session = %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, storeRestoreJournalName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore journal remains: %v", err)
	}
	if matches, err := filepath.Glob(filepath.Join(filepath.Dir(target), storeRestoreRollbackPrefix+"*")); err != nil || len(matches) != 0 {
		t.Fatalf("rollback directories remain: %v %v", matches, err)
	}
	if report.RolledBackOnFailure {
		t.Fatal("successful restore reported rollback")
	}
}

func TestRestoreStoreRejectsUnknownConfigSchemaBeforeReplacingTarget(t *testing.T) {
	source := makeStoreBackupFixture(t, "source-future-config")
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
	archive = rewriteStoreBackupConfig(t, archive.Bytes(), []byte(`{"schema_version":99}`))
	target := makeStoreBackupFixture(t, "target-future-config")
	wantConfig := readStoreBackupFile(t, filepath.Join(target, "config.json"))
	memoryCalled := false
	if _, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir: target,
		Backup:   bytes.NewReader(archive.Bytes()),
		MemoryRestore: func(context.Context, io.Reader) error {
			memoryCalled = true
			return nil
		},
		MemoryCommit:   func(context.Context) error { return nil },
		MemoryRollback: func(context.Context) error { return nil },
	}); err == nil || !strings.Contains(err.Error(), "invalid config component") {
		t.Fatalf("RestoreStore() error = %v, want invalid config component", err)
	}
	if memoryCalled {
		t.Fatal("MemoryRestore() ran for an archive rejected during staging")
	}
	if got := readStoreBackupFile(t, filepath.Join(target, "config.json")); got != wantConfig {
		t.Fatalf("target config changed after rejected archive: got %q want %q", got, wantConfig)
	}
}

func TestRestoreStoreRollsBackWhenMemoryCommitFails(t *testing.T) {
	source := makeStoreBackupFixture(t, "source")
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
	target := makeStoreBackupFixture(t, "target")
	if err := os.WriteFile(filepath.Join(target, "config.json"), []byte("old-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	rolledBack := false
	report, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir:      target,
		Backup:        bytes.NewReader(archive.Bytes()),
		MemoryRestore: func(context.Context, io.Reader) error { return nil },
		MemoryCommit:  func(context.Context) error { return errors.New("commit rejected") },
		MemoryRollback: func(context.Context) error {
			rolledBack = true
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "commit Memory owner restore") {
		t.Fatalf("RestoreStore() error = %v", err)
	}
	if !rolledBack || !report.RolledBackOnFailure {
		t.Fatalf("rollback state = rolledBack:%v report:%#v", rolledBack, report)
	}
	if got := readStoreBackupFile(t, filepath.Join(target, "config.json")); got != "old-config" {
		t.Fatalf("config after rollback = %q", got)
	}
}

func TestRestoreStoreRejectsLiveHostOwner(t *testing.T) {
	storeDir := makeStoreBackupFixture(t, "owner")
	owner, err := hostownership.Acquire(t.Context(), storeDir)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err = RestoreStore(ctx, StoreRestoreOptions{
		StoreDir:       storeDir,
		Backup:         strings.NewReader("not-an-archive"),
		MemoryRestore:  func(context.Context, io.Reader) error { return nil },
		MemoryCommit:   func(context.Context) error { return nil },
		MemoryRollback: func(context.Context) error { return nil },
	})
	if err == nil {
		t.Fatal("RestoreStore() accepted a live Host owner")
	}
}

func TestRestoreStoreRejectsSymlinkedAuthorityTarget(t *testing.T) {
	source := makeStoreBackupFixture(t, "symlink-source")
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
	target := makeStoreBackupFixture(t, "symlink-target")
	outside := filepath.Join(t.TempDir(), "outside-control")
	if err := os.MkdirAll(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(target, "control")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(target, "control")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir:      target,
		Backup:        bytes.NewReader(archive.Bytes()),
		MemoryRestore: func(context.Context, io.Reader) error { t.Fatal("MemoryRestore() ran for symlink target"); return nil },
		MemoryCommit:  func(context.Context) error { return nil },
		MemoryRollback: func(context.Context) error {
			t.Fatal("MemoryRollback() ran for symlink target")
			return nil
		},
	}); err == nil || !strings.Contains(err.Error(), "Control directory") {
		t.Fatalf("RestoreStore() error = %v, want symlinked Control rejection", err)
	}
	if _, err := os.Stat(filepath.Join(outside, "control.sqlite")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("outside Control target changed: %v", err)
	}
}

func TestPrepareStoreUpgradeGatesNewEmbeddedHostUntilRollback(t *testing.T) {
	root := t.TempDir()
	storeDir := filepath.Join(root, "store")
	workspace := filepath.Join(root, "workspace")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	stack, err := newGatewayAppTestStack(t, Config{
		AppName: "upgrade-test", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "upgrade-workspace", WorkspaceCWD: workspace,
		Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.Close(); err != nil {
		t.Fatal(err)
	}
	prepared, err := PrepareStoreUpgrade(t.Context(), storeDir)
	if err != nil {
		t.Fatalf("PrepareStoreUpgrade() error = %v", err)
	}
	if !prepared.RollbackAvailable || prepared.SourceGeneration == "" {
		t.Fatalf("PrepareStoreUpgrade() report = %+v", prepared)
	}
	_, err = NewLocalStack(Config{
		AppName: "upgrade-test", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "upgrade-workspace", WorkspaceCWD: workspace,
		Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err == nil || !strings.Contains(err.Error(), "restored generation awaits operator commit") {
		t.Fatalf("NewLocalStack() while upgrade is pending error = %v", err)
	}
	rolledBack, err := RollbackStoreUpgrade(t.Context(), storeDir)
	if err != nil {
		t.Fatalf("RollbackStoreUpgrade() error = %v", err)
	}
	if rolledBack.RollbackAvailable || rolledBack.StorageGeneration == prepared.StorageGeneration {
		t.Fatalf("RollbackStoreUpgrade() report = %+v; prepared = %+v", rolledBack, prepared)
	}
	reopened, err := newGatewayAppTestStack(t, Config{
		AppName: "upgrade-test", UserID: "local-user", StoreDir: storeDir,
		WorkspaceKey: "upgrade-workspace", WorkspaceCWD: workspace,
		Sandbox: SandboxConfig{RequestedType: "host"},
	})
	if err != nil {
		t.Fatalf("NewLocalStack() after upgrade rollback error = %v", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRestoreStorePreservesAnEmptyCanonicalSessionDirectory(t *testing.T) {
	source := makeStoreBackupFixture(t, "source")
	if err := os.Remove(filepath.Join(source, "sessions", "accepted.jsonl")); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	if _, err := WriteStoreBackup(t.Context(), StoreBackupOptions{
		StoreDir: source,
		MemoryBackup: func(_ context.Context, output io.Writer) error {
			_, err := io.WriteString(output, "memory")
			return err
		},
	}, &archive); err != nil {
		t.Fatal(err)
	}
	target := makeStoreBackupFixture(t, "target")
	var memoryRestored bool
	if _, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir: target,
		Backup:   bytes.NewReader(archive.Bytes()),
		MemoryRestore: func(_ context.Context, input io.Reader) error {
			_, err := io.ReadAll(input)
			memoryRestored = err == nil
			return err
		},
		MemoryCommit:   func(context.Context) error { return nil },
		MemoryRollback: func(context.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	if !memoryRestored {
		t.Fatal("Memory restore was not called")
	}
	entries, err := os.ReadDir(filepath.Join(target, "sessions"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("restored empty Session directory has entries: %#v", entries)
	}
}

func TestRestoreStoreRecoversInterruptedComponentSwap(t *testing.T) {
	storeDir := makeStoreBackupFixture(t, "interrupted")
	rollbackDir := filepath.Join(filepath.Dir(storeDir), storeRestoreRollbackPrefix+"interrupted")
	if err := os.MkdirAll(filepath.Join(rollbackDir, "config"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(storeDir, "config.json"), filepath.Join(rollbackDir, "config.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(storeDir, "config.json"), []byte("partial-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	stageDir := filepath.Join(filepath.Dir(storeDir), storeRestoreStagePrefix+"interrupted")
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeStoreRestoreJournal(storeDir, storeRestoreJournal{
		Format: StoreBackupFormat, State: "applying", StoreDir: storeDir,
		StageDir: stageDir, RollbackDir: rollbackDir, Applied: []string{"config.json"},
	}); err != nil {
		t.Fatal(err)
	}
	source := makeStoreBackupFixture(t, "source")
	var archive bytes.Buffer
	if _, err := WriteStoreBackup(t.Context(), StoreBackupOptions{
		StoreDir: source,
		MemoryBackup: func(_ context.Context, output io.Writer) error {
			_, err := io.WriteString(output, "memory")
			return err
		},
	}, &archive); err != nil {
		t.Fatal(err)
	}
	report, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir:       storeDir,
		Backup:         bytes.NewReader(archive.Bytes()),
		MemoryRestore:  func(context.Context, io.Reader) error { return nil },
		MemoryCommit:   func(context.Context) error { return nil },
		MemoryRollback: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.RecoveredPrevious {
		t.Fatal("restore did not report interrupted-swap recovery")
	}
}

func TestRestoreStoreCleansCommittedJournalAfterFinalizationInterruption(t *testing.T) {
	storeDir := makeStoreBackupFixture(t, "committed")
	rollbackDir := filepath.Join(filepath.Dir(storeDir), storeRestoreRollbackPrefix+"committed")
	stageDir := filepath.Join(filepath.Dir(storeDir), storeRestoreStagePrefix+"committed")
	if err := os.MkdirAll(rollbackDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(stageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeStoreRestoreJournal(storeDir, storeRestoreJournal{
		Format: StoreBackupFormat, State: "committed", StoreDir: storeDir,
		StageDir: stageDir, RollbackDir: rollbackDir,
	}); err != nil {
		t.Fatal(err)
	}
	source := makeStoreBackupFixture(t, "source")
	var archive bytes.Buffer
	if _, err := WriteStoreBackup(t.Context(), StoreBackupOptions{
		StoreDir: source,
		MemoryBackup: func(_ context.Context, output io.Writer) error {
			_, err := io.WriteString(output, "memory")
			return err
		},
	}, &archive); err != nil {
		t.Fatal(err)
	}
	report, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir:       storeDir,
		Backup:         bytes.NewReader(archive.Bytes()),
		MemoryRestore:  func(context.Context, io.Reader) error { return nil },
		MemoryCommit:   func(context.Context) error { return nil },
		MemoryRollback: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.RecoveredPrevious {
		t.Fatal("restore did not clean committed journal")
	}
}

func TestRestoreStoreRejectsJournalRecoveryPathOutsideStore(t *testing.T) {
	storeDir := makeStoreBackupFixture(t, "malicious-journal")
	outside := filepath.Join(filepath.Dir(storeDir), "must-survive")
	if err := os.WriteFile(outside, []byte("sentinel"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeStoreRestoreJournal(storeDir, storeRestoreJournal{
		Format: StoreBackupFormat, State: "committed", StoreDir: storeDir,
		StageDir: outside, RollbackDir: outside,
	}); err != nil {
		t.Fatal(err)
	}
	_, err := RestoreStore(t.Context(), StoreRestoreOptions{
		StoreDir:      storeDir,
		Backup:        strings.NewReader("not-an-archive"),
		MemoryRestore: func(context.Context, io.Reader) error { return nil },
		MemoryCommit:  func(context.Context) error { return nil },
		MemoryRollback: func(context.Context) error {
			t.Fatal("MemoryRollback() ran for a rejected journal")
			return nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "recovery path is outside") {
		t.Fatalf("RestoreStore() error = %v, want rejected journal path", err)
	}
	if got := readStoreBackupFile(t, outside); got != "sentinel" {
		t.Fatalf("outside journal target changed: %q", got)
	}
}

func makeStoreBackupFixture(t *testing.T, name string) string {
	t.Helper()
	storeDir := filepath.Join(t.TempDir(), name)
	if err := os.MkdirAll(filepath.Join(storeDir, "control"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(storeDir, "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(storeDir, "config.json"):                             `{"schema_version":2}`,
		controlStoreDatabasePath(storeDir):                                 "control-" + name,
		filepath.Join(storeDir, "sessions", "accepted.jsonl"):              "accepted-" + name,
		filepath.Join(storeDir, "providers", "openai.key"):                 "provider-secret",
		filepath.Join(storeDir, "memory", "appliance", "management.token"): "management-secret",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return storeDir
}

func readStoreBackupFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func rewriteStoreBackupConfig(t *testing.T, archiveBytes, config []byte) bytes.Buffer {
	t.Helper()
	reader, err := zip.NewReader(bytes.NewReader(archiveBytes), int64(len(archiveBytes)))
	if err != nil {
		t.Fatal(err)
	}
	manifestEntry := findZipEntry(reader, storeBackupManifestName)
	if manifestEntry == nil {
		t.Fatal("archive manifest is missing")
	}
	var manifest StoreBackupManifest
	rawManifest, err := readZipEntry(manifestEntry)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawManifest, &manifest); err != nil {
		t.Fatal(err)
	}
	for index := range manifest.Components {
		if manifest.Components[index].ArchivePath == storeBackupComponentRoot+"config" {
			manifest.Components[index].Size = int64(len(config))
			manifest.Components[index].SHA256 = sha256Bytes(config)
		}
	}
	updatedManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	var rewritten bytes.Buffer
	writer := zip.NewWriter(&rewritten)
	for _, entry := range reader.File {
		output, err := writer.Create(entry.Name)
		if err != nil {
			t.Fatal(err)
		}
		var raw []byte
		switch entry.Name {
		case storeBackupComponentRoot + "config":
			raw = config
		case storeBackupManifestName:
			raw = append(updatedManifest, '\n')
		default:
			raw, err = readZipEntry(entry)
			if err != nil {
				t.Fatal(err)
			}
		}
		if _, err := output.Write(raw); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return rewritten
}
