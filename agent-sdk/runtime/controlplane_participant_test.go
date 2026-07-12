package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestParticipantLifecycleEventUsesNormalizedACPParticipantSemantics(t *testing.T) {
	t.Parallel()

	event := participantLifecycleEvent(
		session.Session{Controller: session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "kernel-1", EpochID: "epoch-1"}},
		session.ParticipantBinding{ID: "participant-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar, SessionID: "remote-1"},
		"attached",
		time.Unix(1, 0),
	)
	participant := session.ProtocolParticipantOf(event)
	if participant == nil || participant.Action != "attached" {
		t.Fatalf("participant protocol = %#v, want normalized attached lifecycle", participant)
	}
	if event.Protocol.Method != session.ProtocolMethodParticipantUpdate {
		t.Fatalf("participant method = %q, want %q", event.Protocol.Method, session.ProtocolMethodParticipantUpdate)
	}
}

func TestParticipantLifecycleIdempotencyIgnoresUnrelatedSessionRevision(t *testing.T) {
	t.Parallel()
	binding := session.ParticipantBinding{
		ID: "participant-1", DelegationID: "delegation-1", AttachmentGeneration: "generation-1",
	}
	before := session.Session{SessionRef: session.SessionRef{SessionID: "session-1"}, Revision: 3}
	after := session.CloneSession(before)
	after.Revision = 99
	if first, second := participantLifecycleIdempotencyKey(before, binding, "attached"), participantLifecycleIdempotencyKey(after, binding, "attached"); first != second {
		t.Fatalf("same participant effect received revision-scoped keys %q and %q", first, second)
	}
}

func TestRuntimeParticipantLifecycleMayOverlapActiveTurnLease(t *testing.T) {
	t.Parallel()

	sessions, active := newTestSessionService(t, "participant-control-overlap")
	var err error
	active, err = sessions.BindController(context.Background(), session.BindControllerRequest{
		SessionRef: active.SessionRef,
		Binding: session.ControllerBinding{
			Kind: session.ControllerKindKernel, ControllerID: "sdk-kernel", EpochID: "kernel-epoch",
		},
	})
	if err != nil {
		t.Fatalf("BindController() error = %v", err)
	}
	lease, err := sessions.(session.SessionLeaseService).AcquireSessionLease(context.Background(), session.AcquireSessionLeaseRequest{
		SessionRef: active.SessionRef,
		OwnerID:    "main-turn-owner",
		TTL:        time.Minute,
	})
	if err != nil {
		t.Fatalf("AcquireSessionLease() error = %v", err)
	}
	binding := session.ParticipantBinding{
		ID: "claude-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar,
		AgentName: "claude", Label: "@claude", SessionID: "remote-claude",
		AttachmentGeneration: "generation-1",
	}
	backend := stubACPController{
		attach: func(context.Context, controller.AttachRequest) (session.ParticipantBinding, error) {
			return binding, nil
		},
		detach: func(_ context.Context, req controller.DetachRequest) error {
			if req.ParticipantID != binding.ID || req.AttachmentGeneration != binding.AttachmentGeneration {
				t.Fatalf("Detach request = %#v, want exact live participant identity", req)
			}
			return nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, Controllers: backend,
	}))
	if err != nil {
		t.Fatal(err)
	}
	attached, err := runtime.AttachParticipant(context.Background(), agent.AttachParticipantRequest{
		SessionRef: active.SessionRef, Agent: "claude", Role: session.ParticipantRoleSidecar,
	})
	if err != nil {
		t.Fatalf("AttachParticipant() during active Turn error = %v", err)
	}
	if durable, ok := participantBinding(attached, binding.ID); !ok || durable.AttachmentGeneration != binding.AttachmentGeneration {
		t.Fatalf("attached participant = %#v, want exact durable generation", durable)
	}
	detached, err := runtime.DetachParticipant(context.Background(), agent.DetachParticipantRequest{
		SessionRef: active.SessionRef, ParticipantID: binding.ID,
	})
	if err != nil {
		t.Fatalf("DetachParticipant() during active Turn error = %v", err)
	}
	if _, ok := participantBinding(detached, binding.ID); ok {
		t.Fatalf("detached Session still contains participant: %#v", detached.Participants)
	}
	durableLease, err := sessions.(session.SessionLeaseReader).SessionLease(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatalf("SessionLease() error = %v", err)
	}
	if durableLease.LeaseID != lease.LeaseID || durableLease.FencingToken != lease.FencingToken {
		t.Fatalf("participant lifecycle changed active Turn lease: got %#v want %#v", durableLease, lease)
	}
}

