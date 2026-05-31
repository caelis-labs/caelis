package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	enginecontext "github.com/OnslaughtSnail/caelis/internal/engine/context"
)

func TestStoreRoundTripPersistsCanonicalEventsAndState(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.Create(ctx, session.StartRequest{
		AppName:            "caelis",
		UserID:             "tester",
		PreferredSessionID: "sess-test",
		Workspace:          session.Workspace{Key: "repo", CWD: "/tmp/repo"},
		Title:              "scratch",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Append(ctx, active.Ref, []session.Event{
		{
			Type: session.EventUser,
			Message: &model.Message{
				Role:  model.RoleUser,
				Parts: []model.Part{model.NewTextPart("ping")},
			},
		},
		{
			Type:       session.EventNotice,
			Visibility: session.VisibilityUIOnly,
			Message: &model.Message{
				Role:  model.RoleAssistant,
				Parts: []model.Part{model.NewTextPart("transient")},
			},
		},
		{
			Type: session.EventAssistant,
			Message: &model.Message{
				Role:  model.RoleAssistant,
				Parts: []model.Part{model.NewTextPart("pong")},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateState(ctx, active.Ref, func(state session.State) (session.State, error) {
		state["phase"] = "done"
		return state, nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	snapshot, err := reloaded.Load(ctx, active.Ref)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Session.Title != "scratch" || snapshot.Session.Workspace.Key != "repo" {
		t.Fatalf("session = %#v, want persisted title/workspace", snapshot.Session)
	}
	if len(snapshot.Events) != 2 {
		t.Fatalf("snapshot events = %d, want 2 canonical visible events", len(snapshot.Events))
	}
	if got := session.EventText(snapshot.Events[1]); got != "pong" {
		t.Fatalf("assistant text = %q, want pong", got)
	}
	if got := snapshot.Cursor; got != "3" {
		t.Fatalf("cursor = %q, want raw event count 3", got)
	}
	if got := snapshot.State["phase"]; got != "done" {
		t.Fatalf("state phase = %v, want done", got)
	}
}

func TestStoreEventsUsesRawCursorsWhenSkippingTransient(t *testing.T) {
	ctx := context.Background()
	store, err := New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	active, err := store.Create(ctx, session.StartRequest{
		AppName:            "caelis",
		UserID:             "tester",
		PreferredSessionID: "sess-cursor",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Append(ctx, active.Ref, []session.Event{
		{Type: session.EventUser, Message: &model.Message{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("one")}}},
		{Type: session.EventNotice, Visibility: session.VisibilityUIOnly},
		{Type: session.EventAssistant, Message: &model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.NewTextPart("two")}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	page, err := store.Events(ctx, session.EventQuery{Ref: active.Ref, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.NextCursor != "1" {
		t.Fatalf("first page = %#v, want one visible event and raw cursor 1", page)
	}
	page, err = store.Events(ctx, session.EventQuery{Ref: active.Ref, After: page.NextCursor, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || session.EventText(page.Events[0]) != "two" || page.NextCursor != "3" {
		t.Fatalf("second page = %#v, want assistant event and raw cursor 3", page)
	}
	page, err = store.Events(ctx, session.EventQuery{Ref: active.Ref, After: "99"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 0 || page.NextCursor != "3" {
		t.Fatalf("oversized cursor page = %#v, want empty page clamped to raw cursor 3", page)
	}
}

func TestStoreIndexedEventsFiltersTypeAndDirection(t *testing.T) {
	ctx := context.Background()
	store, err := New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	active, err := store.Create(ctx, session.StartRequest{
		AppName:            "caelis",
		UserID:             "tester",
		PreferredSessionID: "sess-index",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Append(ctx, active.Ref, []session.Event{
		{Type: session.EventUser, Message: &model.Message{Role: model.RoleUser, Parts: []model.Part{model.NewTextPart("first user")}}},
		{Type: session.EventToolCall, Tool: &session.ToolEvent{ID: "call-1", Name: "run_command"}},
		{Type: session.EventNotice, Visibility: session.VisibilityUIOnly, Message: &model.Message{Role: model.RoleAssistant, Parts: []model.Part{model.NewTextPart("transient")}}},
		{Type: session.EventToolResult, Tool: &session.ToolEvent{ID: "call-1", Name: "run_command", Status: session.ToolCompleted}},
	})
	if err != nil {
		t.Fatal(err)
	}
	page, err := store.IndexedEvents(ctx, session.EventIndexQuery{
		Ref:        active.Ref,
		Types:      []session.EventType{session.EventToolCall, session.EventToolResult},
		Descending: true,
		Limit:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Type != session.EventToolResult || page.NextCursor != "4" {
		t.Fatalf("indexed page = %#v, want latest tool result at cursor 4", page)
	}
	page, err = store.IndexedEvents(ctx, session.EventIndexQuery{
		Ref:        active.Ref,
		Types:      []session.EventType{session.EventToolCall, session.EventToolResult},
		After:      page.NextCursor,
		Descending: true,
		Limit:      1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].Type != session.EventToolCall || page.NextCursor != "2" {
		t.Fatalf("indexed second page = %#v, want previous tool call at cursor 2", page)
	}
	page, err = store.IndexedEvents(ctx, session.EventIndexQuery{
		Ref:   active.Ref,
		Types: []session.EventType{session.EventNotice},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 0 {
		t.Fatalf("indexed notice page = %#v, want transient notice filtered", page)
	}
}

func TestStoreListPersistsSessionSummaries(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	alpha, err := store.Create(ctx, session.StartRequest{
		AppName:            "caelis",
		UserID:             "tester",
		PreferredSessionID: "sess-alpha",
		Workspace:          session.Workspace{Key: "repo", CWD: "/tmp/repo"},
		Title:              "Alpha notes",
		Meta:               map[string]any{"project": "Phoenix migration"},
	})
	if err != nil {
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
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	page, err := reloaded.List(ctx, session.ListQuery{
		Ref:          session.Ref{AppName: "caelis", UserID: "tester", WorkspaceKey: "repo"},
		WorkspaceCWD: "/tmp/repo",
		Search:       "phoenix",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Sessions) != 1 || page.Sessions[0].Session.SessionID != "sess-alpha" {
		t.Fatalf("list page = %#v, want sess-alpha", page)
	}
	if page.Sessions[0].EventCount != 2 || !page.Sessions[0].LastEventAt.Equal(last) {
		t.Fatalf("summary = %#v, want two events and last event time", page.Sessions[0])
	}
}

func TestStoreRoundTripPreservesRebuiltModelContext(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	active, err := store.Create(ctx, session.StartRequest{
		AppName:            "caelis",
		UserID:             "tester",
		PreferredSessionID: "sess-context",
	})
	if err != nil {
		t.Fatal(err)
	}
	events := []session.Event{
		{Type: session.EventUser, Message: &model.Message{
			Role:  model.RoleUser,
			Parts: []model.Part{model.NewTextPart("run command")},
		}},
		{Type: session.EventAssistant, Message: &model.Message{
			Role: model.RoleAssistant,
			Parts: []model.Part{{
				Kind: model.PartToolUse,
				ToolUse: &model.ToolCall{
					ID:   "call-1",
					Name: "run_command",
				},
			}},
		}},
		{Type: session.EventToolCall, Tool: &session.ToolEvent{ID: "call-1", Name: "run_command"}},
		{Type: session.EventToolResult, Message: &model.Message{
			Role: model.RoleTool,
			Parts: []model.Part{{
				Kind: model.PartToolResult,
				ToolResult: &model.ToolResultPart{
					ToolCallID: "call-1",
					Name:       "run_command",
					Content:    []model.Part{model.NewTextPart("hello")},
				},
			}},
		}},
	}
	expected := enginecontext.Messages(events)
	if _, err := store.Append(ctx, active.Ref, events); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reloaded, err := New(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reloaded.Close() })
	snapshot, err := reloaded.Load(ctx, active.Ref)
	if err != nil {
		t.Fatal(err)
	}
	actual := enginecontext.Messages(snapshot.Events)
	if len(actual) != len(expected) {
		t.Fatalf("rebuilt messages = %d, want %d", len(actual), len(expected))
	}
	if actual[0].TextContent() != expected[0].TextContent() {
		t.Fatalf("user message = %q, want %q", actual[0].TextContent(), expected[0].TextContent())
	}
	if calls := actual[1].ToolCalls(); len(calls) != 1 || calls[0].ID != "call-1" {
		t.Fatalf("assistant tool calls = %#v, want call-1", calls)
	}
	if actual[2].Role != model.RoleTool {
		t.Fatalf("tool result role = %q, want tool", actual[2].Role)
	}
}

func TestStoreRejectsUnsafeSessionIDs(t *testing.T) {
	store, err := New(filepath.Join(t.TempDir(), "sessions.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_, err = store.Create(context.Background(), session.StartRequest{
		AppName:            "caelis",
		UserID:             "tester",
		PreferredSessionID: "../escape",
	})
	if !errors.Is(err, session.ErrInvalid) {
		t.Fatalf("Create unsafe id error = %v, want ErrInvalid", err)
	}
}
