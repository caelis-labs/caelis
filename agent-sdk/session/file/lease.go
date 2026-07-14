package file

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

var _ session.SessionLeaseService = (*Store)(nil)
var _ session.SessionLeaseReader = (*Store)(nil)

func (s *Store) SessionLease(ctx context.Context, ref session.SessionRef) (session.SessionLease, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.SessionLease{}, err
	}
	defer s.mu.Unlock()
	var out session.SessionLease
	err := s.withRootLockContext(ctx, storeRootLockExclusive, func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		out = activeDocumentLease(doc)
		return nil
	})
	return out, err
}

func (s *Store) AcquireSessionLease(ctx context.Context, req session.AcquireSessionLeaseRequest) (session.SessionLease, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.SessionLease{}, err
	}
	defer s.mu.Unlock()
	var out session.SessionLease
	err := s.withRootLockContext(ctx, storeRootLockExclusive, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		now := s.now()
		owner := strings.TrimSpace(req.OwnerID)
		if owner == "" || req.TTL <= 0 {
			return fileLeaseConflict(req.SessionRef, "owner_id and positive TTL are required")
		}
		if doc.Lease != nil && doc.Lease.LeaseID != "" && doc.Lease.ExpiresAt.After(now) {
			return fileLeaseConflict(req.SessionRef, "another live owner holds the lease")
		}
		leaseID, err := newFileSessionLeaseID()
		if err != nil {
			return err
		}
		doc.LeaseEpoch++
		lease := session.SessionLease{
			SessionRef: session.NormalizeSessionRef(doc.Session.SessionRef), LeaseID: leaseID, OwnerID: owner,
			Revision: 1, FencingToken: doc.LeaseEpoch, AcquiredAt: now, HeartbeatAt: now, ExpiresAt: now.Add(req.TTL),
		}
		doc.Lease = &lease
		if err := s.writeDocument(doc); err != nil {
			if documentWriteCommitted(err) {
				out = lease
				return &session.CommittedError{Err: err}
			}
			return err
		}
		out = lease
		return nil
	})
	return out, err
}

func (s *Store) HeartbeatSessionLease(ctx context.Context, req session.HeartbeatSessionLeaseRequest) (session.SessionLease, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.SessionLease{}, err
	}
	defer s.mu.Unlock()
	var out session.SessionLease
	err := s.withRootLockContext(ctx, storeRootLockExclusive, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		active := session.SessionLease{}
		if doc.Lease != nil {
			active = *doc.Lease
		}
		now := s.now()
		if err := validateFileLiveSessionLease(active, req.LeaseID, req.OwnerID, req.ExpectedLeaseRevision, now, req.TTL); err != nil {
			return err
		}
		active.Revision++
		active.HeartbeatAt = now
		active.ExpiresAt = now.Add(req.TTL)
		doc.Lease = &active
		if err := s.writeDocument(doc); err != nil {
			if documentWriteCommitted(err) {
				out = active
				return &session.CommittedError{Err: err}
			}
			return err
		}
		out = active
		return nil
	})
	return out, err
}

func (s *Store) ReleaseSessionLease(ctx context.Context, req session.ReleaseSessionLeaseRequest) error {
	if err := s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	return s.withRootLockContext(ctx, storeRootLockExclusive, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		active := session.SessionLease{}
		if doc.Lease != nil {
			active = *doc.Lease
		}
		if err := validateFileSessionLeaseIdentity(active, req.LeaseID, req.OwnerID, req.ExpectedLeaseRevision); err != nil {
			return err
		}
		doc.Lease = nil
		if err := s.writeDocument(doc); err != nil {
			if documentWriteCommitted(err) {
				return &session.CommittedError{Err: err}
			}
			return err
		}
		return nil
	})
}

func validateFileLiveSessionLease(active session.SessionLease, leaseID, ownerID string, revision uint64, now time.Time, ttl time.Duration) error {
	if ttl <= 0 {
		return fileLeaseConflict(active.SessionRef, "positive TTL is required")
	}
	if err := validateFileSessionLeaseIdentity(active, leaseID, ownerID, revision); err != nil {
		return err
	}
	if !active.ExpiresAt.After(now) {
		return fileLeaseConflict(active.SessionRef, "lease has expired")
	}
	return nil
}

func validateFileSessionLeaseIdentity(active session.SessionLease, leaseID, ownerID string, revision uint64) error {
	if active.LeaseID == "" {
		return fileLeaseConflict(active.SessionRef, "session has no active lease")
	}
	if active.LeaseID != strings.TrimSpace(leaseID) || active.OwnerID != strings.TrimSpace(ownerID) || active.Revision != revision {
		return fileLeaseConflict(active.SessionRef, "lease identity, owner, or revision mismatch")
	}
	return nil
}

func validateFileMutationGuard(active session.SessionLease, guard session.MutationGuard, now time.Time) error {
	return session.AuthorizeMutationGuard(active, guard, now)
}

func activeDocumentLease(doc persistedDocument) session.SessionLease {
	if doc.Lease == nil {
		return session.SessionLease{SessionRef: session.NormalizeSessionRef(doc.Session.SessionRef)}
	}
	return *doc.Lease
}

func fileLeaseConflict(ref session.SessionRef, detail string) error {
	return &session.LeaseConflictError{SessionID: session.NormalizeSessionRef(ref).SessionID, Detail: detail}
}

func newFileSessionLeaseID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