func TestRuntimeDetachParticipantRollbackPreservesActiveSessionOnRemoveFailure(t *testing.T) {
	t.Parallel()

	baseSessions, activeSession := newTestSessionService(t, "sess-acp-detach-rollback")
	activeSession, err := baseSessions.PutParticipant(context.Background(), session.PutParticipantRequest{
		SessionRef: activeSession.SessionRef,
		Binding: session.ParticipantBinding{
			ID:        "emma",
			Kind:      session.ParticipantKindACP,
			Role:      session.ParticipantRoleSidecar,
			Label:     "@emma",
			AgentName: "claude",
			SessionID: "remote-emma",
			Source:    "test",
		},
	})
	if err != nil {
		t.Fatalf("PutParticipant() error = %v", err)
	}
	removeErr := errors.New("forced remove participant failure")
	sessions := removeParticipantWithEventFailingService{Service: baseSessions, err: removeErr}
	detachReqCh := make(chan controller.DetachRequest, 1)
	attachReqCh := make(chan controller.AttachRequest, 1)
	testController := stubACPController{
		detach: func(ctx context.Context, req controller.DetachRequest) error {
			_ = ctx
			detachReqCh <- req
			return nil
		},
		attach: func(ctx context.Context, req controller.AttachRequest) (session.ParticipantBinding, error) {
			_ = ctx
			attachReqCh <- req
			if strings.TrimSpace(req.Session.SessionID) == "" {
				return session.ParticipantBinding{}, errors.New("session id is required")
			}
			return session.CloneParticipantBinding(req.Binding), nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions:     sessions,
		AgentFactory: chat.Factory{SystemPrompt: "Be terse."},
		Controllers:  testController,
	}))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, err = runtime.DetachParticipant(context.Background(), agent.DetachParticipantRequest{
		SessionRef:    activeSession.SessionRef,
		ParticipantID: "emma",
		Source:        "test_detach",
	})
	if !errors.Is(err, removeErr) {
		t.Fatalf("DetachParticipant() error = %v, want %v", err, removeErr)
	}
	select {
	case req := <-detachReqCh:
		if req.Session.SessionID != activeSession.SessionID {
			t.Fatalf("detach SessionID = %q, want %q", req.Session.SessionID, activeSession.SessionID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller detach was not called")
	}
	select {
	case req := <-attachReqCh:
		if req.Session.SessionID != activeSession.SessionID {
			t.Fatalf("rollback attach SessionID = %q, want %q", req.Session.SessionID, activeSession.SessionID)
		}
		if len(req.Session.Participants) != 1 || req.Session.Participants[0].ID != "emma" {
			t.Fatalf("rollback attach session participants = %#v, want persisted emma binding", req.Session.Participants)
		}
		if req.Binding.ID != "emma" || req.Binding.SessionID != "remote-emma" {
			t.Fatalf("rollback attach binding = %#v, want emma remote binding", req.Binding)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rollback attach was not called")
	}
	loaded, err := baseSessions.Session(context.Background(), activeSession.SessionRef)
	if err != nil {
		t.Fatalf("Session() error = %v", err)
	}
	if _, ok := participantBinding(loaded, "emma"); !ok {
		t.Fatal("persisted participant was removed despite failed lifecycle removal")
	}
}

func TestRuntimeDetachParticipantRollbackPublishesRotatedGeneration(t *testing.T) {
	t.Parallel()

	baseSessions, active := newTestSessionService(t, "participant-detach-rotate")
	previous := session.ParticipantBinding{
		ID: "emma", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar,
		Label: "@emma", AgentName: "claude", SessionID: "remote-old", Source: "test",
		DelegationID: "delegation-emma", AttachmentGeneration: "generation-old",
	}
	active, err := baseSessions.PutParticipant(context.Background(), session.PutParticipantRequest{SessionRef: active.SessionRef, Binding: previous})
	if err != nil {
		t.Fatal(err)
	}
	removeErr := errors.New("forced remove failure")
	sessions := removeParticipantWithEventFailingService{Service: baseSessions, err: removeErr}
	var rollback session.ParticipantBinding
	backend := stubACPController{
		detach: func(context.Context, controller.DetachRequest) error { return nil },
		attach: func(_ context.Context, req controller.AttachRequest) (session.ParticipantBinding, error) {
			rollback = session.CloneParticipantBinding(req.Binding)
			rollback.SessionID = "remote-new"
			rollback.AttachmentGeneration = "generation-new"
			return rollback, nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Controllers: backend}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.DetachParticipant(context.Background(), agent.DetachParticipantRequest{
		SessionRef: active.SessionRef, ParticipantID: previous.ID, Source: "test",
	})
	if !errors.Is(err, removeErr) {
		t.Fatalf("DetachParticipant() error = %v, want original remove failure", err)
	}
	loaded, err := baseSessions.Session(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	durable, ok := participantBinding(loaded, previous.ID)
	if !ok || durable.AttachmentGeneration != rollback.AttachmentGeneration || durable.SessionID != rollback.SessionID {
		t.Fatalf("durable rollback binding = %#v, live rollback = %#v", durable, rollback)
	}
}

func TestEnsureACPParticipantRunDetachesNewGenerationWhenPublishFails(t *testing.T) {
	t.Parallel()
	baseSessions, active := newTestSessionService(t, "participant-rehydrate-publish-fail")
	previous := session.ParticipantBinding{
		ID: "helper", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar,
		AgentName: "helper", SessionID: "remote-old", DelegationID: "delegation-helper", AttachmentGeneration: "generation-old",
	}
	active, err := baseSessions.PutParticipant(context.Background(), session.PutParticipantRequest{SessionRef: active.SessionRef, Binding: previous})
	if err != nil {
		t.Fatal(err)
	}
	publishErr := errors.New("forced participant publish failure")
	sessions := putParticipantFailingService{Service: baseSessions, err: publishErr}
	attached := previous
	attached.SessionID = "remote-new"
	attached.AttachmentGeneration = "generation-new"
	var detached controller.DetachRequest
	backend := stubACPController{
		attach: func(context.Context, controller.AttachRequest) (session.ParticipantBinding, error) {
			return attached, nil
		},
		detach: func(_ context.Context, req controller.DetachRequest) error { detached = req; return nil },
	}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Controllers: backend}))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = runtime.ensureACPParticipantRun(context.Background(), active, active.SessionRef, previous)
	if !errors.Is(err, publishErr) {
		t.Fatalf("ensureACPParticipantRun() error = %v, want publish failure", err)
	}
	if detached.ParticipantID != attached.ID || detached.DelegationID != attached.DelegationID || detached.AttachmentGeneration != attached.AttachmentGeneration {
		t.Fatalf("compensating detach = %#v, want exact new endpoint %#v", detached, attached)
	}
	loaded, err := baseSessions.Session(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	durable, _ := participantBinding(loaded, previous.ID)
	if durable.AttachmentGeneration != previous.AttachmentGeneration {
		t.Fatalf("durable binding changed on failed publish: %#v", durable)
	}
}

func TestRuntimeAttachParticipantDoesNotCompensateCommittedLifecycleWrite(t *testing.T) {
	t.Parallel()
	baseSessions, activeSession := newTestSessionService(t, "participant-committed-runtime")
	sessions := committedParticipantLifecycleService{Service: baseSessions}
	detachCalls := 0
	backend := stubACPController{
		attach: func(context.Context, controller.AttachRequest) (session.ParticipantBinding, error) {
			return session.ParticipantBinding{
				ID: "participant-a", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar,
				DelegationID: "delegation-a", AttachmentGeneration: "generation-a",
			}, nil
		},
		detach: func(context.Context, controller.DetachRequest) error {
			detachCalls++
			return nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Controllers: backend}))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := runtime.AttachParticipant(context.Background(), agent.AttachParticipantRequest{
		SessionRef: activeSession.SessionRef, Agent: "helper",
	})
	if err != nil {
		t.Fatalf("AttachParticipant() error = %v", err)
	}
	if detachCalls != 0 || len(updated.Participants) != 1 || updated.Participants[0].AttachmentGeneration != "generation-a" {
		t.Fatalf("committed attach = %#v detachCalls=%d", updated, detachCalls)
	}
}

func TestRuntimeAttachParticipantRejectsUnconfirmedCommittedResult(t *testing.T) {
	t.Parallel()
	baseSessions, activeSession := newTestSessionService(t, "participant-false-committed-runtime")
	sessions := falseCommittedParticipantLifecycleService{Service: baseSessions}
	detachCalls := 0
	backend := stubACPController{
		attach: func(context.Context, controller.AttachRequest) (session.ParticipantBinding, error) {
			return session.ParticipantBinding{
				ID: "participant-a", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar,
				DelegationID: "delegation-a", AttachmentGeneration: "generation-a",
			}, nil
		},
		detach: func(context.Context, controller.DetachRequest) error {
			detachCalls++
			return nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Controllers: backend}))
	if err != nil {
		t.Fatal(err)
	}
	updated, err := runtime.AttachParticipant(context.Background(), agent.AttachParticipantRequest{
		SessionRef: activeSession.SessionRef, Agent: "helper",
	})
	if !session.IsCommitted(err) || strings.TrimSpace(updated.SessionID) != "" {
		t.Fatalf("AttachParticipant() = %#v, %v; want explicit unconfirmed committed outcome", updated, err)
	}
	loaded, loadErr := baseSessions.Session(context.Background(), activeSession.SessionRef)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if len(loaded.Participants) != 0 || detachCalls != 1 {
		t.Fatalf("false committed state = participants %#v detach calls %d; want exact live compensation", loaded.Participants, detachCalls)
	}
}

