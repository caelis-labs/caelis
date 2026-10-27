package runtime

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
)

func TestAgentInputBatchValidatesEverySenderBeforeSingleChildDispatch(t *testing.T) {
	r, task, runner := newLocalControllerChildActivityTask(t)
	source := session.ParticipantBinding{ID: "sibling", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated, SessionID: "sibling-session", DelegationID: "sibling-task", AttachmentGeneration: "generation-a", AgentName: "sibling"}
	if _, err := r.sessions.PutParticipant(t.Context(), session.PutParticipantRequest{SessionRef: task.sessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeTest), Binding: source}); err != nil {
		t.Fatal(err)
	}
	entries := []agent.AgentInputBatchEntry{
		{Message: agent.AgentCommunicationInput{Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "parent text"}},
		{Message: agent.AgentCommunicationInput{Input: "sibling text"}, Participant: &source},
	}
	stale := session.CloneParticipantBinding(source)
	stale.AttachmentGeneration = "old"
	entries[1].Participant = &stale
	if err := r.SubmitAgentInputBatch(t.Context(), task.sessionRef, task.handle, entries, nil); !errorcode.Is(err, errorcode.PermissionDenied) {
		t.Fatalf("stale source: %v", err)
	}
	runner.mu.Lock()
	got := agent.CloneChildInputRequest(runner.request)
	runner.mu.Unlock()
	if len(got.Messages) != 0 || got.Input != "" {
		t.Fatalf("partially dispatched: %#v", got)
	}
	entries[1].Participant = &source
	if err := r.SubmitAgentInputBatch(t.Context(), task.sessionRef, task.handle, entries, nil); err != nil {
		t.Fatal(err)
	}
	runner.mu.Lock()
	got = agent.CloneChildInputRequest(runner.request)
	runner.mu.Unlock()
	if len(got.Messages) != 2 || session.ActorRefHasIdentity(got.Source) || got.Input != "" {
		t.Fatalf("not one attributed batch: %#v", got)
	}
	if got.Messages[0].Source != session.ParentCommunicationActor() || got.Messages[1].Source.ID != source.ID || got.Messages[0].Input != "parent text" || got.Messages[1].Input != "sibling text" {
		t.Fatalf("lost source/order: %#v", got.Messages)
	}
}

func (r *runtimeChildInputRunner) SubmitChildInputBatch(ctx context.Context, req agent.ChildInputRequest) (agent.ChildInputResult, error) {
	return r.SubmitChildInput(ctx, req)
}

type blockingBatchRunner struct {
	*runtimeChildInputRunner
	entered chan struct{}
	release chan struct{}
}

func (r *blockingBatchRunner) SubmitChildInputBatch(ctx context.Context, req agent.ChildInputRequest) (agent.ChildInputResult, error) {
	result, err := r.SubmitChildInput(ctx, req)
	close(r.entered)
	<-r.release
	return result, err
}

func TestBatchAdmissionPinsTargetWithoutHoldingTopologyLock(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, child, base := newIdleChildActivityTask(t)
		r.controllers = stubACPController{}
		active, err := r.sessions.Session(t.Context(), child.sessionRef)
		if err != nil {
			t.Fatal(err)
		}
		binding := active.Participants[0]
		runner := &blockingBatchRunner{runtimeChildInputRunner: base, entered: make(chan struct{}), release: make(chan struct{})}
		child.runner = runner
		sent := make(chan error, 1)
		go func() {
			sent <- r.SubmitAgentInputBatch(t.Context(), child.sessionRef, child.handle, []agent.AgentInputBatchEntry{{Message: agent.AgentCommunicationInput{Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "one"}}, {Message: agent.AgentCommunicationInput{Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "two"}}}, nil)
		}()
		<-runner.entered
		detached := make(chan error, 1)
		go func() {
			_, err := r.DetachParticipant(t.Context(), agent.DetachParticipantRequest{SessionRef: child.sessionRef, ParticipantID: binding.ID, RequireSettled: true, ExpectedDelegationID: binding.DelegationID, ExpectedAttachmentGeneration: binding.AttachmentGeneration})
			detached <- err
		}()
		synctest.Wait()
		if !r.participantMu.TryLock() {
			t.Fatal("receipt wait holds topology lock")
		}
		r.participantMu.Unlock()
		if err := <-detached; !errorcode.Is(err, errorcode.Conflict) {
			t.Fatalf("pending admission detach = %v", err)
		}
		close(runner.release)
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		if _, err := r.DetachParticipant(t.Context(), agent.DetachParticipantRequest{SessionRef: child.sessionRef, ParticipantID: binding.ID, RequireSettled: true}); err == nil {
			t.Fatal("detached unsettled producer after receipt")
		}
		finishChildActivity(t, base.request.Completion, "batch complete")
	})
}

