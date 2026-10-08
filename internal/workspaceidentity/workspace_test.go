package workspaceidentity

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFromCWDDoesNotCollideForSameBasename(t *testing.T) {
	root := t.TempDir()
	workspaceA := filepath.Join(root, "a", "repo")
	workspaceB := filepath.Join(root, "b", "repo")
	for _, workspace := range []string{workspaceA, workspaceB} {
		if err := os.MkdirAll(workspace, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	addressA, err := FromCWD(workspaceA)
	if err != nil {
		t.Fatal(err)
	}
	addressB, err := FromCWD(workspaceB)
	if err != nil {
		t.Fatal(err)
	}
	if addressA.Key == addressB.Key {
		t.Fatalf("same-basename workspace keys collide: %q", addressA.Key)
	}
	if addressA.Key != addressA.CWD || addressB.Key != addressB.CWD {
		t.Fatalf("workspace addresses = %#v, %#v, want canonical CWD keys", addressA, addressB)
	}
}

func TestFromCWDResolvesDirectoryLinks(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "目标 空 格")
	link := filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		if output, err := exec.Command("cmd.exe", "/d", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
			t.Fatalf("create junction: %v: %s", err, output)
		}
	} else if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	want, err := FromCWD(target)
	if err != nil {
		t.Fatal(err)
	}
	got, err := FromCWD(link)
	if err != nil || got != want {
		t.Fatalf("directory alias = %+v, %v; want %+v", got, err, want)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CanonicalDirectory(filepath.Join(root, "file")); err == nil {
		t.Fatal("regular file accepted as a workspace")
	}
}
