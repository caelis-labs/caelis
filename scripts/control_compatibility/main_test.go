package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

type transcriptSubscription struct {
	deliveries chan appserver.FeedDelivery
	err        error
}

func (s *transcriptSubscription) Deliveries() <-chan appserver.FeedDelivery { return s.deliveries }
func (s *transcriptSubscription) Close() error                              { return nil }
func (s *transcriptSubscription) Err() error                                { return s.err }

func TestTranscriptRequiresCommittedReplacementAndTargetTerminal(t *testing.T) {
	target := appserver.TurnTarget{HandleID: "handle", RunID: "run", TurnID: "turn"}
	message := func(text string) eventstream.Envelope {
		return eventstream.Envelope{SessionID: "session", TurnID: target.TurnID, Scope: eventstream.ScopeMain, Final: true, Delivery: &eventstream.Delivery{Mode: eventstream.DeliveryCanonical}, Update: eventstream.ContentChunk{SessionUpdate: eventstream.UpdateAgentMessage, MessageID: "same-message", Replacement: true, Content: eventstream.TextContent{Type: "text", Text: text}}}
	}
	terminal := eventstream.TurnCompleted("", "", target.TurnID, time.Now())
	terminal.SessionID = "session"
	terminal.Scope = eventstream.ScopeMain
	begin := appserver.FeedDelivery{Kind: appserver.FeedDeliveryReplaceBegin, Source: appserver.FeedSourceReplacement, SnapshotID: "snapshot"}
	end := appserver.FeedDelivery{Kind: appserver.FeedDeliveryReplaceEnd, Source: appserver.FeedSourceReplacement, SnapshotID: "snapshot", Page: 1}
	page := func(events ...eventstream.Envelope) appserver.FeedDelivery {
		return appserver.FeedDelivery{Kind: appserver.FeedDeliveryReplacePage, Source: appserver.FeedSourceReplacement, SnapshotID: "snapshot", Events: events}
	}
	otherTerminal := terminal
	otherTerminal.TurnID = "other-turn"
	transient := message("MATRIX_FINAL")
	transient.Final = false
	transient.Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
	exact := func(events ...eventstream.Envelope) appserver.FeedDelivery {
		for i := range events {
			events[i].Cursor = fmt.Sprintf("cursor-%d", i+1)
			events[i].Position = &eventstream.FeedPosition{Transient: &eventstream.TransientFeedPosition{Generation: "live", Sequence: uint64(i + 1)}}
		}
		return appserver.FeedDelivery{Kind: appserver.FeedDeliveryAppendPage, Source: appserver.FeedSourceExact, Events: events, NextCursor: events[len(events)-1].Cursor}
	}
	for _, tc := range []struct {
		name       string
		deliveries []appserver.FeedDelivery
		streamErr  error
		wantOK     bool
	}{
		{"committed", []appserver.FeedDelivery{begin, page(message("MATRIX_FINAL"), terminal), end}, nil, true},
		{"incomplete replacement", []appserver.FeedDelivery{begin, page(message("MATRIX_FINAL"), terminal)}, nil, false},
		{"text without terminal", []appserver.FeedDelivery{begin, page(message("MATRIX_FINAL")), end}, nil, false},
		{"wrong terminal", []appserver.FeedDelivery{begin, page(message("MATRIX_FINAL"), otherTerminal), end}, nil, false},
		{"transient text before terminal", []appserver.FeedDelivery{exact(transient)}, nil, false},
		{"live final deduplicated", []appserver.FeedDelivery{exact(transient, terminal)}, nil, true},
		{"stream failure", []appserver.FeedDelivery{begin, page(message("MATRIX_FINAL"), terminal), end}, errors.New("stream failed"), false},
		{"same ID replaced", []appserver.FeedDelivery{begin, page(message("MATRIX_FINAL"), message("revised"), terminal), end}, nil, false},
		{"empty message replacement", []appserver.FeedDelivery{begin, page(message("MATRIX_FINAL"), message(""), terminal), end}, nil, false},
		{"same ID final replacement", []appserver.FeedDelivery{begin, page(message("draft"), message("MATRIX_FINAL"), terminal), end}, nil, true},
		{"empty history replacement", []appserver.FeedDelivery{begin, page(message("MATRIX_FINAL")), end, begin, page(), end, begin, page(terminal), end}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sub := &transcriptSubscription{deliveries: make(chan appserver.FeedDelivery, len(tc.deliveries)), err: tc.streamErr}
			for _, delivery := range tc.deliveries {
				sub.deliveries <- delivery
			}
			close(sub.deliveries)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			err := consumeTranscript(ctx, sub, "session", target)
			if (err == nil) != tc.wantOK {
				t.Fatalf("result = %v; want success %v", err, tc.wantOK)
			}
		})
	}
}
