package taskstream

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/control/streamspool"
	spoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
)

func TestChildFullyReclaimedWindowReloadsProvider(t *testing.T) {
	for _, mode := range []string{"events", "snapshot", "resume"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			spool, err := spoolfile.New(ctx, spoolfile.Config{RootDir: t.TempDir(), MaxBytes: 8192, MaxStreamBytes: 8192, SegmentBytes: 1024, PartitionAllocationCharge: 128, SegmentAllocationCharge: 64, GCInterval: -1})
			if err != nil {
				t.Fatal(err)
			}
			defer spool.Close()
			recorder := NewRecorder(spool, nil)
			defer recorder.Close(context.Background())
			replay := &spoolReplayFixture{}
			svc, err := New(Config{Tasks: newTaskStreamTestStore(restoredTaskFixture()), Spool: spool, Recorder: recorder, Sessions: replay, SubagentHistory: replay, Authorizer: taskStreamTestAuthorizer{}, Secret: taskStreamTestSecret})
			if err != nil {
				t.Fatal(err)
			}
			first, err := svc.Events(ctx, Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
			if err != nil {
				t.Fatal(err)
			}
			cursor := first.Deliveries[len(first.Deliveries)-1].NextCursor
			logical := streamspool.LogicalKey{Namespace: streamspool.NamespaceTask, Digest: streamspool.DigestStrings("session-1", "task-1")}
			// Independent live writers reclaim the idle child's whole window,
			// while its registration remains available for subsequent activities.
			for n := range 30 {
				writer, err := spool.Register(ctx, streamspool.LogicalKey{Namespace: streamspool.NamespaceTask, Digest: streamspool.DigestStrings("other", fmt.Sprint(n))}, streamspool.WriterOptions{OriginComplete: true})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := writer.Append(ctx, 1, time.Now(), make([]byte, 1024)); err != nil {
					t.Fatal(err)
				}
			}
			_, bounds, err := spool.Resolve(ctx, logical)
			if err != nil || bounds.Low == 0 || bounds.Low != bounds.High {
				t.Fatalf("child window was not fully reclaimed: %+v, %v", bounds, err)
			}
			var deliveries []Delivery
			if mode == "events" {
				result, err := svc.Events(ctx, Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
				if err != nil {
					t.Fatal(err)
				}
				deliveries = result.Deliveries
			} else {
				req := SubscribeRequest{SessionID: "session-1", TaskID: "task-1", HistorySnapshot: true, HistoryTurns: 16}
				if mode == "resume" {
					req.Cursor = cursor
				}
				result, err := svc.Subscribe(ctx, Principal{ID: "owner"}, req)
				if err != nil {
					t.Fatal(err)
				}
				defer result.Subscription.Close()
				for delivery := range result.Subscription.Deliveries() {
					deliveries = append(deliveries, delivery)
				}
				if err := result.Subscription.Err(); err != nil {
					t.Fatal(err)
				}
				if deliveries[0].Kind != DeliveryReplaceBegin || deliveries[len(deliveries)-1].Kind != DeliveryReplaceEnd {
					t.Fatalf("reclaimed history was not replaced atomically: %+v", deliveries)
				}
			}
			var texts []string
			for _, delivery := range deliveries {
				for _, record := range delivery.Records {
					texts = append(texts, session.EventText(record.Frame.Event))
				}
			}
			if got := strings.Join(texts, "|"); replay.loads.Load() != 2 || got != "first prompt|first tool|first answer|second answer" {
				t.Fatalf("provider loads=%d, recovered history=%q", replay.loads.Load(), got)
			}
		})
	}
}

