package inmemory

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

func (r *record) authorizeMutation(guard session.MutationGuard) error {
	if err := validateMutationGuard(r.fence, guard); err != nil {
		return err
	}
	r.idle.Invalidate()
	return nil
}

func (s *Store) idleTransaction(ctx context.Context, ref session.SessionRef, update idleStoreUpdate) (*session.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.lookupLocked(ref)
	if !ok {
		return nil, session.ErrSessionNotFound
	}
	loaded := session.LoadedSession{Session: record.session, State: record.state, Events: record.events}
	state := session.CloneIdleCompactionState(record.idle)
	mutation, err := update(&state, record.fence, loaded)
	if err != nil {
		return nil, err
	}
	var result *session.Event
	if mutation.event != nil {
		tx, err := s.prepareAppendTransactionForRecord(record, []*session.Event{mutation.event}, nil, nil, nil, "", "")
		if err != nil {
			return nil, err
		}
		if mutation.validate != nil {
			if err := mutation.validate(); err != nil {
				return nil, err
			}
		}
		s.applyAppendTransactionToRecord(record, tx)
		result = session.CloneEvent(tx.Prepared.Events[0])
	}
	if mutation.releaseFence {
		record.fence = session.SessionFence{}
	}
	record.idle = state
	return result, nil
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
// fence recovery. Ordinary Store operations cannot declare an old Host dead.
func (s priorHostFenceService) RecoverCompactionReceipts(ctx context.Context, ref session.SessionRef, currentHost string) error {
	if s.store == nil || s.authorize == nil || currentHost == "" {
		return fenceConflict(ref, "receipt recovery is not authorized")
	}
	release, ok := s.authorize(ctx)
	if !ok || release == nil {
		return fenceConflict(ref, "product Host ownership is not held")
	}
	defer release()
	store := s.store
	store.mu.Lock()
	defer store.mu.Unlock()
	record, ok := store.lookupLocked(ref)
	if !ok {
		return session.ErrSessionNotFound
	}
	state := session.CloneIdleCompactionState(record.idle)
	events := state.RecoverReceipts(currentHost, ref, store.now())
	if len(events) == 0 {
		record.idle = state
		return nil
	}
	tx, err := store.prepareAppendTransactionForRecord(record, events, nil, nil, nil, "", "")
	if err != nil {
		return err
	}
	store.applyAppendTransactionToRecord(record, tx)
	record.idle = state
	return nil
}
