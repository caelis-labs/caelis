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
