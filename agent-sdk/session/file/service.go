package file

import (
	"context"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (s *Service) StartSession(
	ctx context.Context,
	req session.StartSessionRequest,
) (session.Session, error) {
	return s.store.GetOrCreate(ctx, req)
}

func (s *Service) LoadSession(
	ctx context.Context,
	req session.LoadSessionRequest,
) (session.LoadedSession, error) {
	return s.store.LoadDocument(ctx, req)
}

func (s *Service) Session(
	ctx context.Context,
	ref session.SessionRef,
) (session.Session, error) {
	return s.store.Get(ctx, ref)
}

func (s *Service) AppendEvent(
	ctx context.Context,
	req session.AppendEventRequest,
) (*session.Event, error) {
	return s.store.appendEventRequest(req)
}

func (s *Service) AppendEvents(
	ctx context.Context,
	req session.AppendEventsRequest,
) ([]*session.Event, error) {
	return s.store.AppendEvents(ctx, req)
}

func (s *Service) AppendEventsAndUpdateState(
	ctx context.Context,
	req session.AppendEventsAndUpdateStateRequest,
) ([]*session.Event, error) {
	return s.store.AppendEventsAndUpdateState(ctx, req)
}

func (s *Service) Events(
	ctx context.Context,
	req session.EventsRequest,
) ([]*session.Event, error) {
	return s.store.Events(ctx, req)
}

func (s *Service) ListSessions(
	ctx context.Context,
	req session.ListSessionsRequest,
) (session.SessionList, error) {
	return s.store.List(ctx, req)
}

func (s *Service) BindController(
	ctx context.Context,
	req session.BindControllerRequest,
) (session.Session, error) {
	return s.store.BindController(ctx, req.SessionRef, req.Binding)
}

func (s *Service) BindControllerWithEvent(
	ctx context.Context,
	req session.BindControllerWithEventRequest,
) (session.Session, *session.Event, error) {
	return s.store.BindControllerWithEvent(ctx, req)
}

func (s *Service) PutParticipant(
	ctx context.Context,
	req session.PutParticipantRequest,
) (session.Session, error) {
	return s.store.PutParticipant(ctx, req.SessionRef, req.Binding)
}

func (s *Service) PutParticipantWithEvent(
	ctx context.Context,
	req session.PutParticipantWithEventRequest,
) (session.Session, *session.Event, error) {
	return s.store.PutParticipantWithEvent(ctx, req)
}

func (s *Service) RemoveParticipant(
	ctx context.Context,
	req session.RemoveParticipantRequest,
) (session.Session, error) {
	return s.store.RemoveParticipant(ctx, req.SessionRef, req.ParticipantID)
}

func (s *Service) RemoveParticipantWithEvent(
	ctx context.Context,
	req session.RemoveParticipantWithEventRequest,
) (session.Session, *session.Event, error) {
	return s.store.RemoveParticipantWithEvent(ctx, req)
}

func (s *Service) SnapshotState(
	ctx context.Context,
	ref session.SessionRef,
) (map[string]any, error) {
	return s.store.SnapshotState(ctx, ref)
}

func (s *Service) ReplaceState(
	ctx context.Context,
	ref session.SessionRef,
	state map[string]any,
) error {
	return s.store.ReplaceState(ctx, ref, state)
}

func (s *Service) UpdateState(
	ctx context.Context,
	ref session.SessionRef,
	update func(map[string]any) (map[string]any, error),
) error {
	return s.store.UpdateState(ctx, ref, update)
}
