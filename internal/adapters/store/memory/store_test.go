package memory

import (
	"context"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
)

func TestStoreEventsSkipsTransientByDefault(t *testing.T) {
	ctx := context.Background()
	store := New()
	active, err := store.Create(ctx, session.StartRequest{AppName: "caelis", UserID: "tester"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Append(ctx, active.Ref, []session.Event{
		{
			Type:       session.EventAssistant,
			Visibility: session.VisibilityUIOnly,
			Message:    &model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.NewTextPart("stream")}},
		},
		{
			Type:       session.EventAssistant,
			Visibility: session.VisibilityCanonical,
			Message:    &model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.NewTextPart("final")}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.Events(ctx, session.EventQuery{Ref: active.Ref})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 {
		t.Fatalf("events = %d, want 1", len(page.Events))
	}
	if got := session.EventText(page.Events[0]); got != "final" {
		t.Fatalf("event text = %q, want final", got)
	}
}

func TestStoreListFiltersAndSummarizesSessions(t *testing.T) {
	ctx := context.Background()
	store := New()
	alpha, err := store.Create(ctx, session.StartRequest{
		AppName:            "caelis",
		UserID:             "tester",
		PreferredSessionID: "sess-alpha",
		Workspace:          session.Workspace{Key: "repo", CWD: "/tmp/repo"},
		Title:              "Alpha notes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, session.StartRequest{
		AppName:            "caelis",
		UserID:             "tester",
		PreferredSessionID: "sess-beta",
		Workspace:          session.Workspace{Key: "repo", CWD: "/tmp/repo"},
		Title:              "Beta notes",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(ctx, session.StartRequest{
		AppName:            "caelis",
		UserID:             "tester",
		PreferredSessionID: "sess-other",
		Workspace:          session.Workspace{Key: "other", CWD: "/tmp/other"},
		Title:              "Other notes",
	}); err != nil {
		t.Fatal(err)
	}
	last := time.Unix(200, 0).UTC()
	if _, err := store.Append(ctx, alpha.Ref, []session.Event{
		{Type: session.EventUser, Time: time.Unix(100, 0).UTC()},
		{Type: session.EventAssistant, Time: last},
	}); err != nil {
		t.Fatal(err)
	}

	page, err := store.List(ctx, session.ListQuery{
		Ref:          session.Ref{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		WorkspaceCWD: "/tmp/repo",
		Search:       "alpha",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].Session.SessionID != "sess-alpha" {
		t.Fatalf("search page = %#v, want sess-alpha", page)
	}
	if page.Sessions[0].EventCount != 2 || !page.Sessions[0].LastEventAt.Equal(last) {
		t.Fatalf("summary = %#v, want two events and last time", page.Sessions[0])
	}

	first, err := store.List(ctx, session.ListQuery{
		Ref:          session.Ref{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		WorkspaceCWD: "/tmp/repo",
		Limit:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Sessions) != 1 || first.NextCursor == "" {
		t.Fatalf("first page = %#v, want one item and next cursor", first)
	}
	second, err := store.List(ctx, session.ListQuery{
		Ref:          session.Ref{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		WorkspaceCWD: "/tmp/repo",
		After:        first.NextCursor,
		Limit:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Sessions) != 1 || second.Sessions[0].Session.SessionID == first.Sessions[0].Session.SessionID {
		t.Fatalf("second page = %#v, want next distinct session", second)
	}
}
