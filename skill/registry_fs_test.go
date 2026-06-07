package skill

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemRegistryListsAndLoadsSkills(t *testing.T) {
	root := t.TempDir()
	mustWriteSkill(t, filepath.Join(root, "lint", "SKILL.md"), `---
name: lint
description: Run lint checks.
---
# Lint
`)

	registry := NewRegistry([]string{root})
	listed, err := registry.List(context.Background())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed) != 1 || listed[0].Name != "lint" {
		t.Fatalf("listed = %#v, want lint", listed)
	}

	loaded, err := registry.Load(context.Background(), "lint")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if loaded.Description != "Run lint checks." {
		t.Fatalf("description = %q, want metadata description", loaded.Description)
	}
}

func mustWriteSkill(t *testing.T, path string, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll(%s): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatalf("WriteFile(%s): %v", path, err)
	}
}
