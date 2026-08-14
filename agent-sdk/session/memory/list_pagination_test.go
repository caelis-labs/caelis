package inmemory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestStoreListPaginatesWithStableKeysetCursor(t *testing.T) {
	ctx := context.Background()
	nextID := 0
	now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	store := NewStore(Config{
		SessionIDGenerator: func() string {
			nextID++
			return fmt.Sprintf("session-%03d", nextID)
		},
		Clock: func() time.Time { return now },
	})
	for i := 0; i < 5; i++ {
		if _, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"}); err != nil {
			t.Fatal(err)
		}
	}

	first, err := store.ListSessions(ctx, session.ListSessionsRequest{AppName: "caelis", UserID: "user-1", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sessions) != 3 || first.NextCursor == "" {
		t.Fatalf("first page = %#v", first)
	}
	second, err := store.ListSessions(ctx, session.ListSessionsRequest{AppName: "caelis", UserID: "user-1", Cursor: first.NextCursor, Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Sessions) != 2 || second.NextCursor != "" {
		t.Fatalf("second page = %#v", second)
	}
	seen := map[string]bool{}
	for _, summary := range append(first.Sessions, second.Sessions...) {
		if seen[summary.SessionID] {
			t.Fatalf("duplicate SessionID %q across pages", summary.SessionID)
		}
		seen[summary.SessionID] = true
	}
	if len(seen) != 5 {
		t.Fatalf("paged sessions = %v", seen)
	}
}

func TestStoreListFiltersLegacyWorkspaceAliasesByCWD(t *testing.T) {
	ctx := context.Background()
	store := NewStore(Config{})
	workspace := t.TempDir()
	otherWorkspace := t.TempDir()
	for key, sessionID := range map[string]string{"legacy-a": "session-a", "legacy-b": "session-b"} {
		if _, err := store.StartSession(ctx, session.StartSessionRequest{
			AppName: "caelis", UserID: "user-1",
			Workspace: session.WorkspaceRef{Key: key, CWD: workspace}, PreferredSessionID: sessionID,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1",
		Workspace: session.WorkspaceRef{Key: "legacy-a", CWD: otherWorkspace}, PreferredSessionID: "other-session",
	}); err != nil {
		t.Fatal(err)
	}

	listed, err := store.ListSessions(ctx, session.ListSessionsRequest{UserID: "user-1", CWD: workspace})
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Sessions) != 2 {
		t.Fatalf("CWD-scoped Sessions = %#v", listed.Sessions)
	}
}
