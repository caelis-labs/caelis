package capability

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/internal/adapters/sandbox/windows/internal/pathutil"
)

func TestBindWriteRootsPersistsStableRootSIDs(t *testing.T) {
	store := filepath.Join(t.TempDir(), "cap_sids.json")
	workspace := filepath.Join(t.TempDir(), "workspace")
	extra := filepath.Join(t.TempDir(), "extra")

	first, err := BindWriteRoots(store, workspace, []string{workspace, extra})
	if err != nil {
		t.Fatalf("BindWriteRoots() error = %v", err)
	}
	if len(first.AllSIDs) != 2 {
		t.Fatalf("AllSIDs = %#v, want two SIDs", first.AllSIDs)
	}
	if first.WriteRootTo[pathutil.Normalize(workspace)] == "" || first.WriteRootTo[pathutil.Normalize(extra)] == "" {
		t.Fatalf("WriteRootTo = %#v, want workspace and extra mappings", first.WriteRootTo)
	}
	for _, sid := range first.AllSIDs {
		if !strings.HasPrefix(sid, "S-1-5-21-") {
			t.Fatalf("SID = %q, want generated S-1-5-21 SID", sid)
		}
	}

	second, err := BindWriteRoots(store, workspace, []string{extra, workspace})
	if err != nil {
		t.Fatalf("second BindWriteRoots() error = %v", err)
	}
	if first.WriteRootTo[pathutil.Normalize(workspace)] != second.WriteRootTo[pathutil.Normalize(workspace)] {
		t.Fatalf("workspace SID changed: %q -> %q", first.WriteRootTo[pathutil.Normalize(workspace)], second.WriteRootTo[pathutil.Normalize(workspace)])
	}
	if first.WriteRootTo[pathutil.Normalize(extra)] != second.WriteRootTo[pathutil.Normalize(extra)] {
		t.Fatalf("extra SID changed: %q -> %q", first.WriteRootTo[pathutil.Normalize(extra)], second.WriteRootTo[pathutil.Normalize(extra)])
	}
}
