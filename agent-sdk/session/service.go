package session

import (
	"context"
	"errors"
)

// Store supplies the durable operations behind NewService. LoadSession returns
// a consistent snapshot and AppendEvents commits the whole batch. BeginTurn
// reserves a producer across all service instances sharing the same storage.
type Store interface {
	TurnAdmission
	EventBatchService
	ControllerBindingStore
	LoadSession(context.Context, LoadSessionRequest) (LoadedSession, error)
}

type runService struct{ Store }

// NewService builds a native RunService over a storage adapter. An adapter can
// implement RunService directly to optimize individual reads or single appends.
func NewService(store Store) RunService {
	if store == nil {
		return nil
	}
	return runService{store}
}

func (s runService) Session(ctx context.Context, ref SessionRef) (Session, error) {
	snapshot, err := s.LoadSession(ctx, LoadSessionRequest{SessionRef: ref, Limit: 1})
	return snapshot.Session, err
}

func (s runService) SnapshotState(ctx context.Context, ref SessionRef) (map[string]any, error) {
	snapshot, err := s.LoadSession(ctx, LoadSessionRequest{SessionRef: ref, Limit: 1})
	return snapshot.State, err
}

func (s runService) Events(ctx context.Context, req EventsRequest) ([]*Event, error) {
	snapshot, err := s.LoadSession(ctx, LoadSessionRequest(req))
	return snapshot.Events, err
}

func (s runService) AppendEvent(ctx context.Context, req AppendEventRequest) (*Event, error) {
	if req.Event == nil {
		return nil, ErrInvalidEvent
	}
	events, err := s.AppendEvents(ctx, AppendEventsRequest{
		SessionRef: req.SessionRef, ExpectedRevision: req.ExpectedRevision, MutationGuard: req.MutationGuard, Events: []*Event{req.Event},
	})
	if len(events) == 1 && events[0] != nil {
		return events[0], err
	}
	if err == nil {
		err = errors.New("agent-sdk/session: append returned no matching event")
	}
	return nil, err
}
