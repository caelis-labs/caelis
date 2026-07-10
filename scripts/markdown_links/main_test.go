package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckMarkdownLinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "docs", "target.md"), "# Target\n")
	mustWrite(t, filepath.Join(root, "docs", "good.md"),
		"[relative](target.md)\n"+
			"[root](/docs/target.md)\n"+
			"[anchor](#local-heading)\n"+
			"[external](https://example.com/missing)\n\n"+
			"## Local heading\n\n"+
			"```md\n[example only](missing.md)\n```\n")
	if problems := checkPaths(root, []string{"docs/good.md"}); len(problems) != 0 {
		t.Fatalf("valid links reported problems: %v", problems)
	}

	mustWrite(t, filepath.Join(root, "docs", "bad.md"), "[missing](nowhere.md)\n")
	problems := checkPaths(root, []string{"docs/bad.md"})
	if len(problems) != 1 {
		t.Fatalf("problems = %v, want one missing link", problems)
	}
}

func mustWrite(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
