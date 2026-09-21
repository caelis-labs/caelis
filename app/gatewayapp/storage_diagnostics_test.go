package gatewayapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/memory/appliance"
)

func TestInspectStoreDiagnosticsIsReadOnlyAndDistinguishesUnknownHealth(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	controlDir := filepath.Join(root, "control")
	sessionsDir := filepath.Join(root, "sessions")
	memoryDir := filepath.Join(root, "memory", "appliance")
	if err := os.MkdirAll(controlDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(memoryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	const secret = "diagnostic-secret-must-not-be-read"
	for path, data := range map[string][]byte{
		configPath: []byte(`{"configuration_revision":1}`),
		filepath.Join(controlDir, controlStoreDatabaseFile):                  []byte("control"),
		filepath.Join(memoryDir, embeddedMemoryDatabaseFilename):             []byte("SQLite format 3\x00"),
		filepath.Join(memoryDir, embeddedMemoryOwnerLockFilename):            []byte("owner"),
		filepath.Join(memoryDir, embeddedMemoryManagementCredentialFilename): []byte(secret),
	} {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	before, err := os.ReadDir(memoryDir)
	if err != nil {
		t.Fatal(err)
	}

	diagnostics, err := InspectStoreDiagnostics(root)
	if err != nil {
		t.Fatal(err)
	}
	if !diagnostics.ReadOnly {
		t.Fatal("diagnostics are not marked read-only")
	}
	if diagnostics.ConfigState != diagnosticStatePresent || diagnostics.ControlDatabaseState != diagnosticStatePresent || diagnostics.SessionsState != diagnosticStatePresent {
		t.Fatalf("Store states = %#v", diagnostics)
	}
	if diagnostics.Memory.DatabaseState != diagnosticStatePresent || diagnostics.Memory.DatabaseFormat != "sqlite3" {
		t.Fatalf("Memory database state = %#v", diagnostics.Memory)
	}
	if diagnostics.Memory.SchemaState != diagnosticStateUnknown || diagnostics.Memory.ExpectedSchemaVersion != 2 {
		t.Fatalf("Memory schema state = %#v", diagnostics.Memory)
	}
	if diagnostics.Memory.OwnerLockFileState != diagnosticStatePresent || diagnostics.Memory.OwnerLockState != diagnosticLockFree {
		t.Fatalf("Memory owner lock state = %#v", diagnostics.Memory)
	}
	if diagnostics.Memory.ManagementCredentialState != diagnosticStatePresent {
		t.Fatalf("Memory credential state = %#v", diagnostics.Memory)
	}
	encoded, err := json.Marshal(diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"expected_schema_version":2`) {
		t.Fatalf("diagnostics omitted the expected Memory schema: %s", encoded)
	}
	if strings.Contains(string(encoded), secret) {
		t.Fatalf("diagnostics leaked credential content: %s", encoded)
	}
	after, err := os.ReadDir(memoryDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatalf("diagnostics changed Memory directory entries: before=%v after=%v", before, after)
	}
	for i := range before {
		if before[i].Name() != after[i].Name() || before[i].IsDir() != after[i].IsDir() {
			t.Fatalf("diagnostics changed Memory directory: before=%v after=%v", before, after)
		}
	}
	for _, want := range []string{
		"do not remove memoryd.lock; verify the owning process before recovery",
		"schema version is unknown without an owner-held Memory inspection; do not run an ad hoc migration",
	} {
		if !containsString(diagnostics.RecoveryAdvice, want) {
			t.Fatalf("recovery advice = %#v, missing %q", diagnostics.RecoveryAdvice, want)
		}
	}
}

func TestInspectStoreDiagnosticsMatchesEmbeddedMemorySchema(t *testing.T) {
	root := t.TempDir()
	runtime, err := appliance.Open(t.Context(), appliance.Options{DataDir: filepath.Join(root, "memory", "appliance")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Error(err)
		}
	})
	inspection, err := runtime.Management().Inspect(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	diagnostics, err := InspectStoreDiagnostics(root)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics.Memory.ExpectedSchemaVersion != inspection.SchemaVersion {
		t.Fatalf("diagnostic schema = %d, embedded Memory schema = %d", diagnostics.Memory.ExpectedSchemaVersion, inspection.SchemaVersion)
	}
	if diagnostics.Memory.SchemaState != diagnosticStateUnknown {
		t.Fatalf("read-only diagnostics inferred Memory health: %#v", diagnostics.Memory)
	}
}

func TestInspectStoreDiagnosticsDoesNotTreatCorruptMemoryAsHealthy(t *testing.T) {
	root := t.TempDir()
	memoryDir := filepath.Join(root, "memory", "appliance")
	if err := os.MkdirAll(memoryDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(memoryDir, embeddedMemoryDatabaseFilename), []byte("not-a-sqlite-file"), 0o600); err != nil {
		t.Fatal(err)
	}
	diagnostics, err := InspectStoreDiagnostics(root)
	if err != nil {
		t.Fatal(err)
	}
	if diagnostics.Memory.DatabaseState != diagnosticStatePresent || diagnostics.Memory.DatabaseFormat != diagnosticStateInvalid || diagnostics.Memory.SchemaState != diagnosticStateInvalid {
		t.Fatalf("corrupt Memory diagnostics = %#v", diagnostics.Memory)
	}
	if !containsString(diagnostics.RecoveryAdvice, "the embedded Memory database header is not SQLite; preserve the files and inspect the private service log") {
		t.Fatalf("corrupt Memory recovery advice = %#v", diagnostics.RecoveryAdvice)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