func TestBatchAdmissionRejectsSingularOnlyRunnerWithoutDispatch(t *testing.T) {
	r, child, base := newLocalControllerChildActivityTask(t)
	child.runner = struct {
		agent.SubagentRunner
		agent.ChildInputRunner
	}{base, base}
	err := r.SubmitAgentInputBatch(t.Context(), child.sessionRef, child.handle, []agent.AgentInputBatchEntry{{Message: agent.AgentCommunicationInput{Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "keep full batch"}}}, nil)
	if !errorcode.Is(err, errorcode.Unsupported) {
		t.Fatalf("singular runner accepted batch: %v", err)
	}
	base.mu.Lock()
	defer base.mu.Unlock()
	if len(base.request.Messages) != 0 || base.request.Input != "" {
		t.Fatalf("dispatched through singular interface: %#v", base.request)
	}
}

func TestParentReceiptWaitAllowsStartThreadAndOtherRecipient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, child, runner := newIdleChildActivityTask(t)
		r.controllers = stubACPController{}
		active, err := r.sessions.Session(t.Context(), child.sessionRef)
		if err != nil {
			t.Fatal(err)
		}
		source := active.Participants[0]
		entered, release := make(chan struct{}), make(chan struct{})
		sent := make(chan error, 1)
		go func() {
			sent <- r.SubmitAgentInputBatch(t.Context(), child.sessionRef, agent.AgentInputParent, []agent.AgentInputBatchEntry{{Participant: &source, Message: agent.AgentCommunicationInput{Input: "child report"}}}, func(context.Context, session.Session, []agent.AgentCommunicationInput) error {
				close(entered)
				<-release
				return nil
			})
		}()
		<-entered
		// The parent ACP receipt waits for its currently executing StartThread tool.
		// Starting the next participant must complete without cancelling that receipt.
		started, err := r.tasks.StartSubagent(t.Context(), active, child.sessionRef, runner, taskapi.SubagentStartRequest{Agent: "helper", Prompt: "second child"})
		if err != nil {
			t.Fatal(err)
		}
		err = r.SubmitAgentInputBatch(t.Context(), child.sessionRef, started.Ref.TaskID, []agent.AgentInputBatchEntry{{Participant: &source, Message: agent.AgentCommunicationInput{Input: "sibling input"}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.DetachParticipant(t.Context(), agent.DetachParticipantRequest{SessionRef: child.sessionRef, ParticipantID: source.ID}); !errorcode.Is(err, errorcode.Conflict) {
			t.Fatalf("source detach = %v", err)
		}
		close(release)
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		finishChildActivity(t, runner.request.Completion, "second complete")
		if _, err := r.DetachParticipant(t.Context(), agent.DetachParticipantRequest{SessionRef: child.sessionRef, ParticipantID: source.ID, RequireSettled: true}); err != nil {
			t.Fatalf("source pin not released: %v", err)
		}
	})
}

func TestQueuedChildAdmissionCancellationReleasesOnlyItsPins(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r, child, base := newIdleChildActivityTask(t)
		runner := &blockingBatchRunner{runtimeChildInputRunner: base, entered: make(chan struct{}), release: make(chan struct{})}
		child.runner = runner
		entries := []agent.AgentInputBatchEntry{{Message: agent.AgentCommunicationInput{Source: session.ActorRef{Kind: session.ActorKindController, ID: "controller-1"}, Input: "input"}}}
		first := make(chan error, 1)
		go func() { first <- r.SubmitAgentInputBatch(t.Context(), child.sessionRef, child.handle, entries, nil) }()
		<-runner.entered
		ctx, cancel := context.WithCancel(t.Context())
		second := make(chan error, 1)
		go func() { second <- r.SubmitAgentInputBatch(ctx, child.sessionRef, child.handle, entries, nil) }()
		synctest.Wait()
		cancel()
		if err := <-second; !errors.Is(err, context.Canceled) {
			t.Fatalf("queued cancellation = %v", err)
		}
		r.participantMu.Lock()
		pins := r.participantInputPins[participantPromptKey{sessionID: child.sessionRef.SessionID, participantID: child.anchor.AgentID}]
		r.participantMu.Unlock()
		if pins != 1 {
			t.Fatalf("remaining pins = %d, want first admission only", pins)
		}
		close(runner.release)
		if err := <-first; err != nil {
			t.Fatal(err)
		}
		finishChildActivity(t, base.request.Completion, "complete")
	})
}
