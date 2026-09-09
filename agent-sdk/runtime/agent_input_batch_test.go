package runtime

import (
	"context"
	"testing"
	"testing/synctest"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
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

func TestBatchAdmissionSerializesWithDetachUntilProducerOwnsInput(t *testing.T) {
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
		if r.participantMu.TryLock() {
			r.participantMu.Unlock()
			t.Fatal("batch dispatch released participant mutation lock")
		}
		select {
		case err := <-detached:
			t.Fatalf("detach escaped admission fence: %v", err)
		default:
		}
		close(runner.release)
		if err := <-sent; err != nil {
			t.Fatal(err)
		}
		if err := <-detached; err == nil {
			t.Fatal("detached unsettled batch producer")
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
