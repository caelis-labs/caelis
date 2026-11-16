package runtime

import (
	"reflect"
	"testing"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestCanonicalTaskResultPreservesProducerAuthority(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		kind   taskapi.Kind
		output map[string]any
	}{
		{name: "earlier running observation", kind: taskapi.KindCommand, output: map[string]any{"state": "running", "result": "partial"}},
		{name: "different terminal outcome", kind: taskapi.KindCommand, output: map[string]any{"state": "failed", "error": "older failure"}},
		{name: "state-only cancel receipt", kind: taskapi.KindCommand, output: map[string]any{"state": "completed"}},
		{name: "transport status is not producer state", kind: taskapi.KindCommand, output: map[string]any{"result": "unbound result"}},
		{name: "earlier child activity", kind: taskapi.KindSubagent, output: map[string]any{"state": "completed", "turn_id": "observed-task:1", "final_message": "old activity"}},
		{name: "child result without activity", kind: taskapi.KindSubagent, output: map[string]any{"state": "completed", "final_message": "unbound activity"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, active, rt := newRuntimeRunCommandToolTestHarness(t)
			rt.tasks.store = newFileTaskStoreForTest(t)
			entry := &taskapi.Entry{
				TaskID: "observed-task", Kind: tc.kind, Session: active.SessionRef, State: taskapi.StateCompleted,
				Spec:   map[string]any{"turn_seq": 2, "turn_id": "observed-task:2"},
				Result: map[string]any{"state": "completed", "result": "producer final", "exit_code": 0},
			}
			if err := rt.tasks.store.Upsert(t.Context(), entry); err != nil {
				t.Fatal(err)
			}
			before, err := rt.tasks.store.Get(t.Context(), entry.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			output := session.CloneState(tc.output)
			output["task_id"], output["target_kind"] = entry.TaskID, string(tc.kind)
			if err := rt.tasks.syncCanonicalToolResult(t.Context(), active.SessionRef, &session.Event{
				Type: session.EventTypeToolResult, Time: time.Now(), Meta: trustedTaskResultMeta(nil),
				Tool: &session.EventTool{Name: "Task", Status: "completed", Output: output},
			}); err != nil {
				t.Fatal(err)
			}
			after, err := rt.tasks.store.Get(t.Context(), entry.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatalf("observation changed the producer Task: before=%#v after=%#v", before, after)
			}
		})
	}
}

func TestCanonicalTaskResultRecoveryPreservesProducerAuthority(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name         string
		kind         taskapi.Kind
		state        taskapi.State
		storedFinal  string
		turn         int64
		historyTurn  string
		wantFinal    string
		exitOnlyTail bool
	}{
		{name: "missing command final survives later running history", kind: taskapi.KindCommand, state: taskapi.StateCompleted, wantFinal: "history final"},
		{name: "missing command final survives newer indexed exit-only receipt", kind: taskapi.KindCommand, state: taskapi.StateCompleted, wantFinal: "history final", exitOnlyTail: true},
		{name: "complete producer final wins over newer history", kind: taskapi.KindCommand, state: taskapi.StateCompleted, storedFinal: "producer final", wantFinal: "producer final"},
		{name: "history cannot change producer outcome", kind: taskapi.KindCommand, state: taskapi.StateFailed},
		{name: "unknown outcome stays with producer", kind: taskapi.KindCommand, state: taskapi.StateUnknownOutcome},
		{name: "matching child activity fills missing final", kind: taskapi.KindSubagent, state: taskapi.StateCompleted, turn: 2, historyTurn: "history-task:2", wantFinal: "history final"},
		{name: "previous child activity cannot fill current final", kind: taskapi.KindSubagent, state: taskapi.StateCompleted, turn: 2, historyTurn: "history-task:1"},
		{name: "unbound child history cannot fill later activity", kind: taskapi.KindSubagent, state: taskapi.StateCompleted, turn: 2},
		{name: "unbound legacy child history fills first activity", kind: taskapi.KindSubagent, state: taskapi.StateCompleted, turn: 1, wantFinal: "history final"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sessions, active, rt := newRuntimeRunCommandToolTestHarness(t)
			rt.tasks.store = newFileTaskStoreForTest(t)
			at := time.Now().Add(-time.Minute)
			entry := &taskapi.Entry{
				TaskID: "history-task", Kind: tc.kind, Session: active.SessionRef, State: tc.state, UpdatedAt: at,
				Spec:   map[string]any{"turn_seq": tc.turn, "turn_id": subagentTurnID("history-task", tc.turn)},
				Result: map[string]any{"state": string(tc.state), "exit_code": 0},
			}
			if tc.storedFinal != "" {
				entry.Result["result"] = tc.storedFinal
			}
			if tc.exitOnlyTail {
				entry.UpdatedAt = at.Add(3 * time.Second)
			}
			if err := rt.tasks.store.Upsert(t.Context(), entry); err != nil {
				t.Fatal(err)
			}
			states := []string{"completed", "running"}
			if tc.exitOnlyTail {
				states = append(states, "completed")
			}
			for i, state := range states {
				output := map[string]any{"task_id": entry.TaskID, "target_kind": string(tc.kind), "state": state, "result": "history final", "exit_code": 99}
				if i == 2 {
					delete(output, "result")
				}
				if tc.historyTurn != "" {
					output["turn_id"] = tc.historyTurn
				}
				if _, err := sessions.AppendEvent(t.Context(), session.AppendEventRequest{SessionRef: active.SessionRef, Event: &session.Event{
					Type: session.EventTypeToolResult, Time: at.Add(time.Duration(i+1) * time.Second), Meta: trustedTaskResultMeta(nil),
					Tool: &session.EventTool{Name: "Task", Status: "completed", Output: output},
				}}); err != nil {
					t.Fatal(err)
				}
			}
			stored, err := rt.tasks.store.Get(t.Context(), entry.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			recovered, err := rt.tasks.recoverCanonicalTaskResult(t.Context(), active.SessionRef, stored)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.State != tc.state || recovered.Running || taskRawStringValue(recovered.Result["result"]) != tc.wantFinal {
				t.Fatalf("legacy recovery changed producer authority: %#v", recovered)
			}
			if code, _ := taskInt64Value(recovered.Result["exit_code"]); code != 0 {
				t.Fatalf("recovery replaced producer exit code with %d", code)
			}
			if recovered.UpdatedAt.Before(stored.UpdatedAt) {
				t.Fatalf("recovery regressed the producer timestamp: %v -> %v", stored.UpdatedAt, recovered.UpdatedAt)
			}
		})
	}
}
