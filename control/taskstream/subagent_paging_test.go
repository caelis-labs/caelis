package taskstream

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
)

func TestChildExactOriginPagesLongHistoryAndFollowsOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	entry, spool := restoredTaskFixture(), newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	observer := recorder.BindTaskOutput(ctx, output.Binding{SessionID: "session-1", TaskID: "task-1", ActivityID: "a", Kind: output.TaskKindSubagent, StartsAtTaskOrigin: true})
	const count = maxReplacementRecords + 1
	for i := range count {
		if err := observer.ObserveTaskOutput(ctx, output.Event{Text: fmt.Sprint(i), Closed: i == 12}); err != nil {
			t.Fatal(err)
		}
		if i%maxDeliveryRecords == 0 {
			if err := recorder.Flush(ctx, task.Ref{SessionID: "session-1", TaskID: "task-1"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), spool, recorder)
	checkPage := func(d Delivery, seen *int) {
		t.Helper()
		if d.Kind != DeliveryAppendPage || d.NextCursor == "" || len(d.Records) > maxDeliveryRecords {
			t.Fatalf("not a bounded exact page: kind=%s records=%d", d.Kind, len(d.Records))
		}
		for _, r := range d.Records {
			if r.Sequence != uint64(*seen+1) || r.Frame == nil || r.Frame.Text != fmt.Sprint(*seen) {
				t.Fatalf("record %d lost or repeated: %#v", *seen, r)
			}
			*seen++
		}
	}
	// Finite reads carry an exact cursor from the first page onward.
	cursor, seen := "", 0
	for seen < count {
		result, err := service.Events(ctx, Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1", Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Deliveries) != 1 || len(result.Deliveries[0].Records) == 0 {
			t.Fatal("history pagination made no progress")
		}
		checkPage(result.Deliveries[0], &seen)
		cursor = result.Deliveries[0].NextCursor
	}
	for _, follow := range []bool{false, true} {
		result, err := service.Subscribe(ctx, Principal{ID: "owner"}, SubscribeRequest{SessionID: "session-1", TaskID: "task-1", Follow: follow})
		if err != nil {
			t.Fatal(err)
		}
		defer result.Subscription.Close()
		seen := 0
		for seen < count {
			checkPage(nextTaskDelivery(t, ctx, result.Subscription), &seen)
		}
		if !follow {
			select {
			case _, open := <-result.Subscription.Deliveries():
				if open || result.Subscription.Err() != nil {
					t.Fatal("finite history did not finish cleanly")
				}
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			continue
		}
		if err := observer.ObserveTaskOutput(ctx, output.Event{Text: fmt.Sprint(count), Running: true}); err != nil {
			t.Fatal(err)
		}
		checkPage(nextTaskDelivery(t, ctx, result.Subscription), &seen)
		if seen != count+1 {
			t.Fatal("live tail was not delivered")
		}
		// A recorder replacement still replaces the already visible prefix.
		message := model.NewTextMessage(model.RoleAssistant, "authoritative replay")
		if err := observer.(output.HistoryObserver).ReplaceTaskHistory(ctx, []*session.Event{{Type: session.EventTypeAssistant, Text: "authoritative replay", Message: &message}}); err != nil {
			t.Fatal(err)
		}
		if err := recorder.Flush(ctx, task.Ref{SessionID: "session-1", TaskID: "task-1"}); err != nil {
			t.Fatal(err)
		}
		if d := nextTaskDelivery(t, ctx, result.Subscription); d.Kind != DeliveryReplaceBegin {
			t.Fatalf("new incarnation appended to old history: %#v", d)
		}
		if d := nextTaskDelivery(t, ctx, result.Subscription); d.Kind != DeliveryReplacePage || len(d.Records) != 1 || session.EventText(d.Records[0].Frame.Event) != "authoritative replay" {
			t.Fatalf("replacement lost history: %#v", d)
		}
		if d := nextTaskDelivery(t, ctx, result.Subscription); d.Kind != DeliveryReplaceEnd || d.NextCursor == "" {
			t.Fatal("replacement did not commit with its live cursor")
		}
	}
}

func TestChildSnapshotPagesPastLegacyLimitAndJoinsLiveAtCapturedBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	entry, spool := restoredTaskFixture(), newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	observer := recorder.BindTaskOutput(ctx, output.Binding{SessionID: "session-1", TaskID: "task-1", ActivityID: "a", Kind: output.TaskKindSubagent, StartsAtTaskOrigin: true})
	const count = maxReplacementRecords + 1
	for i := range count {
		if err := observer.ObserveTaskOutput(ctx, output.Event{Text: fmt.Sprint(i)}); err != nil {
			t.Fatal(err)
		}
		if i%maxDeliveryRecords == 0 {
			if err := recorder.Flush(ctx, task.Ref{SessionID: "session-1", TaskID: "task-1"}); err != nil {
				t.Fatal(err)
			}
		}
	}
	svc := newTaskStreamTestService(t, newTaskStreamTestStore(entry), spool, recorder)
	result, err := svc.Subscribe(ctx, Principal{ID: "owner"}, SubscribeRequest{SessionID: "session-1", TaskID: "task-1", Follow: true, HistorySnapshot: true})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	begin := nextTaskDelivery(t, ctx, result.Subscription)
	if begin.Kind != DeliveryReplaceBegin {
		t.Fatalf("begin=%s", begin.Kind)
	}
	// Publication happens after the reader's captured boundary but before its
	// first history page is consumed. It must appear exactly once after commit.
	if err := observer.ObserveTaskOutput(ctx, output.Event{Text: "live tail", Running: true}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(ctx, task.Ref{SessionID: "session-1", TaskID: "task-1"}); err != nil {
		t.Fatal(err)
	}
	seen, pages := 0, uint32(0)
	for {
		d := nextTaskDelivery(t, ctx, result.Subscription)
		if d.SnapshotID != begin.SnapshotID || d.Page != pages {
			t.Fatal("snapshot identity or order changed")
		}
		if d.Kind == DeliveryReplaceEnd {
			if seen != count || d.NextCursor == "" {
				t.Fatalf("end seen=%d cursor=%q", seen, d.NextCursor)
			}
			break
		}
		if d.Kind != DeliveryReplacePage || len(d.Records) > maxDeliveryRecords || d.NextCursor != "" {
			t.Fatal("unbounded or resumable intermediate page")
		}
		for _, r := range d.Records {
			if r.Frame.Text != fmt.Sprint(seen) {
				t.Fatalf("event %d lost or repeated", seen)
			}
			seen++
		}
		pages++
	}
	live := nextTaskDelivery(t, ctx, result.Subscription)
	if live.Kind != DeliveryAppendPage || len(live.Records) != 1 || live.Records[0].Frame.Text != "live tail" || live.NextCursor == "" {
		t.Fatalf("invalid live continuation: %#v", live)
	}
}

func TestChildHistoryWindowsKeepLiveCursorIndependent(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	entry, spool := restoredTaskFixture(), newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	observer := recorder.BindTaskOutput(ctx, output.Binding{SessionID: "session-1", TaskID: "task-1", ActivityID: "a", Kind: output.TaskKindSubagent, StartsAtTaskOrigin: true})
	for n := range 40 {
		message := model.NewTextMessage(model.RoleAssistant, fmt.Sprint(n))
		err := observer.ObserveTaskOutput(ctx, output.Event{Event: &session.Event{Type: session.EventTypeAssistant, Scope: &session.EventScope{TurnID: fmt.Sprint(n)}, Message: &message}})
		if err != nil {
			t.Fatal(err)
		}
	}
	service := newTaskStreamTestService(t, newTaskStreamTestStore(entry), spool, recorder)
	recent, err := service.Subscribe(ctx, Principal{ID: "owner"}, SubscribeRequest{SessionID: "session-1", TaskID: "task-1", Follow: true, HistorySnapshot: true, HistoryTurns: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer recent.Subscription.Close()
	read := func(sub Subscription) (int, string, string) {
		t.Helper()
		count := 0
		for {
			d := nextTaskDelivery(t, ctx, sub)
			count += len(d.Records)
			if d.Kind == DeliveryReplaceEnd {
				return count, d.HistoryBefore, d.NextCursor
			}
		}
	}
	count, before, cursor := read(recent.Subscription)
	if count != 16 || before == "" || cursor == "" {
		t.Fatalf("initial count %d", count)
	}
	if err := observer.ObserveTaskOutput(ctx, output.Event{Text: "live", Running: true}); err != nil {
		t.Fatal(err)
	}
	for before != "" {
		older, err := service.Subscribe(ctx, Principal{ID: "owner"}, SubscribeRequest{SessionID: "session-1", TaskID: "task-1", HistoryBefore: before, HistoryTurns: 16})
		if err != nil {
			t.Fatal(err)
		}
		n, next, live := read(older.Subscription)
		if live != "" {
			t.Fatal("older advanced live cursor")
		}
		count += n
		before = next
		for range older.Subscription.Deliveries() {
			t.Fatal("finite child request followed")
		}
		if err := older.Subscription.Err(); err != nil {
			t.Fatal(err)
		}
		older.Subscription.Close()
	}
	if count != 40 {
		t.Fatalf("count=%d", count)
	}
	live := nextTaskDelivery(t, ctx, recent.Subscription)
	if len(live.Records) != 1 || live.Records[0].Frame.Text != "live" || live.Records[0].Sequence != 41 {
		t.Fatal("live boundary moved")
	}
}
