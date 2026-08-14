package file

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestStoreMigratesOnlyExactLegacyCorruptedGeneratedTitle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	store := NewStore(Config{RootDir: root})

	generated, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "legacy-generated",
		Workspace: session.WorkspaceRef{Key: "ws-1", CWD: "/tmp/ws"},
	})
	if err != nil {
		t.Fatal(err)
	}
	prompt := strings.Repeat("a", 79) + "中文"
	userMessage := model.NewTextMessage(model.RoleUser, prompt)
	if _, err := store.AppendEvent(ctx, session.AppendEventRequest{
		SessionRef: generated.SessionRef,
		Event: &session.Event{
			Type: session.EventTypeUser, Visibility: session.VisibilityCanonical,
			Message: &userMessage, Text: prompt,
		},
	}); err != nil {
		t.Fatal(err)
	}
	tail := make([]*session.Event, 0, 96)
	for i := 0; i < cap(tail); i++ {
		message := model.NewTextMessage(model.RoleAssistant, "tail")
		tail = append(tail, &session.Event{
			Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
			Message: &message, Text: "tail",
		})
	}
	if _, err := store.AppendEvents(ctx, session.AppendEventsRequest{SessionRef: generated.SessionRef, Events: tail}); err != nil {
		t.Fatal(err)
	}

	explicitTitle := "literal replacement rune \uFFFD"
	explicit, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "explicit-title",
		Workspace: session.WorkspaceRef{Key: "ws-1", CWD: "/tmp/ws"}, Title: explicitTitle,
	})
	if err != nil {
		t.Fatal(err)
	}
	explicitEvents := make([]*session.Event, 0, legacyGeneratedTitleMaxEvents+44)
	for i := 0; i < cap(explicitEvents); i++ {
		message := model.NewTextMessage(model.RoleAssistant, "explicit title history")
		explicitEvents = append(explicitEvents, &session.Event{
			Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
			Message: &message, Text: "explicit title history",
		})
	}
	if _, err := store.AppendEvents(ctx, session.AppendEventsRequest{SessionRef: explicit.SessionRef, Events: explicitEvents}); err != nil {
		t.Fatal(err)
	}

	legacyTitle := prompt[:80]
	if utf8.ValidString(legacyTitle) {
		t.Fatalf("legacy test title %q is valid UTF-8, want historical split sequence", legacyTitle)
	}
	var generatedPath string
	if err := store.mu.LockContext(ctx); err != nil {
		t.Fatal(err)
	}
	err = store.withRootWriteLockContext(ctx, func() error {
		var pathErr error
		generatedPath, pathErr = store.resolveWritePath(generated)
		if pathErr != nil {
			return pathErr
		}
		doc, readErr := store.readDocumentAt(generatedPath)
		if readErr != nil {
			return readErr
		}
		doc.Session.Title = legacyTitle
		return store.writeDocument(ctx, doc)
	})
	store.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(store.generatedTitleMigrationMarkerPath()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	storeRootLocks.Delete(store.normalizedRootDir())

	reopened := NewStore(Config{RootDir: root})
	pageLineReads := 0
	contextLineReads := 0
	reopened.eventPageLineRead = func(string, int, int64) { pageLineReads++ }
	reopened.eventLogLineRead = func(string, int, int64) { contextLineReads++ }
	listed, err := reopened.ListSessions(ctx, session.ListSessionsRequest{
		AppName: "caelis", UserID: "user-1", WorkspaceKey: "ws-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantGeneratedTitle := strings.Repeat("a", 79) + "中"
	titles := make(map[string]string, len(listed.Sessions))
	for _, summary := range listed.Sessions {
		titles[summary.SessionID] = summary.Title
	}
	if got := titles[generated.SessionID]; got != wantGeneratedTitle {
		t.Fatalf("migrated generated title = %q, want %q", got, wantGeneratedTitle)
	}
	if got := titles[explicit.SessionID]; got != explicitTitle {
		t.Fatalf("explicit title = %q, want unchanged %q", got, explicitTitle)
	}
	if contextLineReads != 0 {
		t.Fatalf("legacy title migration performed %d full-history line reads, want 0", contextLineReads)
	}
	maxPagedLineReads := legacyGeneratedTitlePageLimit + 1 +
		legacyGeneratedTitleMaxEvents + legacyGeneratedTitleMaxEvents/legacyGeneratedTitlePageLimit
	if pageLineReads > maxPagedLineReads {
		t.Fatalf("legacy title migration read %d paged lines, want at most %d bounded records", pageLineReads, maxPagedLineReads)
	}
	if _, err := os.Stat(reopened.generatedTitleMigrationMarkerPath()); err != nil {
		t.Fatalf("migration completion marker: %v", err)
	}

	persisted, err := reopened.readDocumentAt(generatedPath)
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Session.Title != wantGeneratedTitle {
		t.Fatalf("persisted document title = %q, want %q", persisted.Session.Title, wantGeneratedTitle)
	}
	loaded, err := reopened.LoadSession(ctx, session.LoadSessionRequest{SessionRef: generated.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Session.Title != wantGeneratedTitle || len(loaded.Events) == 0 {
		t.Fatalf("loaded Session = title:%q events:%d, want repaired title and durable context", loaded.Session.Title, len(loaded.Events))
	}
	replayed, ok := session.ModelMessageOf(loaded.Events[0])
	if !ok || !reflect.DeepEqual(replayed, userMessage) {
		t.Fatalf("replayed first model message = %#v, want runtime-produced %#v", replayed, userMessage)
	}

	storeRootLocks.Delete(reopened.normalizedRootDir())
	secondReopen := NewStore(Config{RootDir: root})
	secondPageReads := 0
	secondReopen.eventPageLineRead = func(string, int, int64) { secondPageReads++ }
	if _, err := secondReopen.ListSessions(ctx, session.ListSessionsRequest{
		AppName: "caelis", UserID: "user-1", WorkspaceKey: "ws-1",
	}); err != nil {
		t.Fatal(err)
	}
	if secondPageReads != 0 {
		t.Fatalf("completed migration performed %d event reads after reopen, want 0", secondPageReads)
	}
}

func TestLegacyTitleMigrationDoesNotBlockIndexReadsForInvalidHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	root := t.TempDir()
	store := NewStore(Config{RootDir: root})
	created, err := store.StartSession(ctx, session.StartSessionRequest{
		AppName: "caelis", UserID: "user-1", PreferredSessionID: "invalid-history",
		Workspace: session.WorkspaceRef{Key: "ws-1", CWD: "/tmp/ws"},
		Title:     "explicit title \uFFFD",
	})
	if err != nil {
		t.Fatal(err)
	}
	path, err := store.resolveWritePath(created)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(eventLogPath(path), []byte("{not-json}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.generatedTitleMigrationMarkerPath()); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	storeRootLocks.Delete(store.normalizedRootDir())

	reopened := NewStore(Config{RootDir: root})
	listed, err := reopened.ListSessions(ctx, session.ListSessionsRequest{
		AppName: "caelis", UserID: "user-1", WorkspaceKey: "ws-1",
	})
	if err != nil {
		t.Fatalf("ListSessions() error = %v, want index read despite unrelated invalid history", err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].Title != created.Title {
		t.Fatalf("ListSessions() = %#v, want explicit title unchanged", listed.Sessions)
	}
	if _, err := os.Stat(reopened.generatedTitleMigrationMarkerPath()); !os.IsNotExist(err) {
		t.Fatalf("migration marker error = %v, want absent marker for a later retry", err)
	}
}
