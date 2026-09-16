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

func TestRecorderCompactionPreservesEveryActivityBoundary(t *testing.T) {
	for _, combined := range []bool{false, true} {
		t.Run(fmt.Sprintf("combined=%v", combined), func(t *testing.T) {
			spool := newTaskStreamTestSpool(t)
			r := NewRecorder(spool, nil)
			defer r.Close(context.Background())
			at := time.Unix(100, 0).UTC()
			for n := range 3 {
				id := fmt.Sprint(n)
				o := r.BindTaskOutput(t.Context(), output.Binding{SessionID: "session-1", TaskID: "task-1", Kind: output.TaskKindSubagent, ActivityID: id, TerminalID: "terminal-" + id, StartsAtTaskOrigin: n == 0})
				message := model.NewTextMessage(model.RoleAssistant, "answer-"+id)
				event := output.Event{OccurredAt: at.Add(time.Duration(n) * time.Second), Event: &session.Event{Type: session.EventTypeAssistant, Message: &message}, Text: "frame-" + id, State: "running", Running: true}
				if combined {
					event.State, event.Running, event.Closed, event.ExitCode = "completed", false, true, new(n)
				}
				if err := o.ObserveTaskOutput(t.Context(), event); err != nil {
					t.Fatal(err)
				}
				if !combined {
					if err := o.ObserveTaskOutput(t.Context(), output.Event{OccurredAt: event.OccurredAt.Add(time.Millisecond), State: "completed", Closed: true, ExitCode: new(n), Text: "closed-" + id}); err != nil {
						t.Fatal(err)
					}
				}
				if err := r.Flush(t.Context(), task.Ref{SessionID: "session-1", TaskID: "task-1"}); err != nil {
					t.Fatal(err)
				}
			}
			svc := newTaskStreamTestService(t, newTaskStreamTestStore(restoredTaskFixture()), spool, r)
			read, err := svc.Events(t.Context(), Principal{ID: "owner"}, ReadRequest{SessionID: "session-1", TaskID: "task-1"})
			if err != nil {
				t.Fatal(err)
			}
			var seen []string
			for _, d := range read.Deliveries {
				for _, record := range d.Records {
					frame := record.Frame
					id := frame.ActivityID
					if frame.TerminalID != "terminal-"+id {
						t.Fatalf("wrong owner: %+v", frame)
					}
					if frame.Event != nil {
						seen = append(seen, "answer-"+id)
						if frame.Text != "frame-"+id || frame.Running != !combined || session.EventText(frame.Event) != "answer-"+id {
							t.Fatalf("event frame metadata lost: %+v", frame)
						}
					}
					if frame.Closed {
						seen = append(seen, "closed-"+id)
						if frame.State != "completed" || frame.Running || frame.ExitCode == nil || fmt.Sprint(*frame.ExitCode) != id {
							t.Fatalf("terminal frame metadata lost: %+v", frame)
						}
						wantTime := at.Add(time.Duration(*frame.ExitCode) * time.Second)
						if !combined {
							wantTime = wantTime.Add(time.Millisecond)
							if frame.Text != "closed-"+id {
								t.Fatalf("terminal text lost: %+v", frame)
							}
						}
						if !frame.UpdatedAt.Equal(wantTime) {
							t.Fatalf("frame time = %v, want %v", frame.UpdatedAt, wantTime)
						}
					}
				}
			}
			if got := fmt.Sprint(seen); got != "[answer-0 closed-0 answer-1 closed-1 answer-2 closed-2]" {
				t.Fatalf("retained lifecycle order = %s", got)
			}
		})
	}
}
