package appserver

import (
	"fmt"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

func collectHistoryWindow(t *testing.T, sub FeedSubscription) ([]eventstream.Envelope, FeedDelivery) {
	t.Helper()
	var events []eventstream.Envelope
	assembler := &FeedDeliveryAssembler{}
	for d := range sub.Deliveries() {
		page, _, err := assembler.AcceptPage(d)
		if err != nil {
			t.Fatal(err)
		}
		events = append(events, page...)
		if d.Kind == FeedDeliverySync {
			return events, d
		}
	}
	t.Fatalf("missing sync: %v", sub.Err())
	return nil, FeedDelivery{}
}

func TestFeedHistoryWindowsExactAndCanonical(t *testing.T) {
	for _, canonical := range []bool{false, true} {
		t.Run(fmt.Sprint(canonical), func(t *testing.T) {
			var reader *checkpointPageReader
			var events []*session.Event
			if canonical {
				reader = &checkpointPageReader{}
				for n := range 40 {
					events = append(events, feedIdentifiedNarrative(uint64(n+1), fmt.Sprint(n), fmt.Sprint(n), fmt.Sprint(n)))
				}
				reader.setEvents(events...)
			}
			var broker *FeedBroker
			if canonical {
				broker, _ = newTestFeedBroker(t, reader, FeedBrokerConfig{})
			} else {
				broker, _ = newTestFeedBroker(t, nil, FeedBrokerConfig{})
			}
			if !canonical {
				for n := range 40 {
					env := eventstream.Envelope{Kind: eventstream.KindSessionUpdate, SessionID: "session-1", EventID: fmt.Sprint(n), TurnID: fmt.Sprint(n), Scope: eventstream.ScopeMain, Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryTransient}, Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, MessageID: fmt.Sprint(n), Content: eventstream.TextContent{Type: "text", Text: fmt.Sprint(n)}}}
					if err := broker.Publish(env); err != nil {
						t.Fatal(err)
					}
				}
			}
			result, err := broker.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1", HistoryTurns: 16})
			if err != nil {
				t.Fatal(err)
			}
			defer result.Subscription.Close()
			recent, sync := collectHistoryWindow(t, result.Subscription)
			if len(recent) != 16 || recent[0].TurnID != "24" || recent[15].TurnID != "39" || sync.HistoryBefore == "" || sync.NextCursor == "" {
				t.Fatalf("recent=%d first=%s sync=%+v", len(recent), recent[0].TurnID, sync)
			}
			// Publish after the cut while older history is read on another subscription.
			if err := broker.Publish(eventstream.Envelope{Kind: eventstream.KindNotice, SessionID: "session-1", Notice: "live"}); err != nil {
				t.Fatal(err)
			}
			before := sync.HistoryBefore
			total := len(recent)
			for before != "" {
				older, err := broker.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1", HistoryBefore: before, HistoryTurns: 16})
				if err != nil {
					t.Fatal(err)
				}
				page, edge := collectHistoryWindow(t, older.Subscription)
				if edge.NextCursor != "" {
					t.Fatal("older window advanced live cursor")
				}
				if got := page[len(page)-1].TurnID; got != fmt.Sprint(39-total) {
					t.Fatalf("gap at %d: %s", total, got)
				}
				for range older.Subscription.Deliveries() {
					t.Fatal("finite window followed live output")
				}
				if err := older.Subscription.Err(); err != nil {
					t.Fatal(err)
				}
				older.Subscription.Close()
				total += len(page)
				before = edge.HistoryBefore
			}
			if total != 40 {
				t.Fatalf("total=%d", total)
			}
			live := assertFeedDelivery(t, result.Subscription.Deliveries(), FeedDeliveryAppendPage)
			if len(live.Events) != 1 || live.Events[0].Notice != "live" {
				t.Fatal("lost or duplicated live output")
			}
		})
	}
}

func TestFeedHistoryRejectsForeignIncarnationAndLiveCursor(t *testing.T) {
	broker, codec := newTestFeedBroker(t, nil, FeedBrokerConfig{})
	for n := range 3 {
		if err := broker.Publish(eventstream.Envelope{Kind: eventstream.KindNotice, SessionID: "session-1", TurnID: fmt.Sprint(n), Notice: "row"}); err != nil {
			t.Fatal(err)
		}
	}
	result, err := broker.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1", HistoryTurns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	_, sync := collectHistoryWindow(t, result.Subscription)
	if _, err := codec.Decode("session-1", sync.HistoryBefore); err == nil {
		t.Fatal("history accepted as live cursor")
	}
	other, _ := newTestFeedBroker(t, nil, FeedBrokerConfig{})
	if _, err := other.Subscribe(t.Context(), SubscribeRequest{SessionID: "session-1", HistoryBefore: sync.HistoryBefore}); err == nil {
		t.Fatal("foreign spool incarnation accepted")
	}
}
