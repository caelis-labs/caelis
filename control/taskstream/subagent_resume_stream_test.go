package taskstream

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

// A resumed producer can have exact current output without having the Task's
// complete origin. This records the current fallback boundary explicitly.
func TestSubagentMissingOriginReportsGapInsteadOfFinalOnlyFallback(t *testing.T) {
	entry := taskStreamTestEntry("session-1", "task-1", task.KindSubagent)
	entry.State, entry.Running = task.StateRunning, true
	store, spool := newTaskStreamTestStore(entry), newTaskStreamTestSpool(t)
	recorder := NewRecorder(spool, nil)
	observer := recorder.BindTaskOutput(t.Context(), task.OutputBinding{
		SessionID: "session-1", TaskID: "task-1", ActivityID: "resumed", Kind: task.KindSubagent,
		StartsAtTaskOrigin: false,
	})
	if err := observer.ObserveTaskOutput(t.Context(), task.OutputEvent{Running: true,
		Event: &session.Event{Type: session.EventTypeAssistant, Text: "already streaming"},
	}); err != nil {
		t.Fatal(err)
	}
	service := newTaskStreamTestService(t, store, spool, recorder)
	result, err := service.Subscribe(t.Context(), Principal{ID: "owner"}, SubscribeRequest{
		SessionID: "session-1", TaskID: "task-1", Follow: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer result.Subscription.Close()
	if delivery, ok := <-result.Subscription.Deliveries(); ok {
		t.Fatalf("incomplete history delivered as complete: %#v", delivery)
	}
	if result.Subscription.Err() == nil {
		t.Fatal("missing provider replay did not report error")
	}
}
