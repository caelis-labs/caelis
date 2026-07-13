package controlclient

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ApprovalRecoveryStore is the durable subset needed to settle approval
// prompts whose process-local continuation no longer exists after startup.
type ApprovalRecoveryStore interface {
	ListSessions(context.Context, session.ListSessionsRequest) (session.SessionList, error)
	EventsPage(context.Context, session.EventPageRequest) (session.EventPage, error)
	AppendEvent(context.Context, session.AppendEventRequest) (*session.Event, error)
}

// SweepAbandonedApprovals interrupts durable approval mirrors left without a
// live waiter. It is idempotent and must run before new Turns are accepted.
func SweepAbandonedApprovals(ctx context.Context, store ApprovalRecoveryStore) error {
	if store == nil {
		return nil
	}
	cursor := ""
	for {
		list, err := store.ListSessions(ctx, session.ListSessionsRequest{Cursor: cursor, Limit: 200})
		if err != nil {
			return err
		}
		for _, summary := range list.Sessions {
			if err := sweepSessionApprovals(ctx, store, summary.SessionRef); err != nil {
				return err
			}
		}
		if strings.TrimSpace(list.NextCursor) == "" || list.NextCursor == cursor {
			return nil
		}
		cursor = list.NextCursor
	}
}

func sweepSessionApprovals(ctx context.Context, store ApprovalRecoveryStore, ref session.SessionRef) error {
	pending := map[string]*session.Event{}
	afterSeq := uint64(0)
	for {
		page, err := store.EventsPage(ctx, session.EventPageRequest{
			SessionRef: ref, AfterSeq: afterSeq, Limit: 200, Visibility: session.EventPageAllDurable,
		})
		if err != nil {
			return err
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
	for requestID, request := range pending {
		event := abandonedApprovalSettlement(request, requestID)
		if _, err := store.AppendEvent(ctx, session.AppendEventRequest{SessionRef: ref, Event: event}); err != nil {
			return err
		}
	}
	return nil
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
