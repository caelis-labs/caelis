package appserver

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

type metadataFeedReader struct {
	*sessionfile.Store
	payloads atomic.Int64
}

func (r *metadataFeedReader) EventsPage(ctx context.Context, req session.EventPageRequest) (session.EventPage, error) {
	page, err := r.Store.EventsPage(ctx, req)
	r.payloads.Add(int64(len(page.Events)))
	return page, err
}

func TestFileHistoryMetadataPreservesLateApprovalsAndIndependentPages(t *testing.T) {
	for _, late := range []bool{false, true} {
		t.Run(fmt.Sprint(late), func(t *testing.T) {
			root := t.TempDir()
			writer := sessionfile.NewStore(sessionfile.Config{RootDir: root, SessionIDGenerator: func() string { return "session-1" }})
			active, err := writer.StartSession(t.Context(), session.StartSessionRequest{AppName: "test", UserID: "user"})
			if err != nil {
				t.Fatal(err)
			}
			var events []*session.Event
			for n := 0; n < 40; n++ {
				e := feedIdentifiedNarrative(0, fmt.Sprint(n), fmt.Sprint(n), fmt.Sprint(n))
				e.ID = fmt.Sprintf("event-%d", n)
				message := model.NewTextMessage(model.RoleAssistant, fmt.Sprint(n))
				e.Message = &message
				events = append(events, e)
			}
			if late {
				events = append(events, &session.Event{Type: session.EventTypeLifecycle, Visibility: session.VisibilityJournal, Journal: &session.ExecutionJournalEntry{Schema: session.ExecutionJournalSchemaVersion, Kind: session.JournalKindPauseToken, PauseToken: &session.PauseToken{Schema: session.ExecutionJournalSchemaVersion, TokenID: "pause", SessionID: active.SessionID, RunID: "run", TurnID: "1", Revision: 2, Status: session.PauseTokenResolved, ToolCallID: "call", ReviewText: "late decision"}}})
			}
			if _, err := writer.AppendEvents(t.Context(), session.AppendEventsRequest{SessionRef: active.SessionRef, Events: events}); err != nil {
				t.Fatal(err)
			}
			// Restart both reader and broker: no in-memory index may be assumed.
			reader := &metadataFeedReader{Store: sessionfile.NewStore(sessionfile.Config{RootDir: root})}
			broker, _ := newTestFeedBroker(t, reader, FeedBrokerConfig{})
			recent, err := broker.Subscribe(t.Context(), SubscribeRequest{SessionID: active.SessionID, HistoryTurns: 2})
			if err != nil {
				t.Fatal(err)
			}
			defer recent.Subscription.Close()
			page, edge := collectHistoryWindow(t, recent.Subscription)
			if late {
				if page[0].TurnID != "1" || page[len(page)-1].ApprovalReview == nil {
					t.Fatalf("late original turn lost: %+v", page)
				}
				return
			}
			if len(page) != 2 || page[0].TurnID != "38" || edge.HistoryBefore == "" || reader.payloads.Load() != 2 {
				t.Fatalf("tail=%d payloads=%d edge=%+v", len(page), reader.payloads.Load(), edge)
			}
			if err := broker.Publish(eventstream.Envelope{Kind: eventstream.KindNotice, SessionID: active.SessionID, Notice: "live while paging"}); err != nil {
				t.Fatal(err)
			}
			older, err := broker.Subscribe(t.Context(), SubscribeRequest{SessionID: active.SessionID, HistoryBefore: edge.HistoryBefore, HistoryTurns: 16})
			if err != nil {
				t.Fatal(err)
			}
			defer older.Subscription.Close()
			previous, previousEdge := collectHistoryWindow(t, older.Subscription)
			if len(previous) != 16 || previous[0].TurnID != "22" || previous[15].TurnID != "37" || previousEdge.NextCursor != "" {
				t.Fatalf("older page=%+v edge=%+v", previous, previousEdge)
			}
			live := assertFeedDelivery(t, recent.Subscription.Deliveries(), FeedDeliveryAppendPage)
			if len(live.Events) != 1 || live.Events[0].Notice != "live while paging" {
				t.Fatal("lost live continuation")
			}
		})
	}
}
