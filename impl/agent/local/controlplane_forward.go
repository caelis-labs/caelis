package local

import (
	"context"
	"iter"

	"github.com/OnslaughtSnail/caelis/ports/controller"
	"github.com/OnslaughtSnail/caelis/ports/eventsource"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type acpForwardRequest struct {
	activeSession session.Session
	ref           session.SessionRef
	turnID        string
	source        controller.TurnHandle
	handle        *runner
	isUserEcho    func(*session.Event) bool
}

func (r *Runtime) forwardACPControllerEvents(ctx context.Context, req acpForwardRequest) error {
	accumulator := acpNarrativeAccumulator{}
	for sourceEvent, seqErr := range controllerSourceEvents(req.source) {
		if seqErr != nil {
			return seqErr
		}
		if err := r.forwardACPSourceEvent(ctx, req, &accumulator, sourceEvent); err != nil {
			return err
		}
	}
	if finalEvent := accumulator.finalAssistantEvent(); finalEvent != nil {
		persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
			SessionRef: req.ref,
			Event:      finalEvent,
		})
		if err != nil {
			return err
		}
		req.handle.publishEvent(persisted)
	}
	return nil
}

func (r *Runtime) forwardACPSourceEvent(ctx context.Context, req acpForwardRequest, accumulator *acpNarrativeAccumulator, sourceEvent eventsource.Event) error {
	normalized := normalizeEvent(req.activeSession, req.turnID, sourceEvent.Canonical)
	if normalized != nil && req.isUserEcho != nil && req.isUserEcho(normalized) {
		return nil
	}
	if normalized != nil {
		if _, liveEvent, ok := accumulator.normalize(normalized); ok {
			if liveEvent != nil {
				updateType := ""
				if liveEvent.Protocol != nil {
					updateType = liveEvent.Protocol.UpdateType
				}
				if liveACP := acpEnvelopeWithNarrativeText(sourceEvent.ACP, updateType, narrativeEventText(liveEvent, updateType)); liveACP != nil {
					req.handle.publishSourceEvent(eventsource.Event{Canonical: liveEvent, ACP: liveACP})
				} else {
					req.handle.publishEvent(liveEvent)
				}
			}
			return nil
		}

		if shouldPersistExternalACPEvent(normalized) {
			persisted, err := r.sessions.AppendEvent(ctx, session.AppendEventRequest{
				SessionRef: req.ref,
				Event:      normalized,
			})
			if err != nil {
				return err
			}
			normalized = persisted
		}
		if sourceEvent.ACP != nil {
			req.handle.publishSourceEvent(eventsource.Event{Canonical: normalized, ACP: sourceEvent.ACP})
			return nil
		}
		if normalized != nil {
			req.handle.publishEvent(normalized)
		}
		return nil
	}
	if sourceEvent.ACP != nil {
		req.handle.publishSourceEvent(eventsource.Event{ACP: sourceEvent.ACP})
	}
	return nil
}

func controllerSourceEvents(handle controller.TurnHandle) iter.Seq2[eventsource.Event, error] {
	if sourceHandle, ok := handle.(eventsource.Handle); ok && sourceHandle != nil {
		return sourceHandle.SourceEvents()
	}
	return func(yield func(eventsource.Event, error) bool) {
		for event, err := range handle.Events() {
			if !yield(eventsource.Event{Canonical: event}, err) {
				return
			}
		}
	}
}
