package file

import (
	"errors"
	"os"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestEventsPageStopsAtRequestedDurableCut(t *testing.T) {
	for _, visibility := range []session.EventPageVisibility{session.EventPageAllDurable, session.EventPageClientReplay} {
		t.Run(string(visibility), func(t *testing.T) {
			store, active := newEventPageIndexFixture(t, 1000)
			var lastLine int
			store.eventPageLineRead = func(_ string, line int, _ int64) { lastLine = line }
			page, err := store.EventsPage(t.Context(), session.EventPageRequest{SessionRef: active.SessionRef, AfterSeq: 99, ThroughSeq: 100, Limit: 1, Visibility: visibility})
			if err != nil {
				t.Fatal(err)
			}
			if page.NextSeq != 100 || page.HasMore || len(page.Events) != 1 || page.Events[0].Seq != 100 {
				t.Fatalf("page=%+v", page)
			}
			if lastLine > 100 {
				t.Fatalf("read through line %d beyond durable cut 100", lastLine)
			}
			// The same reader can still fetch the complete later history.
			next, err := store.EventsPage(t.Context(), session.EventPageRequest{SessionRef: active.SessionRef, AfterSeq: 100, Limit: 1000, Visibility: visibility})
			if err != nil || len(next.Events) != 900 || next.NextSeq != 1000 {
				t.Fatalf("later events=%d next=%d err=%v", len(next.Events), next.NextSeq, err)
			}
		})
	}
}

func TestEventsPageValidatesOnlyRequestedCut(t *testing.T) {
	store, active := newEventPageIndexFixture(t, 4)
	document, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(eventLogPath(document), os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString("invalid complete record\n")
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		t.Fatal(err)
	}
	page, err := store.EventsPage(t.Context(), session.EventPageRequest{SessionRef: active.SessionRef, ThroughSeq: 2, Visibility: session.EventPageAllDurable})
	if err != nil || len(page.Events) != 2 || page.NextSeq != 2 || page.HasMore {
		t.Fatalf("cut events=%d next=%d more=%v err=%v", len(page.Events), page.NextSeq, page.HasMore, err)
	}
	if _, err := store.EventsPage(t.Context(), session.EventPageRequest{SessionRef: active.SessionRef, AfterSeq: 2, Visibility: session.EventPageAllDurable}); err == nil {
		t.Fatal("unbounded continuation did not validate the remaining history")
	}
}

func TestFileReplayExcludesValidJSONWithoutRecordTerminator(t *testing.T) {
	store, active := newEventPageIndexFixture(t, 2)
	document, err := store.resolveWritePath(active)
	if err != nil {
		t.Fatal(err)
	}
	message := model.NewTextMessage(model.RoleUser, "uncommitted input")
	raw, err := session.MarshalEventJSON(&session.Event{
		Schema: session.EventSchemaVersion, ID: "incomplete", Seq: 3, SessionID: active.SessionID,
		Type: session.EventTypeUser, Message: &message,
	})
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(eventLogPath(document), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.Write(raw)
	if err := errors.Join(writeErr, file.Close()); err != nil {
		t.Fatal(err)
	}
	cold := NewStore(Config{RootDir: store.rootDir})
	ref := active.SessionRef
	page, err := cold.EventsPage(t.Context(), session.EventPageRequest{SessionRef: ref, Visibility: session.EventPageAllDurable})
	if err != nil || page.NextSeq != 2 || len(page.Events) != 2 {
		t.Errorf("EventsPage = %d events through %d, %v", len(page.Events), page.NextSeq, err)
	}
	checkpoint, err := cold.EventCheckpoint(t.Context(), ref)
	if err != nil || checkpoint.ThroughSeq != 2 {
		t.Errorf("EventCheckpoint = %d, %v", checkpoint.ThroughSeq, err)
	}
	loaded, err := cold.LoadSession(t.Context(), session.LoadSessionRequest{SessionRef: ref, IncludeTransient: true})
	if err != nil || len(loaded.Events) != 2 {
		t.Errorf("LoadSession = %d events, %v", len(loaded.Events), err)
	}
	appendLifecycleEvents(t, cold, ref, 3, 1)
	page, err = cold.EventsPage(t.Context(), session.EventPageRequest{SessionRef: ref, AfterSeq: 2, Visibility: session.EventPageAllDurable})
	if err != nil || page.NextSeq != 3 || len(page.Events) != 1 || page.Events[0].ID == "incomplete" {
		t.Errorf("EventsPage after append = %#v, %v, want replacement at seq 3", page, err)
	}
}
