package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/internal/acpbridge"
)

func TestTurnHandleCanonicalForwardingFailureCancelsProducer(t *testing.T) {
	for _, wantErr := range []error{errors.New("append failed"), session.ErrFenceConflict} {
		t.Run(wantErr.Error(), func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			sessions := &forwardingFailureSession{err: wantErr}
			forwarding, err := acpbridge.NewControllerForwarder(sessions).BeginControllerEvents(ctx, agent.ControllerEventForwardRequest{
				SessionRef: session.SessionRef{SessionID: "session-1"}, Publisher: discardSourcePublisher{},
			})
			if err != nil {
				t.Fatal(err)
			}
			handle := newTurnHandle(ctx, cancel, forwarding)
			message := model.NewTextMessage(model.RoleUser, "canonical fact")
			handle.publishEvent(&session.Event{Type: session.EventTypeUser, Visibility: session.VisibilityCanonical, Message: &message})
			if sessions.calls != 1 {
				t.Fatalf("AppendEvent calls = %d, want 1", sessions.calls)
			}
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
				t.Fatal("canonical forwarding failure did not cancel the producer")
			}
			// Cancellation is not proof that the producer has stopped.
			select {
			case <-handle.done:
				t.Fatal("forwarding failure completed the handle before producer quiescence")
			default:
			}
			handle.publishError(context.Canceled)
			handle.finish()
			if err := handle.WaitCompletion(t.Context()); !errors.Is(err, wantErr) {
				t.Fatalf("WaitCompletion() = %v, want original forwarding error %v", err, wantErr)
			}
		})
	}
}

type forwardingFailureSession struct {
	session.Service
	err   error
	calls int
}

func (s *forwardingFailureSession) AppendEvent(context.Context, session.AppendEventRequest) (*session.Event, error) {
	s.calls++
	return nil, s.err
}

type discardSourcePublisher struct{}

func (discardSourcePublisher) PublishEvent(*session.Event)          {}
func (discardSourcePublisher) PublishSourceEvent(agent.SourceEvent) {}
