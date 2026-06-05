package agentprofiles

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDirStatusWarnsAndSkipsInvalidProfiles(t *testing.T) {
	dir := t.TempDir()
	writeProfileTestFile(t, dir, "valid.md", `---
id: valid
description: Valid profile
---

Collect evidence.
`)
	writeProfileTestFile(t, dir, "empty.md", "   ")
	writeProfileTestFile(t, dir, "dupe.md", `---
id: valid
description: Duplicate profile
---

Duplicate.
`)

	status, err := LoadDirStatus(dir)
	if err != nil {
		t.Fatalf("LoadDirStatus() error = %v", err)
	}
	if got, want := len(status.Profiles), 1; got != want {
		t.Fatalf("len(Profiles) = %d, want %d: %#v", got, want, status.Profiles)
	}
	if status.Profiles[0].ID != "valid" {
		t.Fatalf("profile id = %q, want valid", status.Profiles[0].ID)
	}
	if got, want := len(status.Warnings), 2; got != want {
		t.Fatalf("len(Warnings) = %d, want %d: %#v", got, want, status.Warnings)
	}
}

func writeProfileTestFile(t *testing.T, dir string, name string, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
}
