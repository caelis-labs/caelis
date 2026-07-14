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
	return s.store.appendEventRequest(ctx, req)
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

// EventsPage returns one bounded forward sequence page.
func (s *Service) EventsPage(
	ctx context.Context,
	req session.EventPageRequest,
) (session.EventPage, error) {
	return s.store.EventsPage(ctx, req)
}

func (s *Service) ListSessions(
	ctx context.Context,
	req session.ListSessionsRequest,
) (session.SessionList, error) {
	return s.store.List(ctx, req)
}

// PendingApprovals returns the Store-maintained abandoned-approval candidates.
func (s *Service) PendingApprovals(ctx context.Context) ([]session.PendingApproval, error) {
	return s.store.PendingApprovals(ctx)
}

// SettlePendingApproval atomically settles one still-pending recovery
// candidate.
func (s *Service) SettlePendingApproval(
	ctx context.Context,
	req session.SettlePendingApprovalRequest,
) (session.SettlePendingApprovalResult, error) {
	return s.store.SettlePendingApproval(ctx, req)
}

func (s *Service) BindController(
	ctx context.Context,
	req session.BindControllerRequest,
) (session.Session, error) {
	return s.store.bindControllerRequest(ctx, req)
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
	return s.store.putParticipantRequest(ctx, req)
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
	return s.store.removeParticipantRequest(ctx, req)
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
	req session.ReplaceStateRequest,
) (session.Session, error) {
	return s.store.ReplaceState(ctx, req)
}

func (s *Service) UpdateState(
	ctx context.Context,
	req session.UpdateStateRequest,
) (session.Session, error) {
	return s.store.UpdateState(ctx, req)
}
