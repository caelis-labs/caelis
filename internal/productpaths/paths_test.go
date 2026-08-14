package productpaths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/caelis-labs/caelis/internal/version"
)

func TestDefaultStoreDirSeparatesDevelopmentAndRelease(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		t.Skip("home directory unavailable")
	}
	dev := DefaultStoreDirForBuild(t.TempDir(), version.Info{BuildKind: version.BuildKindDev})
	release := DefaultStoreDirForBuild(t.TempDir(), version.Info{BuildKind: version.BuildKindRelease})
	if dev != filepath.Join(home, ".caelis-dev", "default") {
		t.Fatalf("development Store = %q", dev)
	}
	if release != filepath.Join(home, ".caelis") {
		t.Fatalf("release Store = %q", release)
	}
	if dev == release {
		t.Fatal("development and release Stores are not isolated")
	}
}

func TestServiceInstallDirIsOutsideStore(t *testing.T) {
	store := t.TempDir()
	dir, err := ServiceInstallDir(store)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(store, dir)
	if err != nil {
		t.Fatal(err)
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("service install directory %q is inside Store %q", dir, store)
	}
}
