package devcache

import (
	"path/filepath"
	"testing"
)

// The Go module checksum database lives under GOPATH/pkg/sumdb rather than
// GOMODCACHE. Sandboxed `go` commands fail with a read-only filesystem error
// when that directory is missing from the writable set.
func TestWritableRootsIncludesGoSumDB(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GOPATH", filepath.Join(home, "go"))
	t.Setenv("GOMODCACHE", "")
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))

	roots := WritableRoots()
	want := filepath.Join(home, "go", "pkg", "sumdb")
	for _, root := range roots {
		if root == want {
			return
		}
	}
	t.Fatalf("writable roots %v missing %s", roots, want)
}
