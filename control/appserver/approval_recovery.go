package appserver

import (
	"context"
	"errors"
	"fmt"
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
// A request protected by an execution fence belongs to another active
// Runtime and is left for a later sweep after that execution fence releases.
func SweepAbandonedApprovals(ctx context.Context, store ApprovalRecoveryStore) error {
	_, err := sweepAbandonedApprovals(ctx, store)
	return err
}

type approvalRecoveryAuthority struct {
	ownerID         string
	priorHostFences PriorHostFenceReplacer
}

// PriorHostFenceReplacer is the Host-owned capability used only during
// startup recovery after process-level Store ownership has been established.
// Product composition supplies the implementation, while the Store verifies
// the live process-level ownership pin before replacing durable state.
type PriorHostFenceReplacer interface {
	ReplacePriorHostFence(context.Context, session.AcquireSessionFenceRequest) (session.SessionFence, error)
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

func sweepAbandonedApprovals(
	ctx context.Context,
	store ApprovalRecoveryStore,
) (approvalRecoverySweep, error) {
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

// sweepPriorHostSessionFences is the only product path allowed to replace a
// durable execution fence. Host ownership proves the prior process is gone,
// and the startup gate completes this full sweep before admitting new Turns.
func sweepPriorHostSessionFences(
	ctx context.Context,
	store ApprovalRecoveryStore,
	authority approvalRecoveryAuthority,
	diagnostics *approvalRecoveryDiagnostics,
) error {
	ownerID := strings.TrimSpace(authority.ownerID)
	if authority.priorHostFences == nil {
		return nil
	}
	if ownerID == "" {
		return fmt.Errorf("appserver: prior Host fence recovery requires fence_owner_id")
	}
	fences, ok := store.(session.SessionFenceService)
	if !ok {
		return fmt.Errorf("appserver: prior Host fence recovery requires a session fence service")
	}
	cursor := ""
	for {
		list, err := store.ListSessions(ctx, session.ListSessionsRequest{Cursor: cursor, Limit: 200})
		if err != nil {
			return err
		}
		for _, summary := range list.Sessions {
			durable, err := fences.SessionFence(ctx, summary.SessionRef)
			if err != nil {
				return err
			}
			if strings.TrimSpace(durable.FenceID) == "" || strings.TrimSpace(durable.OwnerID) == ownerID {
				continue
			}
			recovered, err := acquireApprovalRecoveryFence(ctx, authority.priorHostFences, session.AcquireSessionFenceRequest{
				SessionRef: summary.SessionRef,
				OwnerID:    ownerID,
			})
			if err != nil {
				return err
			}
			if err := releaseApprovalRecoveryFence(ctx, fences, recovered, diagnostics); err != nil {
				return err
			}
		}
		if strings.TrimSpace(list.NextCursor) == "" || list.NextCursor == cursor {
			return nil
		}
		cursor = list.NextCursor
	}
}

func sweepSessionApprovals(
	ctx context.Context,
	store ApprovalRecoveryStore,
	ref session.SessionRef,
) (time.Time, error) {
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
	guard session.MutationGuard,
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
		SessionRef:             approval.SessionRef,
		ExpectedRevision:       expectedRevision,
		MutationGuard:          guard,
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
	guard := session.ControlMutationGuard(session.ControlMutationPurposeLifecycle)
	for range 8 {
		_, err := store.SettlePendingApproval(ctx, abandonedApprovalSettlementRequest(approval, &expectedRevision, event, guard))
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
		if !errors.Is(err, session.ErrFenceConflict) {
			return time.Time{}, err
		}
		return time.Now().Add(time.Second), nil
	}
	return time.Now().Add(approvalRecoveryRetryFloor), nil
}

func releaseApprovalRecoveryFence(
	ctx context.Context,
	fences session.SessionFenceService,
	fence session.SessionFence,
	diagnostics *approvalRecoveryDiagnostics,
) error {
	if ctx == nil {
		ctx = context.Background()
	}
	delay := 100 * time.Millisecond
	for {
		started := diagnostics.started()
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		err := fences.ReleaseSessionFence(releaseCtx, session.SessionFenceReleaseRequest(fence))
		cancel()
		diagnostics.observe(ctx, "startup_release", started, err)
		if session.IsCommitted(err) || err == nil {
			return nil
		}

		started = diagnostics.started()
		readCtx, readCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		durable, readErr := fences.SessionFence(readCtx, fence.SessionRef)
		readCancel()
		diagnostics.observe(ctx, "startup_release_reconcile", started, readErr)
		if readErr == nil && (strings.TrimSpace(durable.FenceID) == "" ||
			durable.FenceID != fence.FenceID || durable.OwnerID != fence.OwnerID || durable.FencingToken != fence.FencingToken) {
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return errors.Join(err, readErr, ctxErr)
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return errors.Join(err, readErr, ctx.Err())
		case <-timer.C:
		}
		if delay < 5*time.Second {
			delay *= 2
			if delay > 5*time.Second {
				delay = 5 * time.Second
			}
		}
	}
}

func acquireApprovalRecoveryFence(
	ctx context.Context,
	replacer PriorHostFenceReplacer,
	req session.AcquireSessionFenceRequest,
) (session.SessionFence, error) {
	acquired, err := replacer.ReplacePriorHostFence(ctx, req)
	if !session.IsCommitted(err) {
		return acquired, err
	}
	if approvalRecoveryFenceMatches(req, acquired) {
		return acquired, nil
	}
	return acquired, err
}

func approvalRecoveryFenceMatches(req session.AcquireSessionFenceRequest, fence session.SessionFence) bool {
	return session.NormalizeSessionRef(req.SessionRef) == session.NormalizeSessionRef(fence.SessionRef) &&
		strings.TrimSpace(req.OwnerID) == strings.TrimSpace(fence.OwnerID) &&
		strings.TrimSpace(fence.FenceID) != "" && fence.FencingToken > 0 &&
		session.SessionFenceHasClaim(fence)
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
