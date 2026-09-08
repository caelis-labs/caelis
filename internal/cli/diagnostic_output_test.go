package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDoctorStartupFailureIncludesReadOnlyStorageDiagnostics(t *testing.T) {
	root := t.TempDir()
	memoryDir := filepath.Join(root, "memory", "appliance")
	if err := os.MkdirAll(memoryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, "memoryd.lock"), []byte("lock"), 0o600); err != nil {
		t.Fatal(err)
	}
	result := doctorResultFromStartupFailure(root, true, errors.New("ignored"))
	if result.StorageDiagnostics == nil || !result.StorageDiagnostics.ReadOnly {
		t.Fatalf("startup diagnostics = %#v", result.StorageDiagnostics)
	}
	if result.StorageDiagnostics.Memory.OwnerLockFileState != "present" {
		t.Fatalf("startup Memory diagnostics = %#v", result.StorageDiagnostics.Memory)
	}
	if result.StorageDiagnostics.Memory.DatabaseState != "missing" {
		t.Fatalf("startup Memory database state = %#v", result.StorageDiagnostics.Memory)
	}
	text := formatDoctorResult(result)
	for _, want := range []string{
		"storage_diagnostics_read_only: true",
		"memory_database_state: missing",
		"memory_owner_lock_file_state: present",
		"recovery_advice:",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output missing %q:\n%s", want, text)
		}
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ignored") {
		t.Fatalf("doctor startup diagnostics copied raw startup error: %s", raw)
	}
}
