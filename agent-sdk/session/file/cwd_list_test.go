package file

import (
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestStoreListCWDNormalizesStoredPathsBeforePagination(t *testing.T) {
	workspace := t.TempDir()
	sep := string(filepath.Separator)
	testStoreListCWDVariants(t, workspace, []string{
		workspace,
		workspace + sep,
		workspace + sep + ".",
		workspace + sep + "child" + sep + "..",
	})
}

func TestWindowsStoreListCWDMatchesPathSpellings(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows path comparison")
	}
	for _, workspace := range []string{`C:\Work\项目\Äpp`, `\\Server\Share\项目\Äpp`} {
		t.Run(workspace, func(t *testing.T) {
			testStoreListCWDVariants(t, workspace, []string{
				workspace,
				strings.ToLower(workspace),
				strings.ToUpper(workspace),
				strings.ReplaceAll(workspace, `\`, "/"),
			})
		})
	}
}

func testStoreListCWDVariants(t *testing.T, workspace string, variants []string) {
	t.Helper()
	for _, backend := range []string{"file", "memory"} {
		t.Run(backend, func(t *testing.T) {
			ctx := t.Context()
			now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
			clock := func() time.Time { return now }
			var store, reader session.Lifecycle
			if backend == "file" {
				root := t.TempDir()
				store = NewStore(Config{RootDir: root, Clock: clock})
				// Listing must also recognize spellings in an existing index after
				// reopening, without rewriting the durable Session's CWD or key.
				reader = NewStore(Config{RootDir: root})
			} else {
				store = inmemory.NewStore(inmemory.Config{Clock: clock})
				reader = store
			}
			for i, cwd := range append(append([]string(nil), variants...), workspace+"-other", workspace) {
				userID := "user-1"
				if i == len(variants)+1 {
					userID = "other-user"
				}
				if _, err := store.StartSession(ctx, session.StartSessionRequest{
					AppName: "caelis", UserID: userID,
					Workspace:          session.WorkspaceRef{Key: fmt.Sprintf("workspace-%d", i), CWD: cwd},
					PreferredSessionID: fmt.Sprintf("session-%d", i),
				}); err != nil {
					t.Fatal(err)
				}
				now = now.Add(time.Second)
			}
			for _, query := range variants {
				cursor := ""
				for i := len(variants) - 1; i >= 0; i-- {
					page, err := reader.ListSessions(ctx, session.ListSessionsRequest{
						UserID: "user-1", CWD: query, Limit: 1, Cursor: cursor,
					})
					if err != nil {
						t.Fatal(err)
					}
					wantID := fmt.Sprintf("session-%d", i)
					if len(page.Sessions) != 1 || page.Sessions[0].SessionID != wantID {
						t.Fatalf("ListSessions(CWD=%q, cursor=%q) = %#v, want %s", query, cursor, page, wantID)
					}
					summary := page.Sessions[0]
					loaded, err := reader.LoadSession(ctx, session.LoadSessionRequest{SessionRef: summary.SessionRef})
					if err != nil {
						t.Fatal(err)
					}
					if summary.CWD != variants[i] || loaded.Session.CWD != variants[i] || loaded.Session.WorkspaceKey != fmt.Sprintf("workspace-%d", i) {
						t.Fatalf("listing/loading changed durable workspace: summary=%#v, loaded=%#v", summary, loaded.Session)
					}
					if (page.NextCursor != "") != (i > 0) {
						t.Fatalf("NextCursor = %q with %d matches remaining", page.NextCursor, i)
					}
					cursor = page.NextCursor
				}
			}
		})
	}
}
