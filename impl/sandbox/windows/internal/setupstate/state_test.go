package setupstate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDirsUseStoreRoot(t *testing.T) {
	root := t.TempDir()
	dirs := NewDirs(root)
	if dirs.MarkerPath != filepath.Join(root, ".sandbox", "setup_marker.json") {
		t.Fatalf("MarkerPath = %q", dirs.MarkerPath)
	}
	if dirs.UsersPath != filepath.Join(root, ".sandbox-secrets", "sandbox_users.json") {
		t.Fatalf("UsersPath = %q", dirs.UsersPath)
	}
	if dirs.ProgressPath != filepath.Join(root, ".sandbox", "setup_progress.json") {
		t.Fatalf("ProgressPath = %q", dirs.ProgressPath)
	}
}

func TestMarkerFreshnessDetectsHashChanges(t *testing.T) {
	marker := Marker{
		Version:         CurrentSetupVersion,
		RunnerHash:      "runner-a",
		PolicyHash:      "policy-a",
		OfflineUsername: "CaelisSandboxOffline",
		OnlineUsername:  "CaelisSandboxOnline",
	}
	if freshness := CheckFreshness(marker, Expectation{
		RunnerHash:      "runner-a",
		PolicyHash:      "policy-a",
		OfflineUsername: "caelissandboxoffline",
		OnlineUsername:  "caelissandboxonline",
	}); !freshness.Current {
		t.Fatalf("Freshness = %+v, want current", freshness)
	}
	if freshness := CheckFreshness(marker, Expectation{RunnerHash: "runner-b"}); freshness.Current || freshness.Reason == "" {
		t.Fatalf("Freshness = %+v, want stale runner", freshness)
	}
}

func TestWriteReadMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".sandbox", "setup_marker.json")
	if err := WriteMarker(path, Marker{Version: CurrentSetupVersion, RunnerHash: "runner"}); err != nil {
		t.Fatalf("WriteMarker() error = %v", err)
	}
	got, err := ReadMarker(path)
	if err != nil {
		t.Fatalf("ReadMarker() error = %v", err)
	}
	if got.Version != CurrentSetupVersion || got.RunnerHash != "runner" || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() {
		t.Fatalf("marker = %+v", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("marker file missing: %v", err)
	}
}

func TestHashJSONIsStable(t *testing.T) {
	a, err := HashJSON(map[string]any{"a": "b"})
	if err != nil {
		t.Fatalf("HashJSON() error = %v", err)
	}
	b, err := HashJSON(map[string]any{"a": "b"})
	if err != nil {
		t.Fatalf("HashJSON() second error = %v", err)
	}
	if a == "" || a != b {
		t.Fatalf("hashes = %q %q, want stable non-empty", a, b)
	}
}

func TestWriteReadClearError(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".sandbox", "setup_error.json")
	if err := WriteError(path, ErrorReport{Phase: "setup", Code: "boom", Message: "failed"}); err != nil {
		t.Fatalf("WriteError() error = %v", err)
	}
	report, err := ReadError(path)
	if err != nil {
		t.Fatalf("ReadError() error = %v", err)
	}
	if report.Phase != "setup" || report.Code != "boom" || report.Message != "failed" || report.Time.IsZero() {
		t.Fatalf("report = %+v", report)
	}
	if err := ClearError(path); err != nil {
		t.Fatalf("ClearError() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("error report still exists or unexpected error: %v", err)
	}
}

func TestWriteReadClearProgress(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".sandbox", "setup_progress.json")
	if err := WriteProgress(path, ProgressReport{Phase: "firewall", Message: "refreshing", Step: 8, Total: 11}); err != nil {
		t.Fatalf("WriteProgress() error = %v", err)
	}
	report, err := ReadProgress(path)
	if err != nil {
		t.Fatalf("ReadProgress() error = %v", err)
	}
	if report.Phase != "firewall" || report.Message != "refreshing" || report.Step != 8 || report.Total != 11 || report.Time.IsZero() {
		t.Fatalf("progress = %+v", report)
	}
	if err := ClearProgress(path); err != nil {
		t.Fatalf("ClearProgress() error = %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("progress report still exists or unexpected error: %v", err)
	}
}
