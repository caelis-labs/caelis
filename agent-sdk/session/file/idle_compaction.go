package file

import (
	"context"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

var _ session.IdleCompactionStore = (*Store)(nil)
var _ session.CompactionReceiptStore = (*Store)(nil)

type idleStoreMutation struct {
	event        *session.Event
	releaseFence bool
	validate     func() error
}
type idleStoreUpdate func(*session.IdleCompactionState, session.SessionFence, session.LoadedSession) (idleStoreMutation, error)

func authorizeDocumentMutation(doc *persistedDocument, guard session.MutationGuard) error {
	if err := validateFileMutationGuard(activeDocumentFence(*doc), guard); err != nil {
		return err
	}
	doc.Idle.Invalidate()
	return nil
}

type idleCommitValidationKey struct{}

func (s *Store) idleTransaction(ctx context.Context, ref session.SessionRef, update idleStoreUpdate) (*session.Event, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	var result *session.Event
	err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		events, err := s.eventsForDocumentContext(ctx, doc)
		if err != nil {
			return err
		}
		loaded := session.LoadedSession{Session: doc.Session, State: doc.State, Events: events}
		mutation, err := update(&doc.Idle, activeDocumentFence(doc), loaded)
		if err != nil {
			return err
		}
		if mutation.releaseFence {
			doc.Fence = nil
		}
		if mutation.event == nil {
			err := s.writeFenceDocument(ctx, doc)
			if documentWriteCommitted(err) {
				return &session.CommittedError{Err: err}
			}
			return err
		}
		next, tx, err := s.prepareAppendTransactionForDocument(ctx, doc, []*session.Event{mutation.event}, events, nil, nil, nil, "", "", nil)
		if err != nil {
			return err
		}
		if mutation.validate != nil {
			ctx = context.WithValue(ctx, idleCommitValidationKey{}, mutation.validate)
		}
		if err := s.writeRecoverableDocumentTransaction(ctx, next, tx.Prepared.Persisted, nil); err != nil {
			if documentWriteCommitted(err) {
				result = session.CloneEvent(tx.Prepared.Events[0])
				return &session.CommittedError{Err: err}
			}
			return err
		}
		result = session.CloneEvent(tx.Prepared.Events[0])
		return nil
	})
	return result, err
}

// ReleaseSessionFenceForIdleCompaction is one atomic fence-to-window transition.
func (s *Store) ReleaseSessionFenceForIdleCompaction(ctx context.Context, req session.ReleaseForIdleCompactionRequest) (session.IdleCompactionWindow, error) {
	var window session.IdleCompactionWindow
	_, err := s.idleTransaction(ctx, req.Release.SessionRef, func(state *session.IdleCompactionState, active session.SessionFence, loaded session.LoadedSession) (idleStoreMutation, error) {
		var err error
		window, err = state.Release(req, active, loaded, s.now())
		return idleStoreMutation{releaseFence: err == nil}, err
	})
	return window, err
}

// IssueIdleCompactionGrant fixes one checkpoint and its nonrenewable 2s deadline.
func (s *Store) IssueIdleCompactionGrant(ctx context.Context, req session.IssueIdleCompactionGrantRequest) (session.IdleCompactionGrant, error) {
	var grant session.IdleCompactionGrant
	_, err := s.idleTransaction(ctx, req.Window.SessionRef(), func(state *session.IdleCompactionState, active session.SessionFence, loaded session.LoadedSession) (idleStoreMutation, error) {
		var err error
		grant, err = state.IssueGrant(req, active, loaded, s.now())
		return idleStoreMutation{}, err
	})
	return grant, err
}

// AppendIdleCompactionCheckpoint consumes the grant in the checkpoint transaction.
func (s *Store) AppendIdleCompactionCheckpoint(ctx context.Context, req session.AppendIdleCompactionCheckpointRequest) (*session.Event, error) {
	return s.idleTransaction(ctx, req.Grant.SessionRef(), func(state *session.IdleCompactionState, active session.SessionFence, loaded session.LoadedSession) (idleStoreMutation, error) {
		replay, err := session.IdleCheckpointReplay(loaded, req.Event, s.now())
		if err != nil {
			return idleStoreMutation{}, err
		}
		if replay != nil {
			return idleStoreMutation{event: replay}, nil
		}
		if err := state.ValidateAppend(req, active, loaded, s.now()); err != nil {
			return idleStoreMutation{}, err
		}
		// Keep an unconsumed validation snapshot; consumption and append commit together.
		authority := session.CloneIdleCompactionState(*state)
		state.Window.Consumed = true
		return idleStoreMutation{event: req.Event, validate: func() error { return authority.ValidateAppend(req, active, loaded, s.now()) }}, nil
	})
}

