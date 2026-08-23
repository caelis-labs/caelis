package inmemory

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

func (s *Store) SessionFence(_ context.Context, ref session.SessionRef) (session.SessionFence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	record, ok := s.lookupLocked(ref)
	if !ok {
		return session.SessionFence{}, session.ErrSessionNotFound
	}
	return session.SessionFenceForObservation(record.fence), nil
}

func (s *Store) AcquireSessionFence(_ context.Context, req session.AcquireSessionFenceRequest) (session.SessionFence, error) {
	return s.acquireSessionFence(req, false)
}

func (s priorHostFenceService) ReplacePriorHostSessionFence(ctx context.Context, req session.AcquireSessionFenceRequest) (session.SessionFence, error) {
	if s.store == nil || s.authorize == nil {
		return session.SessionFence{}, fenceConflict(req.SessionRef, "prior-Host replacement is not authorized")
	}
	release, ok := s.authorize(ctx)
	if !ok || release == nil {
		return session.SessionFence{}, fenceConflict(req.SessionRef, "product Host ownership is not held")
	}
	defer release()
	return s.store.acquireSessionFence(req, true)
}

func (s *Store) acquireSessionFence(req session.AcquireSessionFenceRequest, replacePriorHost bool) (session.SessionFence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.SessionFence{}, session.ErrSessionNotFound
	}
	now := s.now()
	owner := strings.TrimSpace(req.OwnerID)
	if owner == "" {
		return session.SessionFence{}, fenceConflict(req.SessionRef, "owner_id is required")
	}
	if session.SessionFenceIsHeld(record.fence) &&
		(!replacePriorHost || record.fence.OwnerID == owner) {
		return session.SessionFence{}, fenceConflict(req.SessionRef, "another execution producer holds the fence")
	}
	fenceID, err := newSessionFenceID()
	if err != nil {
		return session.SessionFence{}, err
	}
	record.fenceEpoch++
	fence, err := session.NewSessionFenceClaim(session.SessionFence{
		SessionRef: session.NormalizeSessionRef(record.session.SessionRef), FenceID: fenceID, OwnerID: owner,
		FencingToken: record.fenceEpoch, AcquiredAt: now,
	})
	if err != nil {
		return session.SessionFence{}, err
	}
	record.fence = session.SessionFenceForStorage(fence)
	return fence, nil
}

func (s *Store) ReleaseSessionFence(_ context.Context, req session.ReleaseSessionFenceRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.lookupLocked(req.SessionRef)
	if !ok {
		return session.ErrSessionNotFound
	}
	if !session.SessionFenceReleaseAuthorized(record.fence, req) {
		return fenceConflict(record.fence.SessionRef, "fence identity, owner, or claim mismatch")
	}
	record.fence = session.SessionFence{}
	return nil
}

func validateMutationGuard(active session.SessionFence, guard session.MutationGuard) error {
	return session.AuthorizeMutationGuard(active, guard)
}

func fenceConflict(ref session.SessionRef, detail string) error {
	return &session.FenceConflictError{SessionID: session.NormalizeSessionRef(ref).SessionID, Detail: detail}
}

func newSessionFenceID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
