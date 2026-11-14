package runtime

import (
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
)

func TestSubagentLegacySpawnResultFileRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		turn       int64
		result     map[string]any
		wantFinal  string
		wantResult string
	}{
		{name: "first activity missing result", turn: 1, wantFinal: "legacy final", wantResult: "legacy result"},
		{name: "first activity preserves canonical fields", turn: 1, result: map[string]any{"final_message": "canonical final"}, wantFinal: "canonical final", wantResult: "legacy result"},
		{name: "later activity ignores first result", turn: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			root := t.TempDir()
			files := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			active, err := files.StartSession(ctx, session.StartSessionRequest{AppName: "test", UserID: "owner"})
			if err != nil {
				t.Fatal(err)
			}
			runner := &recordingSubagentRunner{spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "current final"}}
			rt, err := New(testConfigWithACPForwarder(Config{
				Sessions: files, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: sessionfile.NewTaskStore(files),
			}))
			if err != nil {
				t.Fatal(err)
			}
			started, err := rt.tasks.StartSubagent(ctx, active, active.SessionRef, runner, taskapi.SubagentStartRequest{Agent: "helper", Prompt: "review"})
			if err != nil {
				t.Fatal(err)
			}
			entry, err := rt.tasks.store.Get(ctx, started.Ref.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := entry.Spec["spawn_result"]; exists {
				t.Fatal("current producer persists a duplicate spawn result")
			}
			entry.Spec["spawn_result"] = map[string]any{"final_message": "legacy final", "result": "legacy result"}
			entry.Spec["turn_seq"] = tc.turn
			entry.Spec["turn_id"] = subagentTurnID(entry.TaskID, tc.turn)
			entry.Result = session.CloneState(tc.result)
			if _, err := rt.tasks.store.(taskapi.CASStore).Put(ctx, taskapi.PutRequest{Entry: entry, ExpectedRevision: entry.Revision}); err != nil {
				t.Fatal(err)
			}
			reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
			restored, err := New(testConfigWithACPForwarder(Config{
				Sessions: reopened, AgentFactory: chat.Factory{}, Subagents: runner, TaskStore: sessionfile.NewTaskStore(reopened),
			}))
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := restored.tasks.Read(ctx, active.SessionRef, taskapi.ControlRequest{TaskID: started.Ref.TaskID, Principal: session.ActorKindTool})
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.State != taskapi.StateCompleted || snapshot.Running || taskRawStringValue(snapshot.Result["final_message"]) != tc.wantFinal || taskRawStringValue(snapshot.Result["result"]) != tc.wantResult {
				t.Fatalf("legacy result restoration = %#v", snapshot)
			}
			assertSubagentSagaModelRoundTrip(t, reopened, active.SessionRef)
		})
	}
}
