package controlclient

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ApprovalRecoveryStore is the durable subset needed to settle approval
// prompts whose process-local continuation no longer exists after startup.
type ApprovalRecoveryStore interface {
	ListSessions(context.Context, session.ListSessionsRequest) (session.SessionList, error)
	Session(context.Context, session.SessionRef) (session.Session, error)
	EventsPage(context.Context, session.EventPageRequest) (session.EventPage, error)
	SettlePendingApproval(context.Context, session.SettlePendingApprovalRequest) (session.SettlePendingApprovalResult, error)
}

// SweepAbandonedApprovals interrupts durable approval mirrors left without a
// live waiter. It is idempotent and must run before new Turns are accepted.
// A request protected by a live execution lease belongs to another active
// Runtime and is left for a later sweep after that lease expires.
func SweepAbandonedApprovals(ctx context.Context, store ApprovalRecoveryStore) error {
	_, err := sweepAbandonedApprovals(ctx, store)
	return err
}

type approvalRecoverySweep struct {
	retryAt time.Time
}

func (r *approvalRecoverySweep) deferUntil(candidate time.Time) {
	if candidate.IsZero() {
		return
	}
	if r.retryAt.IsZero() || candidate.Before(r.retryAt) {
		r.retryAt = candidate
	}
}

func sweepAbandonedApprovals(ctx context.Context, store ApprovalRecoveryStore) (approvalRecoverySweep, error) {
	var result approvalRecoverySweep
	if store == nil {
		return result, nil
	}
	if indexed, ok := store.(session.ApprovalRecoveryReader); ok {
		pending, err := indexed.PendingApprovals(ctx)
		if err != nil {
			return result, err
		}
		for _, approval := range pending {
			requestID := ""
			if approval.Request != nil {
				requestID = strings.TrimSpace(approval.Request.ApprovalRequestID)
			}
			if requestID == "" {
				continue
			}
			event := abandonedApprovalSettlement(approval.Request, requestID)
			retryAt, err := settleAbandonedApproval(ctx, store, approval, event)
			if err != nil {
				return result, err
			}
			result.deferUntil(retryAt)
		}
		return result, nil
	}
	cursor := ""
	for {
		list, err := store.ListSessions(ctx, session.ListSessionsRequest{Cursor: cursor, Limit: 200})
		if err != nil {
			return result, err
		}
		for _, summary := range list.Sessions {
			retryAt, err := sweepSessionApprovals(ctx, store, summary.SessionRef)
			if err != nil {
				return result, err
			}
			result.deferUntil(retryAt)
		}
		if strings.TrimSpace(list.NextCursor) == "" || list.NextCursor == cursor {
			return result, nil
		}
		cursor = list.NextCursor
	}
}

func sweepSessionApprovals(ctx context.Context, store ApprovalRecoveryStore, ref session.SessionRef) (time.Time, error) {
	pending := map[string]*session.Event{}
	afterSeq := uint64(0)
	for {
		page, err := store.EventsPage(ctx, session.EventPageRequest{
			SessionRef: ref, AfterSeq: afterSeq, Limit: 200, Visibility: session.EventPageAllDurable,
		})
		if err != nil {
			return time.Time{}, err
		}
		for _, event := range page.Events {
			requestID := strings.TrimSpace(event.ApprovalRequestID)
			if requestID == "" {
				continue
			}
			switch {
			case session.ProtocolPermissionOf(event) != nil:
				pending[requestID] = event
			case event.Lifecycle != nil:
				delete(pending, requestID)
			}
		}
		if page.NextSeq <= afterSeq || !page.HasMore {
			break
		}
		afterSeq = page.NextSeq
	}
	if len(pending) == 0 {
		return time.Time{}, nil
	}
	active, err := store.Session(ctx, ref)
	if err != nil {
		return time.Time{}, err
	}
	var retryAt time.Time
	for requestID, request := range pending {
		event := abandonedApprovalSettlement(request, requestID)
		candidate, err := settleAbandonedApproval(ctx, store, session.PendingApproval{
			SessionRef: ref,
			Revision:   active.Revision,
			Request:    request,
		}, event)
		if err != nil {
			return time.Time{}, err
		}
		if !candidate.IsZero() && (retryAt.IsZero() || candidate.Before(retryAt)) {
			retryAt = candidate
		}
	}
	return retryAt, nil
}

