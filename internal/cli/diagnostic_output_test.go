package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/app/gatewayapp"
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

func TestDoctorHealthyStorageDoesNotPrintRecoveryAdvice(t *testing.T) {
	report := doctorResult{
		StorageDiagnostics: &gatewayapp.StoreDiagnostics{
			ConfigState:          "present",
			ControlDatabaseState: "present",
			SessionsState:        "present",
			Memory: gatewayapp.MemoryStorageDiagnostics{
				DatabaseState:  "present",
				DatabaseFormat: "sqlite3",
			},
			RecoveryAdvice: []string{"recovery advice should remain structured only"},
		},
	}
	if output := formatDoctorResult(report); strings.Contains(output, "recovery_advice:") {
		t.Fatalf("healthy doctor output included recovery advice: %s", output)
	}
	report.ServiceState = "unavailable"
	if output := formatDoctorResult(report); !strings.Contains(output, "recovery_advice: recovery advice should remain structured only") {
		t.Fatalf("unavailable doctor output omitted recovery advice: %s", output)
	}
}

func TestAnnotateStartupStorageDiagnosticsOnlyMarksExplicitOwnerContention(t *testing.T) {
	for _, test := range []struct {
		name string
		err  string
	}{
		{name: "secure", err: "secure owner lock: permission denied"},
		{name: "open", err: "open owner lock: permission denied"},
		{name: "acquire", err: "acquire owner lock: input/output error"},
		{name: "truncate", err: "truncate owner lock: read-only file system"},
		{name: "write", err: "write owner lock: no space left on device"},
		{name: "sync", err: "sync owner lock: input/output error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			diagnostics := gatewayapp.StoreDiagnostics{
				Memory: gatewayapp.MemoryStorageDiagnostics{OwnerLockState: "unknown"},
			}
			annotateStartupStorageDiagnostics(&diagnostics, errors.New(test.err))
			if diagnostics.Memory.OwnerLockState != "unknown" {
				t.Fatalf("startup error %q changed owner lock state to %q; want unknown", test.err, diagnostics.Memory.OwnerLockState)
			}
		})
	}

	diagnostics := gatewayapp.StoreDiagnostics{
		Memory: gatewayapp.MemoryStorageDiagnostics{OwnerLockState: "unknown"},
	}
	annotateStartupStorageDiagnostics(&diagnostics, errors.New("open embedded Memory: memory data directory is already owned"))
	if diagnostics.Memory.OwnerLockState != "held" {
		t.Fatalf("explicit owner contention state = %q, want held", diagnostics.Memory.OwnerLockState)
	}
}
