package file

import (
	"context"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// PendingApprovals returns every durable permission without a later
// settlement. One root transaction-recovery barrier covers the entire scan;
// current documents read the derived index without decoding event payloads.
func (s *Store) PendingApprovals(ctx context.Context) ([]session.PendingApproval, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.mu.Unlock()

	var out []session.PendingApproval
	err := s.withRootReadLockContext(ctx, func() error {
		list, err := s.listFromSessionIndex(session.ListSessionsRequest{})
		if err != nil {
			return err
		}
		for _, summary := range list.Sessions {
			if err := ctx.Err(); err != nil {
				return err
			}
			doc, err := s.readDocumentForRef(summary.SessionRef)
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
				out = append(out, session.PendingApproval{SessionRef: doc.Session.SessionRef, Request: session.CloneEvent(request)})
			}
		}
		return nil
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
