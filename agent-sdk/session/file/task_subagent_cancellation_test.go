package file

import (
	"reflect"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestTaskStoreSubagentCancellationPreservesIdentityAndGeneration(t *testing.T) {
	for _, mutation := range []string{"", "identity", "generation", "activity", "spec", "lease", "missing_journal", "foreign_session", "stale_revision", "reopen"} {
		t.Run(mutation, func(t *testing.T) {
			store := NewTaskStore(NewStore(Config{RootDir: t.TempDir()}))
			current, err := store.Put(t.Context(), task.PutRequest{Entry: &task.Entry{
				TaskID: "child", Kind: task.KindSubagent, Session: taskSessionRef("parent"), State: task.StateRunning, Running: true,
				Spec:     map[string]any{"turn_seq": int64(1), "agent_id": "agent", "session_id": "child-session", "prompt": "retained"},
				Metadata: map[string]any{"child_activity_id": "first", "child_activity_generation": int64(1)},
			}})
			if err != nil {
				t.Fatal(err)
			}
			current, err = store.Get(t.Context(), current.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			next := task.CloneEntry(current)
			next.State = task.StateUnknownOutcome
			next.Spec["cancel_phase"], next.Spec["cancel_turn_seq"] = "subagent_cancel_claimed", int64(1)
			next.Metadata["cancel_phase"], next.Metadata["cancel_turn_seq"] = "subagent_cancel_claimed", int64(1)
			expected := current.Revision
			switch mutation {
			case "identity":
				next.Spec["session_id"] = "other-child"
			case "generation":
				next.Spec["turn_seq"] = int64(2)
			case "activity":
				next.Metadata["child_activity_id"] = "other-activity"
			case "spec":
				next.Spec["prompt"] = "replacement"
			case "lease":
				next.Lease.ID = "replacement"
			case "missing_journal":
				delete(next.Metadata, "cancel_phase")
			case "foreign_session":
				next.Session.SessionID = "other-parent"
			case "stale_revision":
				expected--
			case "reopen":
				next.State = task.StateRunning
			}
			ctx := session.ContextWithControlMutation(t.Context(), session.ControlMutationPurposeSubagentCancellation)
			_, err = store.Put(ctx, task.PutRequest{Entry: next, ExpectedRevision: expected})
			if mutation == "" {
				if err != nil {
					t.Fatalf("cancel claim rejected: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("cancellation changed %s", mutation)
				}
				got, loadErr := store.Get(t.Context(), current.TaskID)
				if loadErr != nil || !reflect.DeepEqual(got, current) {
					t.Fatalf("rejected mutation changed Task: %#v, %v", got, loadErr)
				}
			}
		})
	}
}
