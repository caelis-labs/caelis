package file

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

var _ session.SessionFenceService = (*Store)(nil)
var _ session.SessionFenceReader = (*Store)(nil)
var _ session.PriorHostFenceService = priorHostFenceService{}

type priorHostFenceService struct {
	store     *Store
	authorize func(context.Context) (release func(), ok bool)
}

func (s *Store) SessionFence(ctx context.Context, ref session.SessionRef) (session.SessionFence, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.SessionFence{}, err
	}
	defer s.mu.Unlock()
	var out session.SessionFence
	err := s.withRootLockContext(ctx, storeRootLockExclusive, func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		out = session.SessionFenceForObservation(activeDocumentFence(doc))
		return nil
	})
	return out, err
}

func (s *Store) AcquireSessionFence(ctx context.Context, req session.AcquireSessionFenceRequest) (session.SessionFence, error) {
	return s.acquireSessionFence(ctx, req, false)
}

func (s priorHostFenceService) ReplacePriorHostSessionFence(ctx context.Context, req session.AcquireSessionFenceRequest) (session.SessionFence, error) {
	if s.store == nil || s.authorize == nil {
		return session.SessionFence{}, fileFenceConflict(req.SessionRef, "prior-Host replacement is not authorized")
	}
	release, ok := s.authorize(ctx)
	if !ok || release == nil {
		return session.SessionFence{}, fileFenceConflict(req.SessionRef, "product Host ownership is not held")
	}
	defer release()
	return s.store.acquireSessionFence(ctx, req, true)
}

func (s *Store) acquireSessionFence(ctx context.Context, req session.AcquireSessionFenceRequest, replacePriorHost bool) (session.SessionFence, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return session.SessionFence{}, err
	}
	defer s.mu.Unlock()
	var out session.SessionFence
	err := s.withRootLockContext(ctx, storeRootLockExclusive, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		now := s.now()
		owner := strings.TrimSpace(req.OwnerID)
		if owner == "" {
			return fileFenceConflict(req.SessionRef, "owner_id is required")
		}
		active := activeDocumentFence(doc)
		if session.SessionFenceIsHeld(active) &&
			(!replacePriorHost || active.OwnerID == owner) {
			return fileFenceConflict(req.SessionRef, "another execution producer holds the fence")
		}
		fenceID, err := newFileSessionFenceID()
		if err != nil {
			return err
		}
		doc.FenceEpoch++
		fence, err := session.NewSessionFenceClaim(session.SessionFence{
			SessionRef: session.NormalizeSessionRef(doc.Session.SessionRef), FenceID: fenceID, OwnerID: owner,
			FencingToken: doc.FenceEpoch, AcquiredAt: now,
		})
		if err != nil {
			return err
		}
		doc.Fence = persistedFenceFromSession(session.SessionFenceForStorage(fence))
		if err := s.writeFenceDocument(ctx, doc); err != nil {
			if documentWriteCommitted(err) {
				out = fence
				return &session.CommittedError{Err: err}
			}
			return err
		}
		out = fence
		return nil
	})
	return out, err
}

func (s *Store) ReleaseSessionFence(ctx context.Context, req session.ReleaseSessionFenceRequest) error {
	if err := s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	return s.withRootLockContext(ctx, storeRootLockExclusive, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		active := sessionFenceFromPersisted(doc.Fence)
		if !session.SessionFenceReleaseAuthorized(active, req) {
			return fileFenceConflict(active.SessionRef, "fence identity, owner, or claim mismatch")
		}
		doc.Fence = nil
		if err := s.writeFenceDocument(ctx, doc); err != nil {
			if documentWriteCommitted(err) {
				return &session.CommittedError{Err: err}
			}
			return err
		}
		return nil
	})
}

// Fence transitions do not change the Session index projection. Avoiding the
// SQLite upsert keeps execution fencing off that unrelated durable write path.
func (s *Store) writeFenceDocument(ctx context.Context, doc persistedDocument) error {
	return s.writeDocumentInternal(ctx, doc, true, false)
}

func validateFileMutationGuard(active session.SessionFence, guard session.MutationGuard) error {
	return session.AuthorizeMutationGuard(active, guard)
}

func activeDocumentFence(doc persistedDocument) session.SessionFence {
	fence := sessionFenceFromPersisted(doc.Fence)
	if fence.FenceID == "" {
		fence.SessionRef = session.NormalizeSessionRef(doc.Session.SessionRef)
	}
	return fence
}

func fileFenceConflict(ref session.SessionRef, detail string) error {
	return &session.FenceConflictError{SessionID: session.NormalizeSessionRef(ref).SessionID, Detail: detail}
}

func newFileSessionFenceID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
