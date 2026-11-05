package file

import (
	"errors"
	"os"
	"testing"

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