func TestRecorderCompactionPreservesActivityAndAcceptsOversizedOutput(t *testing.T) {
	entry, spool := restoredTaskFixture(), newTaskStreamTestSpool(t)
	r := NewRecorder(spool, nil)
	defer r.Close(context.Background())
	for n := range 4 {
		id := fmt.Sprint(n)
		o := r.BindTaskOutput(t.Context(), output.Binding{SessionID: "session-1", TaskID: "task-1", Kind: output.TaskKindSubagent, ActivityID: id, StartsAtTaskOrigin: n == 0})
		message := model.NewTextMessage(model.RoleAssistant, "answer-"+id)
		if err := o.ObserveTaskOutput(t.Context(), output.Event{Event: &session.Event{Type: session.EventTypeAssistant, Message: &message}}); err != nil {
			t.Fatal(err)
		}
		if n == 3 {
			if err := o.ObserveTaskOutput(t.Context(), output.Event{Event: &session.Event{Meta: map[string]any{"huge": strings.Repeat("x", 3<<20)}}}); err != nil {
				t.Fatal(err)
			}
			if err := o.ObserveTaskOutput(t.Context(), output.Event{State: "completed", Closed: true}); err != nil {
				t.Fatal(err)
			}
		}
		if err := r.Flush(t.Context(), task.Ref{SessionID: "session-1", TaskID: "task-1"}); err != nil {
			t.Fatal(err)
		}
	}
	svc := newTaskStreamTestService(t, newTaskStreamTestStore(entry), spool, r)
	read, err := svc.Events(t.Context(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	answers, notice, terminal := 0, false, false
	for _, d := range read.Deliveries {
		for _, record := range d.Records {
			if strings.HasPrefix(session.EventText(record.Frame.Event), "answer-") {
				if session.EventText(record.Frame.Event) != "answer-"+record.Frame.ActivityID {
					t.Fatal("compaction reassigned an older activity")
				}
				answers++
			}
			notice = notice || strings.Contains(session.EventText(record.Frame.Event), "omitted")
			terminal = terminal || record.Frame.Closed
		}
	}
	if answers != 4 || !notice || !terminal {
		t.Fatalf("answers=%d omission=%v terminal=%v", answers, notice, terminal)
	}
}

func TestChildSubscriptionReplacesExpiredCursorWithRetainedWindow(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	spool, err := spoolfile.New(ctx, spoolfile.Config{RootDir: t.TempDir(), MaxBytes: 8192, MaxStreamBytes: 4096, SegmentBytes: 1024, PartitionAllocationCharge: 128, SegmentAllocationCharge: 64, GCInterval: -1})
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	r := NewRecorder(spool, nil)
	defer r.Close(context.Background())
	entry := restoredTaskFixture()
	o := r.BindTaskOutput(ctx, output.Binding{SessionID: "session-1", TaskID: "task-1", ActivityID: "a", Kind: output.TaskKindSubagent, StartsAtTaskOrigin: true})
	appendText := func(text string) {
		t.Helper()
		msg := model.NewTextMessage(model.RoleAssistant, text)
		if err := o.ObserveTaskOutput(ctx, output.Event{Event: &session.Event{Type: session.EventTypeAssistant, Scope: &session.EventScope{TurnID: "long"}, Message: &msg}}); err != nil {
			t.Fatal(err)
		}
		if err := r.Flush(ctx, task.Ref{SessionID: "session-1", TaskID: "task-1"}); err != nil {
			t.Fatal(err)
		}
	}
	svc := newTaskStreamTestService(t, newTaskStreamTestStore(entry), spool, r)
	appendText("first")
	first, err := svc.Events(ctx, Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
	if err != nil {
		t.Fatal(err)
	}
	cursor := first.Deliveries[len(first.Deliveries)-1].NextCursor
	for n := range 30 {
		appendText(fmt.Sprint(n) + strings.Repeat("x", 300))
	}
	last := "retained latest answer"
	appendText(last)
	result, err := svc.Subscribe(ctx, Principal{ID: "owner"}, SubscribeRequest{SessionID: "session-1", TaskID: "task-1", Cursor: cursor, HistorySnapshot: true, HistoryTurns: 16})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	begin, found, end := false, false, false
	for d := range result.Subscription.Deliveries() {
		begin = begin || d.Kind == DeliveryReplaceBegin
		end = end || d.Kind == DeliveryReplaceEnd
		for _, record := range d.Records {
			found = found || session.EventText(record.Frame.Event) == last
		}
	}
	if err := result.Subscription.Err(); err != nil {
		t.Fatal(err)
	}
	if !begin || !end || !found {
		t.Fatalf("replacement begin=%v end=%v latest=%v", begin, end, found)
	}
}
