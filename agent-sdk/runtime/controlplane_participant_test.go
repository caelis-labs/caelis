package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/placement"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/chat"
	"github.com/caelis-labs/caelis/agent-sdk/runtime/controller"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func TestParticipantLifecycleEventUsesNormalizedACPParticipantSemantics(t *testing.T) {
	t.Parallel()

	event := participantLifecycleEvent(
		session.Session{Controller: session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "kernel-1", EpochID: "epoch-1"}},
		session.ParticipantBinding{
			ID: "participant-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar,
			SessionID: "remote-1", AgentName: "codex", Label: "@lina",
			Placement: placement.Placement{ProfileID: "acp:codex:gpt", ReasoningEffort: "high"},
		},
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
	if event.Meta["agent"] != "codex" || event.Meta["handle"] != "lina" {
		t.Fatalf("participant display meta = %#v, want typed Agent and human handle", event.Meta)
	}
	if event.Meta["profile_id"] != "acp:codex:gpt" || event.Meta["reasoning_effort"] != "high" {
		t.Fatalf("participant placement audit meta = %#v, want profile and effort", event.Meta)
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

func TestRuntimeParticipantLifecycleMayOverlapActiveTurnFence(t *testing.T) {
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
	fence, err := sessions.(session.SessionFenceService).AcquireSessionFence(context.Background(), session.AcquireSessionFenceRequest{
		SessionRef: active.SessionRef,
		OwnerID:    "main-turn-owner",
	})
	if err != nil {
		t.Fatalf("AcquireSessionFence() error = %v", err)
	}
	binding := session.ParticipantBinding{
		ID: "claude-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar,
		AgentName: "claude", Label: "@claude", SessionID: "remote-claude",
		AttachmentGeneration: "generation-1",
	}
	frozen, err := placement.Seal(placement.Placement{
		Kind: placement.KindAgent, ProfileID: "acp:claude:model", Agent: "claude", Model: "opus",
		ReasoningEffort: "xhigh", SessionConfigValues: map[string]string{"thought_level": "very-high"},
		ConfigFingerprint: "sha256:config",
	})
	if err != nil {
		t.Fatal(err)
	}
	backend := stubACPController{
		attach: func(_ context.Context, req controller.AttachRequest) (session.ParticipantBinding, error) {
			if req.Placement.Fingerprint != frozen.Fingerprint {
				t.Fatalf("Attach placement = %#v, want %#v", req.Placement, frozen)
			}
			attached := binding
			attached.Placement = req.Placement
			return attached, nil
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
		SessionRef: active.SessionRef, Agent: "claude", Role: session.ParticipantRoleSidecar, Placement: frozen,
	})
	if err != nil {
		t.Fatalf("AttachParticipant() during active Turn error = %v", err)
	}
	if durable, ok := participantBinding(attached, binding.ID); !ok || durable.AttachmentGeneration != binding.AttachmentGeneration || durable.Placement.Fingerprint != frozen.Fingerprint {
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
	durableFence, err := sessions.(session.SessionFenceReader).SessionFence(context.Background(), active.SessionRef)
	if err != nil {
		t.Fatalf("SessionFence() error = %v", err)
	}
	if durableFence.FenceID != fence.FenceID || durableFence.FencingToken != fence.FencingToken {
		t.Fatalf("participant lifecycle changed active Turn fence: got %#v want %#v", durableFence, fence)
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
	displayAddress := "/zenith"
	event := participantPromptUserEvent(
		session.Session{Controller: session.ControllerBinding{Kind: session.ControllerKindKernel, ControllerID: "kernel-1"}},
		session.ParticipantBinding{ID: "p-1", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar, AgentName: "helper"},
		"turn-1",
		"test",
		modelInput,
		displayInput,
		displayAddress,
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
	if got := event.Meta["display_address"]; got != displayAddress {
		t.Fatalf("event.Meta[display_address] = %#v, want %q", got, displayAddress)
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

func TestRuntimeParticipantSteeringWaitsForAdmissionAndCommitsFIFOInputs(t *testing.T) {
	t.Parallel()

	sessions, active := newTestSessionService(t, "participant-runtime-steering")
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
	controllerHandle := newTestControllerTurnHandle(nil)
	promptStarted := make(chan struct{})
	releasePrompt := make(chan struct{})
	firstSteerStarted := make(chan struct{})
	releaseFirstSteer := make(chan struct{})
	var steerRequests []controller.ParticipantSteerRequest
	backend := steeringACPController{
		stubACPController: stubACPController{
			promptParticipant: func(context.Context, controller.ParticipantPromptRequest) (controller.TurnResult, error) {
				close(promptStarted)
				<-releasePrompt
				return controller.TurnResult{Handle: controllerHandle}, nil
			},
		},
		steerParticipant: func(_ context.Context, req controller.ParticipantSteerRequest) error {
			steerRequests = append(steerRequests, req)
			if len(steerRequests) == 1 {
				close(firstSteerStarted)
				<-releaseFirstSteer
			}
			return req.Commit()
		},
	}
	runtime, err := New(testConfigWithACPForwarder(Config{
		Sessions: sessions, AgentFactory: chat.Factory{}, Controllers: backend,
	}))
	if err != nil {
		t.Fatal(err)
	}
	result, err := runtime.PromptParticipant(context.Background(), agent.PromptParticipantRequest{
		SessionRef: active.SessionRef, ParticipantID: binding.ID, Input: "initial",
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-promptStarted:
	case <-time.After(time.Second):
		t.Fatal("participant prompt did not reach controller admission")
	}
	contextual, ok := result.Handle.(agent.ContextSubmissionRunner)
	if !ok {
		t.Fatalf("participant runner = %T, want ContextSubmissionRunner", result.Handle)
	}
	firstResult := make(chan error, 1)
	go func() {
		firstResult <- contextual.SubmitContext(context.Background(), agent.Submission{
			Kind: agent.SubmissionKindConversation, Text: "guide one", DisplayInput: "display one",
		})
	}()
	select {
	case <-firstSteerStarted:
		t.Fatal("steering reached controller before initial prompt admission")
	case <-time.After(25 * time.Millisecond):
	}
	close(releasePrompt)
	select {
	case <-firstSteerStarted:
	case <-time.After(time.Second):
		t.Fatal("first steering input did not reach controller")
	}
	secondResult := make(chan error, 1)
	go func() {
		secondResult <- contextual.SubmitContext(context.Background(), agent.Submission{
			Kind: agent.SubmissionKindConversation, Text: "guide two", DisplayInput: "display two",
		})
	}()
	select {
	case err := <-secondResult:
		t.Fatalf("second steering completed before first: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseFirstSteer)
	if err := <-firstResult; err != nil {
		t.Fatalf("first steering result = %v", err)
	}
	if err := <-secondResult; err != nil {
		t.Fatalf("second steering result = %v", err)
	}
	if len(steerRequests) != 2 || steerRequests[0].Input != "guide one" || steerRequests[1].Input != "guide two" {
		t.Fatalf("steering requests = %#v, want FIFO inputs", steerRequests)
	}
	if steerRequests[0].SessionRef.SessionID != active.SessionID || steerRequests[0].ParticipantID != binding.ID || steerRequests[0].TurnID == "" {
		t.Fatalf("steering target = %#v, want exact active participant Turn", steerRequests[0])
	}
	if steerRequests[1].TurnID != steerRequests[0].TurnID {
		t.Fatalf("steering Turn IDs = %q/%q, want same Turn", steerRequests[0].TurnID, steerRequests[1].TurnID)
	}

	controllerHandle.finish()
	var published []*session.Event
	for event, eventErr := range result.Handle.Events() {
		if eventErr != nil {
			t.Fatalf("participant runner event error = %v", eventErr)
		}
		published = append(published, event)
	}
	var publishedInputs []string
	for _, event := range published {
		if event != nil && event.Type == session.EventTypeUser && event.Scope != nil && event.Scope.TurnID == steerRequests[0].TurnID && event.Message != nil {
			publishedInputs = append(publishedInputs, event.Message.TextContent())
		}
	}
	if len(publishedInputs) != 3 || publishedInputs[0] != "initial" || publishedInputs[1] != "guide one" || publishedInputs[2] != "guide two" {
		t.Fatalf("published participant inputs = %#v, want initial and FIFO steering inputs", publishedInputs)
	}
	durable, err := sessions.Events(context.Background(), session.EventsRequest{SessionRef: active.SessionRef})
	if err != nil {
		t.Fatal(err)
	}
	var durableInputs []string
	for _, event := range durable {
		if event != nil && event.Type == session.EventTypeUser && event.Scope != nil && event.Scope.TurnID == steerRequests[0].TurnID && event.Message != nil {
			durableInputs = append(durableInputs, event.Message.TextContent())
		}
	}
	if len(durableInputs) != 3 || durableInputs[0] != "initial" || durableInputs[1] != "guide one" || durableInputs[2] != "guide two" {
		t.Fatalf("durable participant inputs = %#v, want initial and FIFO steering inputs", durableInputs)
	}
}

func TestRuntimeParticipantSteeringAdmissionFailureIsNoEffect(t *testing.T) {
	t.Parallel()

	binding := session.ParticipantBinding{
		ID: "helper", Kind: session.ParticipantKindACP, Role: session.ParticipantRoleSidecar,
		AgentName: "helper", Label: "@helper", SessionID: "remote-helper",
	}
	promptErr := errors.New("initial participant prompt failed")
	steerCalled := false
	backend := steeringACPController{
		steerParticipant: func(context.Context, controller.ParticipantSteerRequest) error {
			steerCalled = true
			return nil
		},
	}
	admission := newSteeringAdmission()
	admission.resolve(promptErr)
	submissionCtx, cancelSubmission := context.WithCancel(context.Background())
	cancelSubmission()
	runtime := &Runtime{controllers: backend}
	handler := runtime.participantSteeringHandler(
		context.Background(),
		session.Session{},
		session.SessionRef{SessionID: "participant-runtime-steering-admission-failure"},
		binding,
		"participant-turn",
		nil,
		admission,
	)
	err := handler(submissionCtx, agent.Submission{
		Kind: agent.SubmissionKindConversation,
		Text: "guide",
	})
	if errorcode.CodeOf(err) != errorcode.FailedPrecondition || !errors.Is(err, promptErr) {
		t.Fatalf("participantSteeringHandler() error = %v, want failed_precondition retaining prompt failure", err)
	}
	if steerCalled {
		t.Fatal("initial prompt failure dispatched participant steering")
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