func abandonedApprovalSettlementRequest(
	approval session.PendingApproval,
	expectedRevision *uint64,
	event *session.Event,
) session.SettlePendingApprovalRequest {
	requestID := ""
	requestEventID := ""
	requestSeq := uint64(0)
	if approval.Request != nil {
		requestID = strings.TrimSpace(approval.Request.ApprovalRequestID)
		requestEventID = strings.TrimSpace(approval.Request.ID)
		requestSeq = approval.Request.Seq
	}
	return session.SettlePendingApprovalRequest{
		SessionRef:       approval.SessionRef,
		ExpectedRevision: expectedRevision,
		// Startup recovery is not a decision from the live approval plane. Use a
		// non-overlapping lifecycle guard so the Store atomically rejects a
		// foreign Runtime lease acquired between discovery and settlement.
		MutationGuard:          session.ControlMutationGuard(session.ControlMutationPurposeLifecycle),
		ApprovalRequestID:      requestID,
		ExpectedRequestEventID: requestEventID,
		ExpectedRequestSeq:     requestSeq,
		Settlement:             event,
	}
}

func settleAbandonedApproval(
	ctx context.Context,
	store ApprovalRecoveryStore,
	approval session.PendingApproval,
	event *session.Event,
) (time.Time, error) {
	expectedRevision := approval.Revision
	for range 8 {
		_, err := store.SettlePendingApproval(ctx, abandonedApprovalSettlementRequest(approval, &expectedRevision, event))
		if err == nil {
			return time.Time{}, nil
		}
		var revisionConflict *session.RevisionConflictError
		if errors.As(err, &revisionConflict) {
			expectedRevision = revisionConflict.Actual
			continue
		}
		if session.IsCommitted(err) {
			// Re-enter the same conditional operation. A recovered commit reports
			// Settled=false because the request is no longer pending.
			continue
		}
		if !errors.Is(err, session.ErrLeaseConflict) {
			return time.Time{}, err
		}
		// Preserve progress without blocking startup. The gate schedules a scoped
		// re-sweep at the foreign lease's durable expiry; a concurrent heartbeat may
		// extend it and will simply return a later retry boundary.
		retryAt := time.Now().Add(time.Second)
		if reader, ok := store.(session.SessionLeaseReader); ok {
			lease, readErr := reader.SessionLease(ctx, approval.SessionRef)
			if readErr == nil && strings.TrimSpace(lease.LeaseID) != "" && !lease.ExpiresAt.IsZero() {
				retryAt = lease.ExpiresAt
			}
		}
		return retryAt, nil
	}
	return time.Now().Add(approvalRecoveryRetryFloor), nil
}

func abandonedApprovalSettlement(request *session.Event, requestID string) *session.Event {
	sessionID := ""
	if request != nil {
		sessionID = strings.TrimSpace(request.SessionID)
	}
	event := &session.Event{
		IdempotencyKey:    "approval-settlement:" + sessionID + ":" + requestID + ":startup_recovery",
		Type:              session.EventTypeLifecycle,
		Visibility:        session.VisibilityMirror,
		ApprovalRequestID: requestID,
		Actor:             session.ActorRef{Kind: session.ActorKindSystem, Name: "control"},
		Lifecycle:         &session.EventLifecycle{Status: "interrupted", Reason: "startup_recovery"},
	}
	if request != nil {
		event.Scope = session.CloneEvent(request).Scope
		if request.ChildOrigin != nil {
			origin := session.CloneEventChildOrigin(*request.ChildOrigin)
			origin.SourceEventID += ":startup_recovery"
			event.ChildOrigin = &origin
		}
	}
	return event
}
