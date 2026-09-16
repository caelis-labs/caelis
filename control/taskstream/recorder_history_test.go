package taskstream

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/control/history"
)

func TestRecorderStreamsLongHistoryBeforeQueuedLiveOutput(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	entry, spool := restoredTaskFixture(), newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	observer := recorder.BindTaskOutput(ctx, output.Binding{SessionID: "session-1", TaskID: "task-1", ActivityID: "a", Kind: output.TaskKindSubagent})
	const count = 9000
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- observer.(output.StreamingHistoryObserver).ReplaceTaskHistoryStream(ctx, func(ctx context.Context, yield func(*session.Event) error) error {
			close(entered)
			select {
			case <-release:
			case <-ctx.Done():
				return ctx.Err()
			}
			for i := range count {
				message := model.NewTextMessage(model.RoleAssistant, fmt.Sprint(i))
				if err := yield(&session.Event{Type: session.EventTypeAssistant, Message: &message}); err != nil {
					return err
				}
			}
			return nil
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// This callback must return while replay I/O is blocked, yet remain behind
	// the replay's publication in the producer queue.
	if err := observer.ObserveTaskOutput(ctx, output.Event{Text: "live tail", Running: true}); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := recorder.Flush(ctx, task.Ref{SessionID: "session-1", TaskID: "task-1"}); err != nil {
		t.Fatal(err)
	}
	svc := newTaskStreamTestService(t, newTaskStreamTestStore(entry), spool, recorder)
	seen, cursor := 0, ""
	for seen <= history.TranscriptEvents {
		result, err := svc.Events(ctx, Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1", Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, delivery := range result.Deliveries {
			for _, record := range delivery.Records {
				if seen < history.TranscriptEvents && session.EventText(record.Frame.Event) != fmt.Sprint(count-history.TranscriptEvents+seen) || seen == history.TranscriptEvents && record.Frame.Text != "live tail" {
					t.Fatalf("record %d lost or reordered", seen)
				}
				seen++
			}
			if delivery.NextCursor == cursor {
				t.Fatal("history read made no progress")
			}
			cursor = delivery.NextCursor
		}
	}
	if seen != history.TranscriptEvents+1 {
		t.Fatalf("read %d records", seen)
	}
}

func TestRecorderHistoryCancellationJoinsSourceBeforeReturning(t *testing.T) {
	spool := newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	t.Cleanup(func() { _ = recorder.Close(context.Background()) })
	observer := recorder.BindTaskOutput(t.Context(), output.Binding{SessionID: "s", TaskID: "t", Kind: output.TaskKindSubagent})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- observer.(output.StreamingHistoryObserver).ReplaceTaskHistoryStream(ctx, func(ctx context.Context, _ func(*session.Event) error) error {
			close(entered)
			<-ctx.Done()
			close(cancelled)
			<-release
			return ctx.Err()
		})
	}()
	<-entered
	cancel()
	<-cancelled
	select {
	case err := <-done:
		close(release)
		t.Fatalf("returned while source was still owned: %v", err)
	default:
	}
	close(release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation = %v", err)
	}
}
