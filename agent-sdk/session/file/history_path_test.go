package file

import (
	"bytes"
	"os"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestCanonicalLogIsAppendOnlyAcrossReopen(t *testing.T) {
	store, active := newEventPageIndexFixture(t, 2)
	document, err := store.resolveDocumentPath(active.SessionID, active.WorkspaceKey)
	if err != nil {
		t.Fatal(err)
	}
	path := eventLogPath(document)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	appendLifecycleEvents(t, store, active.SessionRef, 3, 1)
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) <= len(before) || !bytes.HasPrefix(after, before) {
		t.Fatal("log is not append-only")
	}
	reopened := NewStore(Config{RootDir: store.rootDir})
	again, err := reopened.resolveDocumentPath(active.SessionID, active.WorkspaceKey)
	if err != nil || eventLogPath(again) != path {
		t.Fatalf("path changed %s %v", again, err)
	}
	wrong := active.SessionRef
	wrong.UserID = "other-user"
	if _, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: wrong}); err == nil {
		t.Fatal("event history ignored Session ownership")
	}
	events, err := reopened.Events(t.Context(), session.EventsRequest{SessionRef: active.SessionRef, IncludeTransient: true})
	if err != nil || len(events) != 3 {
		t.Fatalf("roundtrip events=%d err=%v", len(events), err)
	}
}
