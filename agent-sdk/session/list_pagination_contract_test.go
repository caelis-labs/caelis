package session_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
)

func TestStoreListPaginatesWithStableKeysetCursor(t *testing.T) {
	for _, backend := range []string{"file", "memory"} {
		t.Run(backend, func(t *testing.T) {
			nextID := 0
			newID := func() string {
				nextID++
				return fmt.Sprintf("session-%03d", nextID)
			}
			now := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
			clock := func() time.Time { return now }
			var store session.Lifecycle
			switch backend {
			case "file":
				store = file.NewStore(file.Config{RootDir: t.TempDir(), SessionIDGenerator: newID, Clock: clock})
			case "memory":
				store = inmemory.NewStore(inmemory.Config{SessionIDGenerator: newID, Clock: clock})
			}
			ctx := t.Context()
			for i := range 5 {
				if i == 1 {
					now = now.Add(time.Second)
				}
				if _, err := store.StartSession(ctx, session.StartSessionRequest{AppName: "caelis", UserID: "user-1"}); err != nil {
					t.Fatal(err)
				}
			}

			req := session.ListSessionsRequest{AppName: "caelis", UserID: "user-1", Limit: 3}
			first, err := store.ListSessions(ctx, req)
			if err != nil || len(first.Sessions) != 3 || first.NextCursor == "" {
				t.Fatalf("first page = %#v, %v", first, err)
			}
			req.Cursor = first.NextCursor
			second, err := store.ListSessions(ctx, req)
			if err != nil || len(second.Sessions) != 2 || second.NextCursor != "" {
				t.Fatalf("second page = %#v, %v", second, err)
			}
			var got []string
			for _, row := range append(first.Sessions, second.Sessions...) {
				got = append(got, row.SessionID)
			}
			want := []string{"session-002", "session-003", "session-004", "session-005", "session-001"}
			if !slices.Equal(got, want) {
				t.Fatalf("paged sessions = %v, want %v", got, want)
			}
		})
	}
}
