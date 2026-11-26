package appserver

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/control/sessionvisibility"
)

func TestSessionWorkbenchPaginationRejectsChangedPriorityMembership(t *testing.T) {
	t.Run("promotion in recent lane", func(t *testing.T) {
		fixture := newWorkbenchPaginationFixture(t)
		cursor := fixture.page("", "a").NextCursor
		fixture.active = []string{"b"}
		fixture.requireConflict(cursor)

		fresh := fixture.page("", "b").NextCursor
		if !SessionWorkbenchPriorityChanged(cursor, fresh) {
			t.Fatal("first-page cursors did not identify the changed priority membership")
		}
		cursor = fixture.page(fresh, "a").NextCursor
		if next := fixture.page(cursor, "c").NextCursor; next != "" {
			t.Fatalf("unexpected continuation after last Session: %q", next)
		}
	})
	t.Run("read before recent lane starts", func(t *testing.T) {
		fixture := newWorkbenchPaginationFixture(t)
		seq := fixture.recordResult("a")
		cursor := fixture.page("", "a").NextCursor
		if err := fixture.client.AcknowledgeSessionRead(t.Context(), fixture.principal, "a", SessionReadReceipt{ThroughSeq: seq}); err != nil {
			t.Fatal(err)
		}
		fixture.requireConflict(cursor)

		cursor = fixture.page("", "a").NextCursor
		fixture.page(cursor, "b")
	})
	t.Run("demotion in priority lane", func(t *testing.T) {
		fixture := newWorkbenchPaginationFixture(t)
		fixture.active = []string{"b", "c"}
		cursor := fixture.page("", "b").NextCursor
		fixture.active = nil
		fixture.requireConflict(cursor)

		cursor = fixture.page("", "a").NextCursor
		cursor = fixture.page(cursor, "b").NextCursor
		fixture.page(cursor, "c")
	})
}

func TestSessionWorkbenchPaginationIgnoresPrioritySourceAndHiddenActivity(t *testing.T) {
	fixture := newWorkbenchPaginationFixture(t)
	fixture.active = []string{"c", "b", "c"}
	fixture.recordResult("c")
	cursor := fixture.page("", "b").NextCursor

	// C remains in the visible priority lane through its unread result.
	fixture.active = []string{"b", "foreign", "0-managed", "b"}
	fixture.recordResult("0-managed")
	page := fixture.page(cursor, "c")
	if SessionWorkbenchPriorityChanged(cursor, page.NextCursor) {
		t.Fatal("changing lanes with the same visible membership invalidated the cursor")
	}
	if page.Sessions[0].Active || !page.Sessions[0].Unread {
		t.Fatalf("priority row retained stale activity: %+v", page.Sessions[0])
	}

	fixture.active = []string{"0-managed", "b", "c"}
	if next := fixture.page(page.NextCursor, "a").NextCursor; next != "" {
		t.Fatalf("unexpected continuation after last Session: %q", next)
	}
}

type workbenchPaginationFixture struct {
	t         *testing.T
	client    *Client
	directory *inmemory.Store
	principal Principal
	active    []string
}

func newWorkbenchPaginationFixture(t *testing.T) *workbenchPaginationFixture {
	t.Helper()
	// Equal timestamps keep the directory order stable while lane membership changes.
	directory := inmemory.NewStore(inmemory.Config{Clock: func() time.Time { return time.Unix(1, 0) }})
	fixture := &workbenchPaginationFixture{t: t, directory: directory, principal: Principal{ID: "owner"}}
	for _, id := range []string{"a", "b", "c", "foreign", "0-managed"} {
		req := session.StartSessionRequest{AppName: "caelis", UserID: fixture.principal.ID, PreferredSessionID: id}
		switch id {
		case "foreign":
			req.UserID = "someone-else"
		case "0-managed":
			req.Metadata = map[string]any{sessionvisibility.MetadataSystemManagedAgent: "subagent"}
		}
		if _, err := directory.StartSession(t.Context(), req); err != nil {
			t.Fatal(err)
		}
	}
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "workbench.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	workbench, err := NewSessionWorkbenchStore(t.Context(), db, directory)
	if err != nil {
		t.Fatal(err)
	}
	fixture.client = &Client{config: ClientConfig{
		Authorizer: SessionAuthorizer{Sessions: directory}, Sessions: directory,
		SessionWorkbench: workbench, ActiveSessions: fixture,
	}}
	return fixture
}

func (f *workbenchPaginationFixture) ActiveSessionIDs(context.Context) ([]string, error) {
	return f.active, nil
}

func (f *workbenchPaginationFixture) page(cursor, wantID string) SessionWorkbenchPage {
	f.t.Helper()
	page, err := f.client.ListSessionWorkbench(f.t.Context(), f.principal, SessionWorkbenchRequest{Limit: 1, Cursor: cursor})
	if err != nil || len(page.Sessions) != 1 || page.Sessions[0].SessionID != wantID {
		f.t.Fatalf("page for cursor %q = %+v, %v, want Session %q", cursor, page, err, wantID)
	}
	return page
}

func (f *workbenchPaginationFixture) requireConflict(cursor string) {
	f.t.Helper()
	if cursor == "" {
		f.t.Fatal("missing continuation cursor")
	}
	page, err := f.client.ListSessionWorkbench(f.t.Context(), f.principal, SessionWorkbenchRequest{Limit: 1, Cursor: cursor})
	if !errorcode.Is(err, errorcode.Conflict) || len(page.Sessions) != 0 || page.NextCursor != "" {
		f.t.Fatalf("stale page = %+v, %v, want a conflict without a page", page, err)
	}
}

func (f *workbenchPaginationFixture) recordResult(id string) uint64 {
	f.t.Helper()
	ctx := f.t.Context()
	ref := session.SessionRef{SessionID: id}
	workbench := f.client.config.SessionWorkbench
	turn, err := workbench.BeginTurn(ctx, ref, "fixture-"+id)
	if err != nil {
		f.t.Fatal(err)
	}
	text := model.NewTextMessage(model.RoleAssistant, "result")
	result, err := f.directory.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: ref, Event: &session.Event{Message: &text, Text: "result"},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	if err := workbench.RecordResult(ctx, turn, result.Seq); err != nil {
		f.t.Fatal(err)
	}
	return result.Seq
}
