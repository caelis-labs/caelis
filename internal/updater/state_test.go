package updater

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseUpdateLockAcceptsJSONAndLegacyTimestamps(t *testing.T) {
	jsonLock, err := json.Marshal(updateLockRecord{PID: 42, LockedAt: time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	record, ok := parseUpdateLock(append(jsonLock, '\n'))
	if !ok || record.PID != 42 || record.LockedAt.UTC().Format(time.RFC3339) != "2026-04-09T12:00:00Z" {
		t.Fatalf("parseUpdateLock(json) = %#v, %v", record, ok)
	}

	legacy, ok := parseUpdateLock([]byte("2026-04-09T12:00:00.000000001Z\n"))
	if !ok || legacy.PID != 0 || legacy.LockedAt.UTC().Format(time.RFC3339Nano) != "2026-04-09T12:00:00.000000001Z" {
		t.Fatalf("parseUpdateLock(legacy) = %#v, %v", legacy, ok)
	}
	if _, ok := parseUpdateLock([]byte("not-a-lock")); ok {
		t.Fatal("parseUpdateLock(invalid) = true, want false")
	}
}

func TestUpdateReclaimsAbandonedOwnerLock(t *testing.T) {
	storeDir := t.TempDir()
	writeUpdateLockFile(t, storeDir, updateLockRecord{PID: 4242, LockedAt: time.Now().UTC()})
	manager := New(Config{
		StoreDir:       storeDir,
		CurrentVersion: "dev",
		ProcessExists:  func(pid int) bool { return pid == os.Getpid() },
	})

	result, err := manager.Update(context.Background(), UpdateOptions{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.Reason == "another update is already running" {
		t.Fatalf("Update() = %#v, want abandoned lock reclaimed", result)
	}
	if _, err := os.Stat(manager.lockPath()); !os.IsNotExist(err) {
		t.Fatalf("abandoned lock remains: %v", err)
	}
}

func TestUpdateReclaimsLegacyTimestampLock(t *testing.T) {
	storeDir := t.TempDir()
	writeLegacyUpdateLock(t, storeDir, time.Now().UTC())
	manager := New(Config{
		StoreDir:       storeDir,
		CurrentVersion: "dev",
	})

	result, err := manager.Update(context.Background(), UpdateOptions{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.Reason == "another update is already running" {
		t.Fatalf("Update() = %#v, want legacy lock reclaimed", result)
	}
}

func TestUpdateReclaimsExpiredOwnerLock(t *testing.T) {
	storeDir := t.TempDir()
	now := time.Date(2026, 4, 9, 12, 0, 0, 0, time.UTC)
	writeUpdateLockFile(t, storeDir, updateLockRecord{PID: 7, LockedAt: now.Add(-updateLockMaxAge)})
	manager := New(Config{
		StoreDir:       storeDir,
		CurrentVersion: "dev",
		Now:            func() time.Time { return now },
		ProcessExists:  func(pid int) bool { return pid == 7 },
	})

	result, err := manager.Update(context.Background(), UpdateOptions{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if result.Reason == "another update is already running" {
		t.Fatalf("Update() = %#v, want expired lock reclaimed", result)
	}
}

func TestUpdateSkipsWhenLockOwnerProcessIsAlive(t *testing.T) {
	storeDir := t.TempDir()
	writeUpdateLockFile(t, storeDir, updateLockRecord{PID: 7, LockedAt: time.Now().UTC()})
	manager := New(Config{
		StoreDir:       storeDir,
		CurrentVersion: "v1.0.0",
		ProcessExists:  func(pid int) bool { return pid == 7 },
	})

	result, err := manager.Update(context.Background(), UpdateOptions{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !result.Skipped || !strings.Contains(result.Reason, "already running") {
		t.Fatalf("Update() = %#v, want running update skip", result)
	}
}

func TestWindowsHandoffLockRecordsParentPID(t *testing.T) {
	globalRoot := t.TempDir()
	packageDir := filepath.Join(globalRoot, "@caelis", "caelis")
	handoffDir := filepath.Join(t.TempDir(), "reserved-handoff")
	manager := New(Config{
		StoreDir:       t.TempDir(),
		CurrentVersion: "v1.0.0",
		Executable:     filepath.Join(packageDir, "runtime", "caelis.exe"),
		GOOS:           "windows",
		PID:            func() int { return 11 },
		ParentPID:      func() int { return 99 },
		ProcessExists:  func(int) bool { return true },
		Env: func(key string) string {
			switch key {
			case EnvInstallMethod:
				return MethodNPM
			case EnvNPMPackageDir:
				return packageDir
			case EnvNPMUpdateHandoffDir:
				return handoffDir
			default:
				return ""
			}
		},
		LookPath: func(name string) (string, error) {
			return `C:\npm\` + name + ".cmd", nil
		},
		CommandOutput: func(_ context.Context, _ string, args []string) ([]byte, error) {
			switch strings.Join(args, " ") {
			case "root -g":
				return []byte(globalRoot + "\n"), nil
			case "view @caelis/caelis version --registry=https://registry.npmjs.org":
				return []byte("1.2.0\n"), nil
			default:
				t.Fatalf("unexpected CommandOutput args: %#v", args)
				return nil, nil
			}
		},
		CommandRun: func(context.Context, string, []string, io.Writer, io.Writer) error {
			t.Fatal("Windows npm handoff must not run npm before the native process exits")
			return nil
		},
		CommandStart: func(string, []string) error {
			t.Fatal("foreground launcher handoff must not schedule a detached update")
			return nil
		},
	})

	result, err := manager.Update(context.Background(), UpdateOptions{})
	if err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if !result.Handoff {
		t.Fatalf("Update() = %#v, want foreground handoff", result)
	}
	data, err := os.ReadFile(manager.lockPath())
	if err != nil {
		t.Fatalf("read handoff update lock: %v", err)
	}
	record, ok := parseUpdateLock(data)
	if !ok || record.PID != 99 {
		t.Fatalf("handoff lock = %#v, want parent pid 99", record)
	}
	ownershipData, err := os.ReadFile(filepath.Join(handoffDir, npmHandoffOwnershipName))
	if err != nil {
		t.Fatalf("read npm handoff ownership: %v", err)
	}
	var ownership npmHandoffOwnership
	if err := json.Unmarshal(ownershipData, &ownership); err != nil {
		t.Fatalf("decode npm handoff ownership: %v", err)
	}
	if ownership.LockToken != strings.TrimSpace(string(data)) {
		t.Fatalf("handoff lock token = %q, want %q", ownership.LockToken, strings.TrimSpace(string(data)))
	}
}

func writeUpdateLockFile(t *testing.T, storeDir string, record updateLockRecord) {
	t.Helper()
	path := filepath.Join(storeDir, "updates", "update.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func writeLegacyUpdateLock(t *testing.T, storeDir string, lockedAt time.Time) {
	t.Helper()
	path := filepath.Join(storeDir, "updates", "update.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(lockedAt.UTC().Format(time.RFC3339Nano)+"\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}
