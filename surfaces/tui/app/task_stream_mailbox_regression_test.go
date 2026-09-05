package tuiapp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

func taskMailboxExactDelivery(sequence int, activityID string) taskstream.Delivery {
	cursor := fmt.Sprintf("cursor-%d", sequence)
	return taskstream.Delivery{
		Kind: taskstream.DeliveryAppendPage, Source: taskstream.SourceExact, ActivityID: activityID,
		NextCursor: cursor,
		Events: []eventstream.Envelope{tuiExactEnvelope(eventstream.Envelope{
			EventID: fmt.Sprintf("event-%d", sequence), ActivityID: activityID,
		}, cursor, uint64(sequence))},
	}
}

func TestRegressionTaskMailboxCatchesUpInBoundedBatches(t *testing.T) {
	const backlog = 4096
	deliveries := make(chan taskstream.Delivery, backlog)
	for sequence := 1; sequence <= backlog; sequence++ {
		deliveries <- taskMailboxExactDelivery(sequence, "activity-1")
	}
	mailbox := &taskStreamMailbox{}
	seen, batches := 0, 0
	for seen < backlog {
		events, cursor, activity, replacement, open, err := mailbox.read(t.Context(), deliveries)
		if err != nil || !open || replacement || activity != "activity-1" {
			t.Fatalf("backlog batch: cursor=%q activity=%q replacement=%v open=%v err=%v", cursor, activity, replacement, open, err)
		}
		if len(events) != taskStreamMailboxBatchSize {
			t.Fatalf("backlog was not batched: got %d events", len(events))
		}
		for _, event := range events {
			seen++
			if event.EventID != fmt.Sprintf("event-%d", seen) {
				t.Fatalf("FIFO event %d = %q", seen, event.EventID)
			}
		}
		if cursor != events[len(events)-1].Cursor {
			t.Fatal("batch cursor does not match its applied tail")
		}
		batches++
	}
	if batches != backlog/taskStreamMailboxBatchSize {
		t.Fatalf("UI catch-up messages = %d", batches)
	}
	// Following continues without a new attachment once the backlog is drained.
	deliveries <- taskMailboxExactDelivery(backlog+1, "activity-1")
	started := time.Now()
	events, cursor, _, replacement, open, err := mailbox.read(t.Context(), deliveries)
	if err != nil || !open || replacement || len(events) != 1 || cursor != "cursor-4097" || time.Since(started) > time.Second {
		t.Fatalf("live continuation = %d %q replacement=%v open=%v err=%v", len(events), cursor, replacement, open, err)
	}
}

func TestTaskMailboxKeepsReplacementAndActivityBoundaries(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(fmt.Sprintf("incomplete=%v", incomplete), func(t *testing.T) {
			deliveries := make(chan taskstream.Delivery, 10)
			deliveries <- taskMailboxExactDelivery(1, "activity-1")
			deliveries <- taskstream.Delivery{Kind: taskstream.DeliveryReplaceBegin, Source: taskstream.SourceReplacement, SnapshotID: "snapshot"}
			deliveries <- taskstream.Delivery{Kind: taskstream.DeliveryReplacePage, Source: taskstream.SourceReplacement, SnapshotID: "snapshot", Events: []eventstream.Envelope{{EventID: "replacement"}}}
			if !incomplete {
				deliveries <- taskstream.Delivery{Kind: taskstream.DeliveryReplaceEnd, Source: taskstream.SourceReplacement, SnapshotID: "snapshot", Page: 1}
				deliveries <- taskMailboxExactDelivery(2, "activity-1")
				deliveries <- taskMailboxExactDelivery(3, "activity-2")
			}
			close(deliveries)
			mailbox := &taskStreamMailbox{}
			events, cursor, _, replacement, open, err := mailbox.read(t.Context(), deliveries)
			if err != nil || !open || replacement || len(events) != 1 || cursor != "cursor-1" {
				t.Fatalf("exact prefix crossed replacement: events=%v cursor=%q replacement=%v open=%v err=%v", events, cursor, replacement, open, err)
			}
			events, cursor, _, replacement, open, err = mailbox.read(t.Context(), deliveries)
			if incomplete {
				if err == nil || !strings.Contains(err.Error(), "before commit") || len(events) != 0 || cursor != "" || replacement || open {
					t.Fatalf("incomplete replacement became visible: events=%v cursor=%q replacement=%v open=%v err=%v", events, cursor, replacement, open, err)
				}
				return
			}
			if err != nil || !open || !replacement || len(events) != 1 || events[0].EventID != "replacement" || cursor != "" {
				t.Fatalf("replacement did not commit atomically: events=%v cursor=%q replacement=%v open=%v err=%v", events, cursor, replacement, open, err)
			}
			for sequence := 2; sequence <= 3; sequence++ {
				events, cursor, activity, replacement, _, err := mailbox.read(t.Context(), deliveries)
				if err != nil || replacement || len(events) != 1 || cursor != fmt.Sprintf("cursor-%d", sequence) || activity != fmt.Sprintf("activity-%d", sequence-1) {
					t.Fatalf("activity boundary lost: events=%v cursor=%q activity=%q replacement=%v err=%v", events, cursor, activity, replacement, err)
				}
			}
		})
	}
}
