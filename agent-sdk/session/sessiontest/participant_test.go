package sessiontest_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	sessionfile "github.com/caelis-labs/caelis/agent-sdk/session/file"
	sessionmemory "github.com/caelis-labs/caelis/agent-sdk/session/memory"
	"github.com/caelis-labs/caelis/agent-sdk/session/sessiontest"
)

func TestReferenceParticipantStoresConform(t *testing.T) {
	t.Run("memory", func(t *testing.T) {
		sessiontest.ParticipantLifecycleConformance(t, func(*testing.T) sessiontest.ParticipantStore {
			return &committedFaultParticipantStore{participantBackend: sessionmemory.NewStore(sessionmemory.Config{})}
		})
	})
	t.Run("file", func(t *testing.T) {
		sessiontest.ParticipantLifecycleConformance(t, func(t *testing.T) sessiontest.ParticipantStore {
			return &committedFaultParticipantStore{participantBackend: sessionfile.NewStore(sessionfile.Config{RootDir: t.TempDir()})}
		})
	})
}

type participantBackend interface {
	session.Service
	session.ParticipantLifecycleService
}

type committedFaultParticipantStore struct {
	participantBackend
	mu    sync.Mutex
	armed sessiontest.ParticipantMutation
}

func (s *committedFaultParticipantStore) ArmParticipantCommittedFailure(mutation sessiontest.ParticipantMutation) {
	s.mu.Lock()
	s.armed = mutation
	s.mu.Unlock()
}

func (s *committedFaultParticipantStore) consume(mutation sessiontest.ParticipantMutation) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.armed != mutation {
		return false
	}
	s.armed = ""
	return true
}

func (s *committedFaultParticipantStore) PutParticipant(ctx context.Context, req session.PutParticipantRequest) (session.Session, error) {
	updated, err := s.participantBackend.PutParticipant(ctx, req)
	if err == nil && s.consume(sessiontest.ParticipantPlainPut) {
		err = &session.CommittedError{Err: errors.New("forced post-commit report failure")}
	}
	return updated, err
}

func (s *committedFaultParticipantStore) RemoveParticipant(ctx context.Context, req session.RemoveParticipantRequest) (session.Session, error) {
	updated, err := s.participantBackend.RemoveParticipant(ctx, req)
	if err == nil && s.consume(sessiontest.ParticipantPlainRemove) {
		err = &session.CommittedError{Err: errors.New("forced post-commit report failure")}
	}
	return updated, err
}

func (s *committedFaultParticipantStore) PutParticipantWithEvent(ctx context.Context, req session.PutParticipantWithEventRequest) (session.Session, *session.Event, error) {
	updated, event, err := s.participantBackend.PutParticipantWithEvent(ctx, req)
	if err == nil && s.consume(sessiontest.ParticipantLifecyclePut) {
		err = &session.CommittedError{Err: errors.New("forced post-commit report failure")}
	}
	return updated, event, err
}

func (s *committedFaultParticipantStore) RemoveParticipantWithEvent(ctx context.Context, req session.RemoveParticipantWithEventRequest) (session.Session, *session.Event, error) {
	updated, event, err := s.participantBackend.RemoveParticipantWithEvent(ctx, req)
	if err == nil && s.consume(sessiontest.ParticipantLifecycleRemove) {
		err = &session.CommittedError{Err: errors.New("forced post-commit report failure")}
	}
	return updated, event, err
}

func (s *committedFaultParticipantStore) AcquireSessionLease(ctx context.Context, req session.AcquireSessionLeaseRequest) (session.SessionLease, error) {
	return s.participantBackend.(session.SessionLeaseService).AcquireSessionLease(ctx, req)
}

func (s *committedFaultParticipantStore) HeartbeatSessionLease(ctx context.Context, req session.HeartbeatSessionLeaseRequest) (session.SessionLease, error) {
	return s.participantBackend.(session.SessionLeaseService).HeartbeatSessionLease(ctx, req)
}

func (s *committedFaultParticipantStore) ReleaseSessionLease(ctx context.Context, req session.ReleaseSessionLeaseRequest) error {
	return s.participantBackend.(session.SessionLeaseService).ReleaseSessionLease(ctx, req)
}