func TestRuntimeDetachParticipantRestoresLiveEndpointWhenCommittedResultIsFalse(t *testing.T) {
	t.Parallel()
	baseSessions, active := newTestSessionService(t, "participant-false-detach-committed")
	previous := session.ParticipantBinding{
		ID: "participant-a", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar,
		AgentName: "helper", SessionID: "remote-old", DelegationID: "delegation-a", AttachmentGeneration: "generation-old",
	}
	active, err := baseSessions.PutParticipant(context.Background(), session.PutParticipantRequest{SessionRef: active.SessionRef, Binding: previous})
	if err != nil {
		t.Fatal(err)
	}
	sessions := falseCommittedRemoveParticipantLifecycleService{Service: baseSessions}
	reattached := previous
	reattached.SessionID = "remote-new"
	reattached.AttachmentGeneration = "generation-new"
	attachCalls := 0
	backend := stubACPController{
		detach: func(context.Context, controller.DetachRequest) error { return nil },
		attach: func(context.Context, controller.AttachRequest) (session.ParticipantBinding, error) {
			attachCalls++
			return reattached, nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Controllers: backend}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.DetachParticipant(context.Background(), agent.DetachParticipantRequest{
		SessionRef: active.SessionRef, ParticipantID: previous.ID,
	})
	if !session.IsCommitted(err) || attachCalls != 1 {
		t.Fatalf("DetachParticipant() error/attach calls = %v/%d, want false committed detection and one rollback", err, attachCalls)
	}
	loaded, err := baseSessions.Session(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatal(err)
	}
	durable, ok := participantBinding(loaded, previous.ID)
	if !ok || durable.AttachmentGeneration != reattached.AttachmentGeneration || durable.SessionID != reattached.SessionID {
		t.Fatalf("durable/live rollback binding = %#v/%#v", durable, reattached)
	}
}

func TestParticipantPromptUserEventUsesDisplayInputForProjection(t *testing.T) {
	t.Parallel()

	modelInput := "Load skill `cmpctl` before taking task actions, then follow its instructions.\n\nUser request:\narchive preflight"
	displayInput := "$cmpctl archive preflight"
	event := participantPromptUserEvent(
		session.Session{Controller: session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "kernel-1"}},
		session.ParticipantBinding{ID: "p-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar, AgentName: "helper"},
		"turn-1",
		"test",
		modelInput,
		displayInput,
		"",
		nil,
		time.Unix(1, 0),
	)
	if event == nil {
		t.Fatal("participantPromptUserEvent() = nil")
	}
	if event.Message == nil || event.Message.TextContent() != modelInput {
		t.Fatalf("event.Message = %#v, want model-visible input", event.Message)
	}
	if event.Text != displayInput {
		t.Fatalf("event.Text = %q, want display input %q", event.Text, displayInput)
	}
	if got := event.Meta["display_input"]; got != displayInput {
		t.Fatalf("event.Meta[display_input] = %#v, want %q", got, displayInput)
	}
	update := session.ProtocolUpdateOf(event)
	content, _ := update.Content.(map[string]any)
	if content["text"] != displayInput {
		t.Fatalf("protocol content = %#v, want display input", update.Content)
	}
}

func TestRuntimeParticipantPromptSingleFlightRejectsBeforeUserEventPersistence(t *testing.T) {
	t.Parallel()

	sessions, active := newTestSessionService(t, "participant-runtime-single-flight")
	binding := session.ParticipantBinding{
		ID: "helper", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar,
		AgentName: "helper", Label: "@helper", SessionID: "remote-helper",
		DelegationID: "delegation-helper", AttachmentGeneration: "generation-helper",
	}
	active, err := sessions.PutParticipant(context.Background(), session.PutParticipantRequest{
		SessionRef: active.SessionRef, Binding: binding,
	})
	if err != nil {
		t.Fatal(err)
	}
	firstHandle := newTestControllerTurnHandle(nil)
	firstPromptStarted := make(chan struct{})
	thirdPromptStarted := make(chan struct{})
	promptCalls := 0
	backend := stubACPController{
		promptParticipant: func(context.Context, controller.ParticipantPromptRequest) (controller.TurnResult, error) {
			promptCalls++
			if promptCalls == 1 {
				close(firstPromptStarted)
				return controller.TurnResult{Handle: firstHandle}, nil
			}
			handle := newTestControllerTurnHandle(nil)
			handle.finish()
			close(thirdPromptStarted)
			return controller.TurnResult{Handle: handle}, nil
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{Sessions: sessions, AgentFactory: chat.Factory{}, Controllers: backend}))
	if err != nil {
		t.Fatal(err)
	}

	first, err := runtime.PromptParticipant(context.Background(), agent.PromptParticipantRequest{
		SessionRef: active.SessionRef, ParticipantID: binding.ID, Input: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstPromptStarted:
	case <-time.After(time.Second):
		t.Fatal("first participant prompt did not reach the backend")
	}
	before, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.PromptParticipant(context.Background(), agent.PromptParticipantRequest{
		SessionRef: active.SessionRef, ParticipantID: binding.ID, Input: "must-not-persist",
	}); err == nil || !strings.Contains(err.Error(), "prompt in progress") {
		t.Fatalf("overlapping PromptParticipant() error = %v, want synchronous single-flight rejection", err)
	}
	after, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("overlapping rejected prompt appended durable history: before=%d after=%d", len(before), len(after))
	}

	firstHandle.finish()
	for range first.Handle.Events() {
	}
	third, err := runtime.PromptParticipant(context.Background(), agent.PromptParticipantRequest{
		SessionRef: active.SessionRef, ParticipantID: binding.ID, Input: "third",
	})
	if err != nil {
		t.Fatalf("PromptParticipant() after completion error = %v", err)
	}
	select {
	case <-thirdPromptStarted:
	case <-time.After(time.Second):
		t.Fatal("completed participant prompt did not release the Runtime claim")
	}
	for range third.Handle.Events() {
	}
}

type removeParticipantWithEventFailingService struct {
	session.Service
	err error
}

type putParticipantFailingService struct {
	session.Service
	err error
}

type committedParticipantLifecycleService struct{ session.Service }

type falseCommittedParticipantLifecycleService struct{ session.Service }

type falseCommittedRemoveParticipantLifecycleService struct{ session.Service }

func (s putParticipantFailingService) PutParticipant(context.Context, session.PutParticipantRequest) (session.Session, error) {
	return session.Session{}, s.err
}

func (s committedParticipantLifecycleService) PutParticipantWithEvent(ctx context.Context, req session.PutParticipantWithEventRequest) (session.Session, *session.Event, error) {
	lifecycle := s.Service.(session.ParticipantLifecycleService)
	updated, event, err := lifecycle.PutParticipantWithEvent(ctx, req)
	if err != nil {
		return updated, event, err
	}
	return updated, event, &session.CommittedError{Err: errors.New("forced committed report failure")}
}

func (s committedParticipantLifecycleService) RemoveParticipantWithEvent(ctx context.Context, req session.RemoveParticipantWithEventRequest) (session.Session, *session.Event, error) {
	lifecycle := s.Service.(session.ParticipantLifecycleService)
	return lifecycle.RemoveParticipantWithEvent(ctx, req)
}

func (s falseCommittedParticipantLifecycleService) PutParticipantWithEvent(ctx context.Context, req session.PutParticipantWithEventRequest) (session.Session, *session.Event, error) {
	current, loadErr := s.Session(ctx, req.SessionRef)
	if loadErr != nil {
		return session.Session{}, nil, loadErr
	}
	fake := &session.Event{
		ID: "fake-old-event", SessionID: current.SessionID, Seq: 1, Schema: session.EventSchemaVersion,
		Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
	}
	return current, fake, &session.CommittedError{Err: errors.New("false committed report")}
}

func (s falseCommittedParticipantLifecycleService) RemoveParticipantWithEvent(ctx context.Context, req session.RemoveParticipantWithEventRequest) (session.Session, *session.Event, error) {
	return s.Service.(session.ParticipantLifecycleService).RemoveParticipantWithEvent(ctx, req)
}

func (s falseCommittedRemoveParticipantLifecycleService) PutParticipantWithEvent(ctx context.Context, req session.PutParticipantWithEventRequest) (session.Session, *session.Event, error) {
	return s.Service.(session.ParticipantLifecycleService).PutParticipantWithEvent(ctx, req)
}

func (s falseCommittedRemoveParticipantLifecycleService) RemoveParticipantWithEvent(ctx context.Context, req session.RemoveParticipantWithEventRequest) (session.Session, *session.Event, error) {
	current, loadErr := s.Session(ctx, req.SessionRef)
	if loadErr != nil {
		return session.Session{}, nil, loadErr
	}
	fake := &session.Event{
		ID: "fake-old-event", SessionID: current.SessionID, Seq: 1, Schema: session.EventSchemaVersion,
		Type: session.EventTypeAssistant, Visibility: session.VisibilityCanonical,
	}
	return current, fake, &session.CommittedError{Err: errors.New("false committed detach report")}
}

func (s removeParticipantWithEventFailingService) PutParticipantWithEvent(
	ctx context.Context,
	req session.PutParticipantWithEventRequest,
) (session.Session, *session.Event, error) {
	lifecycle, ok := s.Service.(session.ParticipantLifecycleService)
	if !ok {
		return session.Session{}, nil, errors.New("wrapped service does not support participant lifecycle")
	}
	return lifecycle.PutParticipantWithEvent(ctx, req)
}

func (s removeParticipantWithEventFailingService) RemoveParticipantWithEvent(
	context.Context,
	session.RemoveParticipantWithEventRequest,
) (session.Session, *session.Event, error) {
	return session.Session{}, nil, s.err
}
