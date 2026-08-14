package workspaceidentity

import (
	"os"
	"path/filepath"
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
