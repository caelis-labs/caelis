package runtime

import (
	"context"
	"encoding/json"
	"errors"
	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
	"reflect"
	"strings"
	"testing"
	"testing/synctest"
)

func TestAgentInputBatchValidatesEverySenderBeforeSingleChildDispatch(t *testing.T) {
	t.Parallel()
	r, task, runner := newLocalControllerChildActivityTask(t)
	source := session.ParticipantBinding{ID: "sibling", Kind: session.ParticipantKindSubagent, Role: session.ParticipantRoleDelegated, SessionID: "sibling-session", DelegationID: "sibling-task", AttachmentGeneration: "generation-a", AgentName: "sibling"}
	if _, err := r.participantBindings.PutParticipant(t.Context(), session.PutParticipantRequest{SessionRef: task.sessionRef, MutationGuard: session.ControlMutationGuard(session.ControlMutationPurposeParticipant), Binding: source}); err != nil {
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
	t.Parallel()
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
	t.Parallel()
	r, child, base := newLocalControllerChildActivityTask(t)
	child.runner = struct {
		taskapi.SubagentRunner
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
	t.Parallel()
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
	t.Parallel()
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

func TestRuntimeRunPersistsOrderedAgentCommunicationBatchInOneTurn(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	sessions := newLogicalFileStore(t, sessionfile.Config{RootDir: root})
	active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "batch-user",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	orbit := session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-orbit", Role: "delegated", Name: "orbit"}
	zenith := session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-zenith", Role: "delegated", Name: "zenith"}
	probe := &durableIdentityModel{text: "ack both"}
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	result, err := runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef,
		InputKind:  agent.SubmissionKindAgentCommunication,
		Inputs: []agent.AgentCommunicationInput{
			{Source: orbit, Input: "status from orbit"},
			{Source: zenith, Input: "status from zenith"},
		},
		AgentSpec: agent.AgentSpec{Name: "chat", Model: probe},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if _, err := drainRunnerEvents(t, result.Handle); err != nil {
		t.Fatalf("drain error = %v", err)
	}

	wantUser := []model.Message{
		model.NewTextMessage(model.RoleUser, "status from orbit\n\nFrom: orbit"),
		model.NewTextMessage(model.RoleUser, "status from zenith\n\nFrom: zenith"),
	}
	if got := userMessages(probe.messages); !reflect.DeepEqual(got, wantUser) {
		t.Fatalf("runtime-produced model context = %#v, want %#v", got, wantUser)
	}

	reopened := newLogicalFileStore(t, sessionfile.Config{RootDir: root})
	loaded, err := reopened.LoadSession(context.Background(), session.LoadSessionRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatalf("LoadSession() error = %v", err)
	}
	persisted := contextEvents(loaded.Events)
	if len(persisted) != 2 {
		t.Fatalf("persisted Agent communication events = %d, want 2 in one Turn", len(persisted))
	}
	if persisted[0].Actor.ID != orbit.ID || persisted[1].Actor.ID != zenith.ID {
		t.Fatalf("durable sources = (%#v, %#v), want orbit then zenith", persisted[0].Actor, persisted[1].Actor)
	}
	if persisted[0].Scope == nil || persisted[1].Scope == nil || persisted[0].Scope.TurnID == "" ||
		persisted[0].Scope.TurnID != persisted[1].Scope.TurnID {
		t.Fatalf("batch events used different Turns: (%#v, %#v)", persisted[0].Scope, persisted[1].Scope)
	}
	if persisted[0].IdempotencyKey == persisted[1].IdempotencyKey {
		t.Fatalf("batch events reused idempotency key %q", persisted[0].IdempotencyKey)
	}

	raw, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []session.Event
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	replayed := make([]*session.Event, 0, len(decoded))
	for i := range decoded {
		migrated, err := session.MigrateEvent(decoded[i])
		if err != nil {
			t.Fatal(err)
		}
		if err := session.ValidateDurableCoreEvent(&migrated); err != nil {
			t.Fatal(err)
		}
		replayed = append(replayed, &migrated)
	}
	got := make([]model.Message, 0, len(replayed))
	for _, event := range replayed {
		message, ok := session.ModelMessageOf(event)
		if !ok {
			t.Fatalf("replayed event has no model message: %#v", event)
		}
		got = append(got, message)
	}
	if !reflect.DeepEqual(got, wantUser) {
		t.Fatalf("rebuilt model context = %#v, want runtime-produced %#v", got, wantUser)
	}
}

func userMessages(messages []model.Message) []model.Message {
	out := make([]model.Message, 0, len(messages))
	for _, message := range messages {
		if message.Role == model.RoleUser {
			out = append(out, message)
		}
	}
	return out
}

func contextEvents(events []*session.Event) []*session.Event {
	out := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if event != nil && session.EventTypeOf(event) == session.EventTypeContext {
			out = append(out, event)
		}
	}
	return out
}

func TestRuntimeRunRejectsMixedSingularAndBatchAgentCommunication(t *testing.T) {
	t.Parallel()

	sessions := newLogicalFileStore(t, sessionfile.Config{RootDir: t.TempDir()})
	active, err := sessions.StartSession(context.Background(), session.StartSessionRequest{
		AppName: "caelis", UserID: "mixed-user",
	})
	if err != nil {
		t.Fatalf("StartSession() error = %v", err)
	}
	runtime, err := New(Config{Sessions: sessions, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	actor := session.ActorRef{Kind: session.ActorKindParticipant, ID: "child-1", Name: "orbit"}
	_, err = runtime.Run(context.Background(), agent.RunRequest{
		SessionRef: active.SessionRef,
		InputKind:  agent.SubmissionKindAgentCommunication,
		Input:      "singular",
		InputActor: actor,
		Inputs:     []agent.AgentCommunicationInput{{Source: actor, Input: "batch"}},
		AgentSpec:  agent.AgentSpec{Name: "chat", Model: staticModel{text: "no"}},
	})
	if err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Fatalf("Run(mixed) error = %v, want combined-field rejection", err)
	}
}

func TestRuntimeRejectsPartiallyEmptyInputBatchBeforeStarting(t *testing.T) {
	t.Parallel()
	store, active := newTestSessionService(t, "empty-batch-member")
	rt, err := New(Config{Sessions: store, AgentFactory: chat.Factory{}})
	if err != nil {
		t.Fatal(err)
	}
	source := session.ActorRef{Kind: session.ActorKindParticipant, ID: "reviewer"}
	result, err := rt.Run(t.Context(), agent.RunRequest{
		SessionRef: active.SessionRef, InputKind: agent.SubmissionKindAgentCommunication,
		Inputs:    []agent.AgentCommunicationInput{{Source: source, Input: "valid"}, {Source: source, Input: "  "}},
		AgentSpec: agent.AgentSpec{Name: "chat", Model: staticModel{text: "unexpected"}},
	})
	if !errorcode.Is(err, errorcode.InvalidArgument) || result.Handle != nil {
		t.Fatalf("partially empty batch admitted: handle=%v, err=%v", result.Handle, err)
	}
}
