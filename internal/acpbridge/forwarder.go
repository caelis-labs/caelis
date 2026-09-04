package acpbridge

import (
	"context"
	"errors"

	agentsdk "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ControllerForwarder forwards external ACP controller source streams into
// canonical persistence and live publication paths.
type ControllerForwarder struct {
	sessions session.Service
}

// NewControllerForwarder returns the default ACP controller event forwarder.
func NewControllerForwarder(sessions session.Service) *ControllerForwarder {
	return &ControllerForwarder{sessions: sessions}
}

// BeginControllerEvents implements agentsdk.ControllerEventForwarder.
func (f *ControllerForwarder) BeginControllerEvents(_ context.Context, req agentsdk.ControllerEventForwardRequest) (agentsdk.ControllerEventSession, error) {
	if f == nil || f.sessions == nil {
		return nil, errors.New("acpbridge: session service is required")
	}
	if req.Publisher == nil {
		return nil, errors.New("acpbridge: source event publisher is required")
	}
	return &controllerEventSession{forwarder: f, request: req}, nil
}

type controllerEventSession struct {
	forwarder   *ControllerForwarder
	request     agentsdk.ControllerEventForwardRequest
	accumulator narrativeAccumulator
}

func (s *controllerEventSession) ObserveSourceEvent(ctx context.Context, event agentsdk.SourceEvent) error {
	if s == nil || s.forwarder == nil {
		return errors.New("acpbridge: controller event session is unavailable")
	}
	if event.Err != nil {
		return event.Err
	}
	return s.forwarder.forwardSourceEvent(ctx, s.request, &s.accumulator, SourceEventFromAgent(event))
}

func (s *controllerEventSession) Complete(ctx context.Context) error {
	if s == nil || s.forwarder == nil {
		return errors.New("acpbridge: controller event session is unavailable")
	}
	if finalEvent := s.accumulator.finalAssistantEvent(); finalEvent != nil {
		persisted, err := s.forwarder.sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef:    s.request.SessionRef,
			MutationGuard: s.request.MutationGuard,
			Event:         finalEvent,
		})
		if err != nil {
			return err
		}
		s.request.Publisher.PublishSourceEvent(agentsdk.SourceEvent{
			Canonical:                        persisted,
			CanonicalContentAlreadyPublished: agentsdk.PublishedAssistantMessage,
		})
	}
	return nil
}

func (f *ControllerForwarder) forwardSourceEvent(ctx context.Context, req agentsdk.ControllerEventForwardRequest, accumulator *narrativeAccumulator, sourceEvent SourceEvent) error {
	normalize := req.Normalize
	if normalize == nil {
		normalize = func(_ session.Session, _ string, event *session.Event) *session.Event {
			return session.CloneEvent(event)
		}
	}
	normalized := normalize(req.ActiveSession, req.TurnID, sourceEvent.Canonical)
	if normalized != nil && req.IsUserEcho != nil && req.IsUserEcho(normalized) {
		return nil
	}
	if normalized != nil {
		if _, liveEvent, ok := accumulator.normalize(normalized); ok {
			if liveEvent != nil {
				updateType := eventUpdateType(liveEvent)
				if liveACP := envelopeWithNarrativeText(sourceEvent.ACP, updateType, narrativeEventText(liveEvent, updateType), narrativeMessageID(liveEvent)); liveACP != nil {
					req.Publisher.PublishSourceEvent(agentsdk.SourceEvent{Canonical: liveEvent, Native: liveACP})
				} else {
					req.Publisher.PublishEvent(liveEvent)
				}
			}
			return nil
		}
		accumulator.observeBarrier(normalized)

		if shouldPersistExternalACPEvent(normalized) {
			persisted, err := f.sessions.AppendEvent(ctx, session.AppendEventRequest{
				SessionRef:    req.SessionRef,
				MutationGuard: req.MutationGuard,
				Event:         normalized,
			})
			if err != nil {
				return err
			}
			normalized = persisted
		}
		if sourceEvent.ACP != nil {
			req.Publisher.PublishSourceEvent(agentsdk.SourceEvent{Canonical: normalized, Native: sourceEvent.ACP})
			return nil
		}
		if normalized != nil {
			req.Publisher.PublishEvent(normalized)
		}
		return nil
	}
	if sourceEvent.ACP != nil {
		req.Publisher.PublishSourceEvent(agentsdk.SourceEvent{Native: sourceEvent.ACP})
	}
	return nil
}
