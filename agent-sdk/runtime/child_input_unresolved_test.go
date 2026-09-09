package runtime

import (
	"testing"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
)

func TestChildInputRejectsPersistedUnresolvedExecution(t *testing.T) {
	r, task, runner := newLocalControllerChildActivityTask(t)
	task.mu.Lock()
	task.applyResult(delegation.Result{State: delegation.StateUnknownOutcome, Error: "prompt observation lost"})
	entry := task.entrySnapshot(r.now())
	task.mu.Unlock()
	if err := r.tasks.persistTaskEntryWithConflictInvalidation(t.Context(), entry, false); err != nil {
		t.Fatal(err)
	}
	stored, err := r.tasks.store.Get(t.Context(), task.ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	// Discard process-local activity state. The persisted result must still
	// prevent endpoint binding/resume from turning an unresolved Run into idle.
	r.tasks.mu.Lock()
	r.tasks.subagents[task.ref.TaskID] = r.tasks.rehydrateSubagentTask(stored)
	r.tasks.mu.Unlock()
	_, err = r.SubmitChildInput(t.Context(), task.sessionRef, agent.ChildInputCommand{
		Target: task.handle,
		Source: session.ControllerExecutor(session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "controller-1", AgentName: "local"}),
		Input:  "do not start a second Run",
	})
	if !errorcode.Is(err, errorcode.UnknownOutcome) {
		t.Fatalf("unresolved input = %v", err)
	}
	runner.mu.Lock()
	defer runner.mu.Unlock()
	if runner.request.Input != "" {
		t.Fatalf("unresolved input dispatched: %#v", runner.request)
	}
	after, err := r.tasks.store.Get(t.Context(), task.ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != stored.Revision || after.State != taskapi.StateUnknownOutcome {
		t.Fatalf("rejected input mutated Task: %#v", after)
	}
}
