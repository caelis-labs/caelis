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

type removeParticipantWithEventFailingService struct {
	session.Service
	err error
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
