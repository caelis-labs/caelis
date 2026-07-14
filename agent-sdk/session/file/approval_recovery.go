package file

import (
	"context"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

var _ session.ApprovalRecoveryService = (*Store)(nil)

// PendingApprovals returns every durable permission without a later
// settlement. A stable SessionRef snapshot is captured under one short root
// transaction; per-Session recovery then releases all locks between Sessions.
func (s *Store) PendingApprovals(ctx context.Context) ([]session.PendingApproval, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	refs, err := s.pendingApprovalSessionRefs(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]session.PendingApproval, 0)
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pending, err := s.pendingApprovalsForSession(ctx, ref)
		if err != nil {
			return nil, err
		}
		out = append(out, pending...)
		if s.approvalRecoverySessionDone != nil {
			s.approvalRecoverySessionDone(ref)
		}
	}
	return out, nil
}

func (s *Store) pendingApprovalSessionRefs(ctx context.Context) ([]session.SessionRef, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	var out []session.SessionRef
	err := s.withRootReadLockContext(ctx, func() error {
		list, err := s.listFromSessionIndex(session.ListSessionsRequest{})
		if err != nil {
			return err
		}
		seen := make(map[string]struct{}, len(list.Sessions))
		out = make([]session.SessionRef, 0, len(list.Sessions))
		for _, summary := range list.Sessions {
			ref := session.NormalizeSessionRef(summary.SessionRef)
			if ref.SessionID == "" {
				continue
			}
			if _, ok := seen[ref.SessionID]; ok {
				continue
			}
			seen[ref.SessionID] = struct{}{}
			out = append(out, ref)
		}
		return nil
	})
	return out, err
}

func (s *Store) pendingApprovalsForSession(ctx context.Context, ref session.SessionRef) ([]session.PendingApproval, error) {
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()
	var out []session.PendingApproval
	err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		if doc.PendingApprovals == nil {
			events, err := s.eventsForDocumentContext(ctx, doc)
			if err != nil {
				return err
			}
			doc.PendingApprovals = pendingApprovalsFromEvents(events)
			// This is derived persistence metadata, not a semantic Session
			// mutation: revision, lease, state, and canonical events stay fixed.
			if err := s.writeDocumentInternal(doc, false, false); err != nil {
				return err
			}
		}
		requestIDs := make([]string, 0, len(doc.PendingApprovals))
		for requestID := range doc.PendingApprovals {
			requestIDs = append(requestIDs, requestID)
		}
		sort.Strings(requestIDs)
		for _, requestID := range requestIDs {
			request := doc.PendingApprovals[requestID]
			if request == nil {
				continue
			}
			out = append(out, session.PendingApproval{
				SessionRef: doc.Session.SessionRef, Revision: doc.Session.Revision,
				Request: session.CloneEvent(request),
			})
		}
		return nil
	})
	return out, err
}

// SettlePendingApproval appends one approval settlement only while the exact
// request observed by recovery remains pending at the expected revision. The
// pending check, lease guard, revision CAS, and append share one root lock.
func (s *Store) SettlePendingApproval(
	ctx context.Context,
	req session.SettlePendingApprovalRequest,
) (session.SettlePendingApprovalResult, error) {
	if err := session.ValidateSettlePendingApprovalRequest(req); err != nil {
		return session.SettlePendingApprovalResult{}, err
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return session.SettlePendingApprovalResult{}, err
	}
	defer s.mu.Unlock()

	var out session.SettlePendingApprovalResult
	err := s.withRootWriteLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(req.SessionRef)
		if err != nil {
			return err
		}
		var existingEvents []*session.Event
		if doc.PendingApprovals == nil {
			existingEvents, err = s.eventsForDocumentContext(ctx, doc)
			if err != nil {
				return err
			}
			doc.PendingApprovals = pendingApprovalsFromEvents(existingEvents)
		}
		current := doc.PendingApprovals[strings.TrimSpace(req.ApprovalRequestID)]
		if !session.PendingApprovalMatches(current, req) {
			return nil
		}
		if err := validateFileMutationGuard(activeDocumentLease(doc), req.MutationGuard, s.now()); err != nil {
			return err
		}
		if err := session.CheckExpectedRevision(doc.Session, req.ExpectedRevision); err != nil {
			return err
		}
		if existingEvents == nil {
			existingEvents, err = s.eventsForDocumentContext(ctx, doc)
			if err != nil {
				return err
			}
		}
		nextDoc, tx, err := s.prepareAppendTransactionForDocument(
			doc,
			[]*session.Event{req.Settlement},
			existingEvents,
			nil,
			nil,
			req.ExpectedRevision,
			"",
			"",
		)
		if err != nil {
			return err
		}
		out.Settled = tx.Changed
		if len(tx.Prepared.Events) > 0 {
			out.Event = session.CloneEvent(tx.Prepared.Events[0])
		}
		if !tx.Changed {
			return nil
		}
		return s.writeDocumentWithEvents(nextDoc, tx.Prepared.Persisted)
	})
	return out, err
}

func pendingApprovalsFromEvents(events []*session.Event) map[string]*session.Event {
	pending := map[string]*session.Event{}
	applyPendingApprovalEvents(pending, events)
	return pending
}

func applyPendingApprovalEvents(pending map[string]*session.Event, events []*session.Event) {
	for _, event := range events {
		requestID := ""
		if event != nil {
			requestID = strings.TrimSpace(event.ApprovalRequestID)
		}
		if requestID == "" {
			continue
		}
		switch {
		case session.ProtocolPermissionOf(event) != nil:
			pending[requestID] = session.CloneEvent(event)
		case event.Lifecycle != nil:
			delete(pending, requestID)
		}
	}
}

func clonePendingApprovals(in map[string]*session.Event) map[string]*session.Event {
	if in == nil {
		return nil
	}
	out := make(map[string]*session.Event, len(in))
	for requestID, event := range in {
		out[requestID] = session.CloneEvent(event)
	}
	return out
}