// RevokeIdleCompaction cannot revoke a later job's window.
func (s *Store) RevokeIdleCompaction(ctx context.Context, window session.IdleCompactionWindow) error {
	_, err := s.idleTransaction(ctx, window.SessionRef(), func(state *session.IdleCompactionState, _ session.SessionFence, _ session.LoadedSession) (idleStoreMutation, error) {
		state.RevokeWindow(window)
		return idleStoreMutation{}, nil
	})
	return err
}

// BeginCompactionAttempt reserves a bounded accounting record before dispatch.
func (s *Store) BeginCompactionAttempt(ctx context.Context, req session.BeginCompactionAttemptRequest) (session.CompactionReceiptTicket, error) {
	var ticket session.CompactionReceiptTicket
	_, err := s.idleTransaction(ctx, req.Window.SessionRef(), func(state *session.IdleCompactionState, active session.SessionFence, loaded session.LoadedSession) (idleStoreMutation, error) {
		var err error
		ticket, err = state.BeginAttempt(req, active, loaded, s.now())
		return idleStoreMutation{}, err
	})
	return ticket, err
}

// StageCompactionReceipt seals terminal accounting independently of the foreground fence.
func (s *Store) StageCompactionReceipt(ctx context.Context, ticket session.CompactionReceiptTicket, result session.CompactionAttemptResult) error {
	_, err := s.idleTransaction(ctx, ticket.SessionRef(), func(state *session.IdleCompactionState, _ session.SessionFence, _ session.LoadedSession) (idleStoreMutation, error) {
		return idleStoreMutation{}, state.StageReceipt(ticket, result)
	})
	return err
}

// FinalizeCompactionReceipt atomically appends the sealed journal and clears pending state.
func (s *Store) FinalizeCompactionReceipt(ctx context.Context, ticket session.CompactionReceiptTicket) (*session.Event, error) {
	return s.idleTransaction(ctx, ticket.SessionRef(), func(state *session.IdleCompactionState, _ session.SessionFence, loaded session.LoadedSession) (idleStoreMutation, error) {
		event, err := state.ReceiptEvent(ticket, loaded)
		if err == nil {
			state.ForgetReceipt(ticket)
		}
		return idleStoreMutation{event: event}, err
	})
}

// CancelCompactionReservation reconciles a rejected start hook without billing
// a provider request that the SDK proved it did not dispatch.
func (s *Store) CancelCompactionReservation(ctx context.Context, req session.BeginCompactionAttemptRequest) error {
	_, err := s.idleTransaction(ctx, req.Window.SessionRef(), func(state *session.IdleCompactionState, _ session.SessionFence, _ session.LoadedSession) (idleStoreMutation, error) {
		return idleStoreMutation{}, state.CancelReservation(req)
	})
	return err
}

// RecoverCompactionReceipts uses the same exclusive product-Host proof as prior
// fence recovery. The ordinary Store does not expose this privileged operation.
func (s priorHostFenceService) RecoverCompactionReceipts(ctx context.Context, ref session.SessionRef, currentHost string) error {
	if s.store == nil || s.authorize == nil || currentHost == "" {
		return fileFenceConflict(ref, "receipt recovery is not authorized")
	}
	release, ok := s.authorize(ctx)
	if !ok || release == nil {
		return fileFenceConflict(ref, "product Host ownership is not held")
	}
	defer release()
	store := s.store
	if err := store.mu.LockContext(ctx); err != nil {
		return err
	}
	defer store.mu.Unlock()
	return store.withRootWriteLockContext(ctx, func() error {
		doc, err := store.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		epoch := doc.Idle.Epoch
		events := doc.Idle.RecoverReceipts(currentHost, ref, store.now())
		if len(events) == 0 {
			if doc.Idle.Epoch != epoch {
				return store.writeFenceDocument(ctx, doc)
			}
			return nil
		}
		next, tx, err := store.prepareAppendTransactionForDocument(ctx, doc, events, nil, nil, nil, nil, "", "", nil)
		if err != nil {
			return err
		}
		err = store.writeRecoverableDocumentTransaction(ctx, next, tx.Prepared.Persisted, nil)
		if documentWriteCommitted(err) {
			return &session.CommittedError{Err: err}
		}
		return err
	})
}
