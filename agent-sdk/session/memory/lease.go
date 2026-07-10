package inmemory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

var _ session.SessionLeaseService = (*Service)(nil)

func (s *Store) AcquireSessionLease(_ context.Context, req session.AcquireSessionLeaseRequest) (session.SessionLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.SessionLease{}, session.ErrSessionNotFound
	}
	now := s.now()
	owner := strings.TrimSpace(req.OwnerID)
	if owner == "" || req.TTL <= 0 {
		return session.SessionLease{}, leaseConflict(req.SessionRef, "owner_id and positive TTL are required")
	}
	if record.lease.LeaseID != "" && record.lease.ExpiresAt.After(now) {
		if record.lease.OwnerID == owner {
			return record.lease, nil
		}
		return session.SessionLease{}, leaseConflict(req.SessionRef, "another live owner holds the lease")
	}
	leaseID, err := newSessionLeaseID()
	if err != nil {
		return session.SessionLease{}, err
	}
	record.lease = session.SessionLease{
		SessionRef: session.NormalizeSessionRef(record.session.SessionRef), LeaseID: leaseID, OwnerID: owner,
		Revision: 1, AcquiredAt: now, HeartbeatAt: now, ExpiresAt: now.Add(req.TTL),
	}
	return record.lease, nil
}

func (s *Store) HeartbeatSessionLease(_ context.Context, req session.HeartbeatSessionLeaseRequest) (session.SessionLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.SessionLease{}, session.ErrSessionNotFound
	}
	now := s.now()
	if err := validateLiveSessionLease(record.lease, req.LeaseID, req.OwnerID, req.ExpectedLeaseRevision, now, req.TTL); err != nil {
		return session.SessionLease{}, err
	}
	record.lease.Revision++
	record.lease.HeartbeatAt = now
	record.lease.ExpiresAt = now.Add(req.TTL)
	return record.lease, nil
}

func (s *Store) ReleaseSessionLease(_ context.Context, req session.ReleaseSessionLeaseRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.ErrSessionNotFound
	}
	if err := validateSessionLeaseIdentity(record.lease, req.LeaseID, req.OwnerID, req.ExpectedLeaseRevision); err != nil {
		return err
	}
	record.lease = session.SessionLease{}
	return nil
}

func (s *Service) AcquireSessionLease(ctx context.Context, req session.AcquireSessionLeaseRequest) (session.SessionLease, error) {
	return s.store.AcquireSessionLease(ctx, req)
}

func (s *Service) HeartbeatSessionLease(ctx context.Context, req session.HeartbeatSessionLeaseRequest) (session.SessionLease, error) {
	return s.store.HeartbeatSessionLease(ctx, req)
}

func (s *Service) ReleaseSessionLease(ctx context.Context, req session.ReleaseSessionLeaseRequest) error {
	return s.store.ReleaseSessionLease(ctx, req)
}

func validateLiveSessionLease(active session.SessionLease, leaseID, ownerID string, revision uint64, now time.Time, ttl time.Duration) error {
	if ttl <= 0 {
		return leaseConflict(active.SessionRef, "positive TTL is required")
	}
	if err := validateSessionLeaseIdentity(active, leaseID, ownerID, revision); err != nil {
		return err
	}
	if !active.ExpiresAt.After(now) {
		return leaseConflict(active.SessionRef, "lease has expired")
	}
	return nil
}

func validateSessionLeaseIdentity(active session.SessionLease, leaseID, ownerID string, revision uint64) error {
	if active.LeaseID == "" {
		return leaseConflict(active.SessionRef, "session has no active lease")
	}
	if active.LeaseID != strings.TrimSpace(leaseID) || active.OwnerID != strings.TrimSpace(ownerID) || active.Revision != revision {
		return leaseConflict(active.SessionRef, "lease identity, owner, or revision mismatch")
	}
	return nil
}

func leaseConflict(ref session.SessionRef, detail string) error {
	return &session.LeaseConflictError{SessionID: session.NormalizeSessionRef(ref).SessionID, Detail: detail}
}

func newSessionLeaseID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
