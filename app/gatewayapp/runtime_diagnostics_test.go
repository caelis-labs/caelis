package gatewayapp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestRuntimeDiagnosticsLoggerWritesPrivateJSONLFile(t *testing.T) {
	root := t.TempDir()
	logger := newRuntimeDiagnosticsLogger(root)
	logger.Warn(
		"session file operation",
		"component", "session_file",
		"operation", "replace_document",
		"error_class", "sharing_violation",
		"win32_code", 32,
		"attempts", 2,
		"outcome", "recovered",
		"path_class", "local",
	)

	path := filepath.Join(root, "logs", runtimeDiagnosticsFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(runtime diagnostics) error = %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("Unmarshal(runtime diagnostics) error = %v; data = %q", err, data)
	}
	if record["component"] != "session_file" || record["operation"] != "replace_document" || record["outcome"] != "recovered" {
		t.Fatalf("runtime diagnostic = %#v, want fixed environment fields", record)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat(runtime diagnostics) error = %v", err)
		}
		if got := info.Mode().Perm(); got&0o077 != 0 {
			t.Fatalf("runtime diagnostics mode = %o, want no group/other permissions", got)
		}
		dirInfo, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("Stat(runtime diagnostics directory) error = %v", err)
		}
		if got := dirInfo.Mode().Perm(); got&0o077 != 0 {
			t.Fatalf("runtime diagnostics directory mode = %o, want no group/other permissions", got)
		}
	}
}

func TestMemoryActivationDiagnosticUsesFixedStateOnly(t *testing.T) {
	root := t.TempDir()
	logger := newRuntimeDiagnosticsLogger(root)
	logMemoryActivationState(logger, memoryActivationUnconfigured)

	data, err := os.ReadFile(filepath.Join(root, "logs", runtimeDiagnosticsFilename))
	if err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record["component"] != "memory" || record["state"] != memoryActivationUnconfigured {
		t.Fatalf("Memory activation diagnostic = %#v", record)
	}
	if _, exists := record["error"]; exists {
		t.Fatalf("Memory activation diagnostic exposed a detailed error: %#v", record)
	}
}

func TestInvalidPersistedMemoryConfigurationLogsUnconfigured(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
	}{
		{name: "current", raw: `{"schema_version":2,"memory":{"enabled":true}}`},
		{name: "current wire type", raw: `{"schema_version":2,"memory":{"bindings":"not-an-array"}}`},
		{name: "mixed legacy wire", raw: `{"schema_version":2,"memory":{"enabled":true,"bots":[{}],"bindings":[{}]}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "config.json"), []byte(test.raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := NewLocalStack(Config{StoreDir: root, WorkspaceCWD: t.TempDir(), Sandbox: SandboxConfig{RequestedType: "host"}}); err == nil {
				t.Fatal("NewLocalStack() accepted invalid Memory configuration")
			}
			data, err := os.ReadFile(filepath.Join(root, "logs", runtimeDiagnosticsFilename))
			if err != nil {
				t.Fatal(err)
			}
			var record map[string]any
			if err := json.Unmarshal(data, &record); err != nil {
				t.Fatal(err)
			}
			if record["component"] != "memory" || record["state"] != memoryActivationUnconfigured {
				t.Fatalf("invalid Memory diagnostic = %#v", record)
			}
			if _, exists := record["error"]; exists {
				t.Fatalf("invalid Memory diagnostic exposed detailed error: %#v", record)
			}
		})
	}
}

func TestBoundedDiagnosticWriterKeepsOneSizeBoundedBackup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "runtime.jsonl")
	writer := &boundedDiagnosticWriter{path: path, maxBytes: 12}
	first := []byte("first-line\n")
	second := []byte("second\n")

	if _, err := writer.Write(first); err != nil {
		t.Fatalf("Write(first) error = %v", err)
	}
	if _, err := writer.Write(second); err != nil {
		t.Fatalf("Write(second) error = %v", err)
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(current) error = %v", err)
	}
	backup, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("ReadFile(backup) error = %v", err)
	}
	if string(current) != string(second) || string(backup) != string(first) {
		t.Fatalf("rotated diagnostics current=%q backup=%q", current, backup)
	}
}
