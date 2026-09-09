package runtime

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/output"
)

type collaborationLifecycleRunner struct {
	*runtimeChildInputRunner
	inputEntered chan struct{}
	inputRelease chan struct{}
	waitEntered  chan struct{}
	waitRelease  chan struct{}
}

func (r *collaborationLifecycleRunner) SubmitChildInput(ctx context.Context, req agent.ChildInputRequest) (agent.ChildInputResult, error) {
	result, err := r.runtimeChildInputRunner.SubmitChildInput(ctx, req)
	if r.inputEntered != nil {
		close(r.inputEntered)
		<-r.inputRelease
	}
	return result, err
}
func (r *collaborationLifecycleRunner) Wait(context.Context, delegation.Anchor, int) (delegation.Result, error) {
	close(r.waitEntered)
	<-r.waitRelease
	return delegation.Result{State: delegation.StateRunning, Running: true}, nil
}
func TestSubagentWaitObservesFollowupAfterReadingFirstResult(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, child, base := newIdleChildActivityTaskWithRole(t, session.ParticipantRoleSidecar)
		first, err := r.tasks.Read(t.Context(), child.sessionRef, task.ControlRequest{TaskID: child.ref.TaskID, Principal: session.ActorKindUser})
		if err != nil || first.Result["final_message"] != "first done" {
			t.Fatalf("first read: %#v %v", first, err)
		}
		runner := &collaborationLifecycleRunner{runtimeChildInputRunner: base, waitEntered: make(chan struct{}), waitRelease: make(chan struct{})}
		child.runner = runner
		_, err = r.SubmitChildInput(t.Context(), child.sessionRef, agent.ChildInputCommand{Target: child.handle, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "follow up"})
		if err != nil {
			t.Fatal(err)
		}
		if err := base.request.Output.ObserveTaskOutput(t.Context(), output.Event{Text: "working", Running: true}); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := r.WaitSubagentTask(t.Context(), child.sessionRef, child.ref.TaskID, time.Second)
			done <- err
		}()
		synctest.Wait()
		select {
		case <-runner.waitEntered:
		default:
			t.Fatal("Wait returned before observing the current activity")
		}
		select {
		case err := <-done:
			t.Fatalf("Wait returned early: %v", err)
		default:
		}
		close(runner.waitRelease)
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}
func TestSettledDetachSerializesWithAdmittedFollowup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, child, base := newIdleChildActivityTask(t)
		r.controllers = stubACPController{}
		active, err := r.sessions.Session(t.Context(), child.sessionRef)
		if err != nil {
			t.Fatal(err)
		}
		binding := active.Participants[0]
		req := agent.DetachParticipantRequest{SessionRef: child.sessionRef, ParticipantID: binding.ID, RequireSettled: true, ExpectedDelegationID: binding.DelegationID, ExpectedAttachmentGeneration: binding.AttachmentGeneration}
		runner := &collaborationLifecycleRunner{runtimeChildInputRunner: base, inputEntered: make(chan struct{}), inputRelease: make(chan struct{})}
		child.runner = runner
		sent := make(chan error, 1)
		go func() {
			_, err := r.SubmitChildInput(t.Context(), child.sessionRef, agent.ChildInputCommand{Target: child.handle, Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "follow up"})
			sent <- err
		}()
		<-runner.inputEntered
		removed := make(chan error, 1)
		go func() { _, err := r.DetachParticipant(t.Context(), req); removed <- err }()
		close(runner.inputRelease)
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		if err := <-removed; err == nil {
			t.Fatal("removed admitted activity before first output")
		}
		finishChildActivity(t, base.request.Completion, "followup done")
		stale := req
		stale.ExpectedDelegationID = "replaced"
		if _, err := r.DetachParticipant(t.Context(), stale); err == nil {
			t.Fatal("stale identity detached participant")
		}
		if _, err := r.DetachParticipant(t.Context(), req); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSettledDetachRejectsRunningAndUnknownTasks(t *testing.T) {
	for _, state := range []task.State{task.StateRunning, task.StateUnknownOutcome} {
		t.Run(string(state), func(t *testing.T) {
			r, child, _ := newIdleChildActivityTask(t)
			r.controllers = stubACPController{}
			active, err := r.sessions.Session(t.Context(), child.sessionRef)
			if err != nil {
				t.Fatal(err)
			}
			binding := active.Participants[0]
			entry, err := r.tasks.store.Get(t.Context(), binding.DelegationID)
			if err != nil {
				t.Fatal(err)
			}
			entry.State = state
			entry.Running = state == task.StateRunning
			if err := r.tasks.store.Upsert(t.Context(), entry); err != nil {
				t.Fatal(err)
			}
			_, err = r.DetachParticipant(t.Context(), agent.DetachParticipantRequest{SessionRef: child.sessionRef, ParticipantID: binding.ID, RequireSettled: true, ExpectedDelegationID: binding.DelegationID, ExpectedAttachmentGeneration: binding.AttachmentGeneration})
			if err == nil {
				t.Fatal("unsettled participant removed")
			}
			current, err := r.sessions.Session(t.Context(), child.sessionRef)
			if err != nil || len(current.Participants) != 1 {
				t.Fatalf("roster changed: %#v %v", current, err)
			}
		})
	}
}
