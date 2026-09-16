package taskstream

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/session/file"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	spoolfile "github.com/caelis-labs/caelis/control/streamspool/file"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

func TestCompactedSpoolReplacementProjectsEveryActivityFinal(t *testing.T) {
	for _, combined := range []bool{false, true} {
		t.Run(fmt.Sprint(combined), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			spool, err := spoolfile.New(ctx, spoolfile.Config{RootDir: t.TempDir(), GCInterval: -1})
			if err != nil {
				t.Fatal(err)
			}
			defer spool.Close()
			recorder := controltaskstream.NewRecorder(spool, nil)
			defer recorder.Close(context.Background())
			tasks := file.NewTaskStore(file.NewStore(file.Config{RootDir: t.TempDir()}))
			if err := tasks.Upsert(ctx, &task.Entry{TaskID: "child", Session: session.SessionRef{SessionID: "parent"}, Kind: task.KindSubagent, State: task.StateCompleted,
				Metadata: map[string]any{"parent_call": "spawn", "child_activity_id": "2"}}); err != nil {
				t.Fatal(err)
			}
			for n := range 3 {
				id := fmt.Sprint(n)
				observer := recorder.BindTaskOutput(ctx, output.Binding{SessionID: "parent", TaskID: "child", Kind: output.TaskKindSubagent, ActivityID: id, TerminalID: "stable-child", StartsAtTaskOrigin: n == 0})
				frame := output.Event{Event: &session.Event{Type: session.EventTypeAssistant, Visibility: session.VisibilityUIOnly,
					Protocol: &session.EventProtocol{Method: session.ProtocolMethodSessionUpdate, Update: &session.ProtocolUpdate{SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage), Content: session.ProtocolTextContent("answer-" + id)}}}}
				if combined {
					frame.State, frame.Closed = "completed", true
				}
				if err := observer.ObserveTaskOutput(ctx, frame); err != nil {
					t.Fatal(err)
				}
				if !combined {
					if err := observer.ObserveTaskOutput(ctx, output.Event{State: "completed", Closed: true}); err != nil {
						t.Fatal(err)
					}
				}
				if err := recorder.Flush(ctx, task.Ref{SessionID: "parent", TaskID: "child"}); err != nil {
					t.Fatal(err)
				}
			}
			control, err := controltaskstream.New(controltaskstream.Config{Tasks: tasks, Spool: spool, Recorder: recorder, Authorizer: compactionReplayAuthorizer{}, Secret: make([]byte, 32)})
			if err != nil {
				t.Fatal(err)
			}
			result, err := New(control).Subscribe(ctx, Principal{ID: "owner"}, SubscribeRequest{SessionID: "parent", TaskID: "child", HistorySnapshot: true, HistoryTurns: 16})
			if err != nil {
				t.Fatal(err)
			}
			defer result.Subscription.Close()
			var phases []DeliveryKind
			last := make(map[string]eventstream.Envelope)
			for delivery := range result.Subscription.Deliveries() {
				phases = append(phases, delivery.Kind)
				for _, event := range delivery.Events {
					if event.ParentTool == nil || event.ParentTool.ToolCallID != "spawn" || event.ScopeID != "child" {
						t.Fatalf("lost child identity: %+v", event)
					}
					last[event.TurnID] = event
				}
			}
			if err := result.Subscription.Err(); err != nil {
				t.Fatal(err)
			}
			if len(phases) < 3 || phases[0] != DeliveryReplaceBegin || phases[len(phases)-1] != DeliveryReplaceEnd || len(last) != 3 {
				t.Fatalf("incomplete replacement: phases=%v turns=%v", phases, last)
			}
			for n := range 3 {
				event := last[fmt.Sprint(n)]
				if !event.Final || (!combined && (event.Lifecycle == nil || event.Lifecycle.State != "completed")) {
					t.Fatalf("activity %d has no terminal projection: %+v", n, event)
				}
			}
		})
	}
}

type compactionReplayAuthorizer struct{}

func (compactionReplayAuthorizer) AuthorizeTaskStream(context.Context, Principal, string) error {
	return nil
}
