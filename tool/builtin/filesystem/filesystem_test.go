package filesystem

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/OnslaughtSnail/caelis/tool"
	"github.com/stretchr/testify/require"
)

func TestGlobRecursive(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src", "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("package main"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "pkg", "lib.go"), []byte("package lib"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "pkg", "test.txt"), []byte("test"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("# test"), 0o644))

	g := &globFiles{}
	result, err := g.Run(newTestContext(dir), tool.Call{
		Args: map[string]any{"pattern": "*.go", "root": "."},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("error: %s", result.Output)
	}
	if len(result.Output) == 0 {
		t.Error("expected matches")
	}
}

func TestGlobExclude(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "node_modules", "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "index.js"), []byte("js"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "node_modules", "pkg", "dep.js"), []byte("dep"), 0o644))

	g := &globFiles{}
	result, err := g.Run(newTestContext(dir), tool.Call{
		Args: map[string]any{"pattern": "*.js", "root": "."},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("error: %s", result.Output)
	}
	if contains(result.Output, "node_modules") {
		t.Error("node_modules should be excluded")
	}
}

func TestSearchRecursive(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("func main() {\n\tfmt.Println(\"hello\")\n}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "lib.go"), []byte("func helper() {\n\t// helper\n}"), 0o644))

	s := &searchFiles{}
	result, err := s.Run(newTestContext(dir), tool.Call{
		Args: map[string]any{"query": "func", "path": "."},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("error: %s", result.Output)
	}
	if !contains(result.Output, "main.go") || !contains(result.Output, "lib.go") {
		t.Errorf("expected matches in both files, got: %s", result.Output)
	}
}

func TestSearchWithInclude(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "code.go"), []byte("func Test() {}"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "readme.md"), []byte("func is not code here"), 0o644))

	s := &searchFiles{}
	result, err := s.Run(newTestContext(dir), tool.Call{
		Args: map[string]any{"query": "func", "path": ".", "include": "*.go"},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("error: %s", result.Output)
	}
	if contains(result.Output, "readme.md") {
		t.Error("should not match non-.go files")
	}
	if !contains(result.Output, "code.go") {
		t.Error("should match .go files")
	}
}

func TestGlobDoubleStarRecursive(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "a", "b", "c"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "x.go"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "b", "y.go"), []byte("y"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "b", "c", "z.go"), []byte("z"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "a", "b", "c", "w.txt"), []byte("w"), 0o644))

	g := &globFiles{}
	result, err := g.Run(newTestContext(dir), tool.Call{
		Args: map[string]any{"pattern": "**/*.go", "root": "."},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("error: %s", result.Output)
	}
	// Should find all .go files across all subdirectories.
	if !contains(result.Output, "x.go") {
		t.Error("expected x.go in results")
	}
	if !contains(result.Output, "y.go") {
		t.Error("expected y.go in results")
	}
	if !contains(result.Output, "z.go") {
		t.Error("expected z.go in results")
	}
	if contains(result.Output, "w.txt") {
		t.Error("should not match .txt files")
	}
}

func TestGlobWithDirectoryPrefix(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src", "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "pkg", "lib.go"), []byte("y"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "README.md"), []byte("z"), 0o644))

	g := &globFiles{}
	result, err := g.Run(newTestContext(dir), tool.Call{
		Args: map[string]any{"pattern": "src/*.go", "root": "."},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("error: %s", result.Output)
	}
	// Should find src/main.go but not src/pkg/lib.go (not recursive without **)
	// and not README.md.
	if !contains(result.Output, "main.go") {
		t.Error("expected main.go in results")
	}
	if contains(result.Output, "README") {
		t.Error("should not match files outside src/")
	}
}

func TestGlobRecursiveWithDirPrefix(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "src", "pkg"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "main.go"), []byte("x"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "src", "pkg", "lib.go"), []byte("y"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "other.go"), []byte("z"), 0o644))

	g := &globFiles{}
	result, err := g.Run(newTestContext(dir), tool.Call{
		Args: map[string]any{"pattern": "src/**/*.go", "root": "."},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.IsError {
		t.Fatalf("error: %s", result.Output)
	}
	if !contains(result.Output, "main.go") {
		t.Error("expected main.go")
	}
	if !contains(result.Output, "lib.go") {
		t.Error("expected lib.go")
	}
	if contains(result.Output, "other.go") {
		t.Error("should not match files outside src/")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
