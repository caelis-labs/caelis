package runtime

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
)

func TestChildInputAdmissionDoesNotAdvanceTask(t *testing.T) {
	r, task, runner := newIdleChildActivityTask(t)
	before, err := r.tasks.store.Get(t.Context(), task.ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = r.SubmitChildInput(t.Context(), task.sessionRef, agent.ChildInputCommand{
		Target: task.handle, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "follow up",
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	req := runner.request
	runner.mu.Unlock()
	if req.ActivityID == "" || req.ActivityID == task.activityID {
		t.Fatalf("follow-up binding = %#v", req)
	}
	assertChildActivityEntry(t, r, task, before.Revision, 1, taskStringValue(before.Metadata[subagentActivityIDMeta]), false)
	// Closing a stable producer with no new activity must not open a turn.
	if err := req.Output.ObserveTaskOutput(t.Context(), output.Event{ProducerClosed: true}); err != nil {
		t.Fatal(err)
	}
	assertChildActivityEntry(t, r, task, before.Revision, 1, taskStringValue(before.Metadata[subagentActivityIDMeta]), false)
	if err := req.Output.ObserveTaskOutput(t.Context(), output.Event{Text: "second", Running: true}); err != nil {
		t.Fatal(err)
	}
	assertChildActivityEntry(t, r, task, before.Revision+1, 2, req.ActivityID, true)
	finishChildActivity(t, req.Completion, "second complete")
	assertChildActivityEntry(t, r, task, 0, 2, req.ActivityID, false)
}

func TestChildActivityCompletionAdvancesWithoutOutput(t *testing.T) {
	r, task, _ := newIdleChildActivityTask(t)
	id, _, completion, err := r.prepareChildTaskOutput(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	finishChildActivity(t, completion, "terminal-only result")
	entry := assertChildActivityEntry(t, r, task, 0, 2, id, false)
	reloaded := r.tasks.rehydrateSubagentTask(entry)
	if got := reloaded.snapshot(); got.State != taskapi.StateCompleted || got.EventCursor != 2 {
		t.Fatalf("durable Task round trip = %#v", got)
	}
	if got := task.snapshot().Result["final_message"]; got != "terminal-only result" {
		t.Fatalf("final result = %v", got)
	}
}

func TestChildActivityCASConflictPreservesCommittedGeneration(t *testing.T) {
	for _, state := range []taskapi.State{taskapi.StateRunning, taskapi.StateCompleted} {
		for _, sameActivity := range []bool{true, false} {
			t.Run(string(state)+map[bool]string{true: "/same", false: "/other"}[sameActivity], func(t *testing.T) {
				r, task, _ := newIdleChildActivityTask(t)
				id, observer, completion, err := r.prepareChildTaskOutput(t.Context(), task)
				if err != nil {
					t.Fatal(err)
				}
				entry, err := r.tasks.store.Get(t.Context(), task.ref.TaskID)
				if err != nil {
					t.Fatal(err)
				}
				entry.State, entry.Running = state, state == taskapi.StateRunning
				entry.Spec["turn_seq"] = int64(2)
				entry.Metadata[subagentActivityIDMeta] = id
				entry.Metadata[subagentActivityGenerationMeta] = int64(2)
				if !sameActivity {
					entry.Metadata[subagentActivityIDMeta] = "other-activity"
				}
				committed, err := r.tasks.store.(taskapi.CASStore).Put(t.Context(), taskapi.PutRequest{Entry: entry, ExpectedRevision: entry.Revision})
				if err != nil {
					t.Fatal(err)
				}
				if err := observer.ObserveTaskOutput(t.Context(), output.Event{Text: "late first frame", Running: true}); err != nil {
					t.Fatal(err)
				}
				assertChildActivityEntry(t, r, task, committed.Revision, 2, taskStringValue(entry.Metadata[subagentActivityIDMeta]), entry.Running)
				if !sameActivity {
					finishChildActivity(t, completion, "stale final")
					assertChildActivityEntry(t, r, task, committed.Revision, 2, "other-activity", entry.Running)
				}
			})
		}
	}
}

func TestChildActivitySidecarFinalSurvivesModelContextRoundTrip(t *testing.T) {
	root := t.TempDir()
	sessions := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	active, err := sessions.StartSession(t.Context(), session.StartSessionRequest{
		AppName: "caelis", UserID: "test", Workspace: session.WorkspaceRef{Key: "workspace", CWD: t.TempDir()},
	})
	if err != nil {
		t.Fatal(err)
	}
	runner := &runtimeChildInputRunner{spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "first sidecar final"}}
	r, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, TaskStore: sessionfile.NewTaskStore(sessions), AgentFactory: chat.Factory{}, Subagents: runner}))
	if err != nil {
		t.Fatal(err)
	}
	started, err := r.StartSubagent(t.Context(), active.SessionRef, "helper", "inspect", "test")
	if err != nil {
		t.Fatal(err)
	}
	task, err := r.tasks.lookupSubagent(t.Context(), active.SessionRef, started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	_, observer, completion, err := r.prepareChildTaskOutput(t.Context(), task)
	if err != nil {
		t.Fatal(err)
	}
	if err := observer.ObserveTaskOutput(t.Context(), output.Event{Text: "disposable partial trace", Running: true}); err != nil {
		t.Fatal(err)
	}
	finishChildActivity(t, completion, "follow-up sidecar final")

	reopened := sessionfile.NewStore(sessionfile.Config{RootDir: root})
	rebuilt, err := New(testConfigWithACPForwarder(Config{Sessions: reopened, TaskStore: sessionfile.NewTaskStore(reopened), AgentFactory: chat.Factory{}}))
	if err != nil {
		t.Fatal(err)
	}
	probe := &capturingContextModel{messages: make(chan []model.Message, 1)}
	run, err := rebuilt.Run(t.Context(), agent.RunRequest{SessionRef: active.SessionRef, Input: "read persisted context", AgentSpec: agent.AgentSpec{Name: "chat", Model: probe}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := drainRunnerEvents(t, run.Handle); err != nil {
		t.Fatal(err)
	}
	var contextText strings.Builder
	for _, message := range <-probe.messages {
		contextText.WriteString(message.TextContent())
		contextText.WriteByte('\n')
	}
	for _, final := range []string{"first sidecar final", "follow-up sidecar final"} {
		if strings.Count(contextText.String(), final) != 1 {
			t.Fatalf("model context must contain final once: %q in %q", final, contextText.String())
		}
	}
	if strings.Contains(contextText.String(), "disposable partial trace") {
		t.Fatal("transient child trace entered durable model context")
	}
}

func TestChildActivityOutputDoesNotWaitForTaskControl(t *testing.T) {
	for _, terminal := range []bool{false, true} {
		t.Run(map[bool]string{false: "running", true: "terminal"}[terminal], func(t *testing.T) {
			r, task, _ := newIdleChildActivityTask(t)
			id, observer, completion, err := r.prepareChildTaskOutput(t.Context(), task)
			if err != nil {
				t.Fatal(err)
			}
			observed := &childActivityOutputCounter{}
			observer.(*childTaskActivity).observer = observed
			release, claimed := r.tasks.tryClaimSubagentOperation(task.sessionRef, task.ref.TaskID)
			if !claimed {
				t.Fatal("could not claim Task control")
			}
			// This journal targets the accepted runner turn before its first frame.
			_, err = r.tasks.persistSubagentCancelPhase(t.Context(), task, 2, subagentCancelPhaseClaimed, "cancel pending", nil, false)
			if err != nil {
				release()
				t.Fatal(err)
			}
			returned := make(chan struct{})
			go func() {
				_ = observer.ObserveTaskOutput(t.Context(), output.Event{Text: "first", Running: !terminal, Closed: terminal})
				close(returned)
			}()
			select {
			case <-returned:
			case <-time.After(time.Second):
				release()
				t.Fatal("producer output waited for Task control")
			}
			if observed.count.Load() != 1 {
				release()
				t.Fatal("output was not forwarded")
			}
			var done <-chan struct{}
			if terminal {
				done = completion.(subagentCompletionSink).enqueue(delegation.Result{State: delegation.StateCompleted, Result: "done"})
			}
			release()
			if terminal {
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Fatal("terminal acknowledgement remained parked")
				}
			} else {
				deadline := time.Now().Add(2 * time.Second)
				for {
					entry, err := r.tasks.store.Get(t.Context(), task.ref.TaskID)
					if err == nil && taskTurnSeqFromSpec(entry.Spec) == 2 {
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("deferred first output did not advance Task: entry=%#v load=%v", entry, err)
					}
					time.Sleep(time.Millisecond)
				}
			}
			entry := assertChildActivityEntry(t, r, task, 0, 2, id, !terminal)
			if seq, ok := subagentCancelTurnSeq(entry.Metadata); !ok || seq != 2 {
				t.Fatalf("cancel generation lost: %#v", entry.Metadata)
			}
			if !terminal {
				finishChildActivity(t, completion, "done")
			}
		})
	}
}

func newIdleChildActivityTask(t *testing.T) (*Runtime, *subagentTask, *runtimeChildInputRunner) {
	t.Helper()
	runner := &runtimeChildInputRunner{spawnResult: delegation.Result{State: delegation.StateCompleted, Result: "first done"}}
	r, active := newSubagentTaskTestRuntime(t, runner)
	_, err := r.sessions.BindController(t.Context(), session.BindControllerRequest{
		SessionRef: active.SessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest),
		Binding: session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "controller-1", AgentName: "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := r.tasks.StartSubagent(t.Context(), active, active.SessionRef, runner, taskapi.SubagentStartRequest{Agent: "helper", Prompt: "first"})
	if err != nil {
		t.Fatal(err)
	}
	task, err := r.tasks.lookupSubagent(t.Context(), active.SessionRef, started.Ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	return r, task, runner
}

func assertChildActivityEntry(t *testing.T, r *Runtime, task *subagentTask, revision uint64, turn int64, activity string, running bool) *taskapi.Entry {
	t.Helper()
	entry, err := r.tasks.store.Get(t.Context(), task.ref.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if revision != 0 && entry.Revision != revision || taskTurnSeqFromSpec(entry.Spec) != turn || entry.Running != running || taskStringValue(entry.Metadata[subagentActivityIDMeta]) != activity {
		t.Fatalf("Task activity = %#v, want revision=%d turn=%d activity=%q running=%v", entry, revision, turn, activity, running)
	}
	return entry
}

func finishChildActivity(t *testing.T, sink delegation.CompletionSink, result string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		sink.PublishSubagentCompletion(delegation.Result{State: delegation.StateCompleted, Result: result})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("child completion did not acknowledge durable state")
	}
}

type childActivityOutputCounter struct{ count atomic.Int64 }

func (o *childActivityOutputCounter) ObserveTaskOutput(context.Context, output.Event) error {
	o.count.Add(1)
	return nil
}
