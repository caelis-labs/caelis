package session

import (
	"context"
	"errors"
)

// Service reads and updates existing sessions by SessionRef. A Runtime shares one
// service across runs and acquires a writer for each prompt's session.
// Readers can query committed history throughout the turn.
type Service interface {
	Reader
	StateReader
	EventAppender
	LoadSession(context.Context, LoadSessionRequest) (LoadedSession, error)

	// AcquireWriter rejects a competing writer across service instances. Use
	// writeCtx for mutations and release after all producers stop, including
	// after cancellation or failed startup.
	AcquireWriter(ctx context.Context, ref SessionRef, owner string) (writeCtx context.Context, release func() error, err error)
	// AppendEvents commits the entire batch before any event becomes visible.
	AppendEvents(context.Context, AppendEventsRequest) ([]*Event, error)
	// BindController records the active controller under the current writer's authority.
	BindController(context.Context, BindControllerRequest) (Session, error)
}

// ManagedService adds host administration to the operations on an existing Session.
type ManagedService interface {
	Service
	Lifecycle
	ParticipantBindingStore
	StateWriter
}

// Store supplies the durable operations behind NewService. LoadSession returns
// a consistent snapshot and AppendEvents commits the whole batch. AcquireWriter
// reserves a producer across all service instances sharing the same storage.
type Store interface {
	LoadSession(context.Context, LoadSessionRequest) (LoadedSession, error)
	AcquireWriter(ctx context.Context, ref SessionRef, owner string) (writeCtx context.Context, release func() error, err error)
	AppendEvents(context.Context, AppendEventsRequest) ([]*Event, error)
	BindController(context.Context, BindControllerRequest) (Session, error)
}

type storedService struct{ Store }

// NewService derives individual reads and single-event appends from a Store.
// Hosts with a full-featured service can pass it directly to Runtime.
func NewService(store Store) Service {
	if store == nil {
		return nil
	}
	return storedService{store}
}

func (s storedService) Session(ctx context.Context, ref SessionRef) (Session, error) {
	snapshot, err := s.LoadSession(ctx, LoadSessionRequest{SessionRef: ref, Limit: 1})
	return snapshot.Session, err
}

func (s storedService) SnapshotState(ctx context.Context, ref SessionRef) (map[string]any, error) {
	snapshot, err := s.LoadSession(ctx, LoadSessionRequest{SessionRef: ref, Limit: 1})
	return snapshot.State, err
}

func (s storedService) Events(ctx context.Context, req EventsRequest) ([]*Event, error) {
	snapshot, err := s.LoadSession(ctx, LoadSessionRequest(req))
	return snapshot.Events, err
}

func (s storedService) AppendEvent(ctx context.Context, req AppendEventRequest) (*Event, error) {
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
