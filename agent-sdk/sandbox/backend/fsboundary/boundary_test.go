package fsboundary

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestDirectoryLinksDoNotExpandWriteRoots(t *testing.T) {
	root := t.TempDir()
	workspace, outside := filepath.Join(root, "work"), filepath.Join(root, "目标 空 格")
	for _, dir := range []string{workspace, outside} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(workspace, "alias")
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Fatalf("create junction: %v: %s", err, output)
		}
	} else if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "existing.txt"), []byte("preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	canonical, err := ResolveExistingPath(outside)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"existing.txt", filepath.Join("new", "file.txt")} {
		path := filepath.Join(link, suffix)
		if got := ResolvePathWithSymlinks(path); got != filepath.Join(canonical, suffix) {
			t.Fatalf("physical path = %q; want %q", got, filepath.Join(canonical, suffix))
		}
		if IsWithinRoots(path, []string{workspace}, nil) {
			t.Fatalf("directory link expanded the workspace write root: %s", path)
		}
		if !IsWithinRoots(path, []string{outside}, nil) {
			t.Fatalf("explicit physical write root did not admit %s", path)
		}
	}
}
