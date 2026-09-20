package appserver

import (
	"context"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// SessionActivityReader samples existing live Control handles only. Reads must
// not activate a Runtime, inspect durable history, or create a feed subscription.
type SessionActivityReader interface {
	SessionRunning(sessionID string) bool
}

// ListSessions reads authorized directory metadata and samples activity only
// for the returned page. Reconnect state and history remain explicit reads.
func (c *Client) ListSessions(ctx context.Context, principal Principal, req ListSessionsRequest) (SessionList, error) {
	list, err := c.listSessions(ctx, principal, req)
	if err != nil {
		return SessionList{}, err
	}
	out := SessionList{Sessions: list.Sessions, NextCursor: list.NextCursor}
	if c.config.SessionActivity != nil {
		for _, summary := range out.Sessions {
			if err := ctx.Err(); err != nil {
				return SessionList{}, err
			}
			if c.config.SessionActivity.SessionRunning(summary.SessionID) {
				out.RunningSessionIDs = append(out.RunningSessionIDs, summary.SessionID)
			}
		}
	}
	return out, nil
}

func (c *Client) listSessions(ctx context.Context, principal Principal, req ListSessionsRequest) (session.SessionList, error) {
	if err := c.config.Authorizer.Authorize(ctx, principal, ActionSessionList, ""); err != nil {
		return session.SessionList{}, err
	}
	listReq := session.ListSessionsRequest{
		WorkspaceKey: strings.TrimSpace(req.WorkspaceKey),
		CWD:          strings.TrimSpace(req.CWD),
		Cursor:       strings.TrimSpace(req.Cursor),
		Limit:        req.Limit,
	}
	if !principal.HasRole("admin") {
		listReq.UserID = strings.TrimSpace(principal.ID)
	}
	if req.Limit <= 0 {
		list, err := c.config.Sessions.ListSessions(ctx, listReq)
		if err != nil {
			return session.SessionList{}, err
		}
		list.Sessions = userVisibleSessionSummaries(list.Sessions)
		return list, nil
	}

	visible := make([]session.SessionSummary, 0, req.Limit)
	seen := make(map[string]struct{}, req.Limit)
	cursor := listReq.Cursor
	for len(visible) < req.Limit {
		listReq.Cursor = cursor
		listReq.Limit = req.Limit - len(visible)
		page, err := c.config.Sessions.ListSessions(ctx, listReq)
		if err != nil {
			return session.SessionList{}, err
		}
		for _, summary := range userVisibleSessionSummaries(page.Sessions) {
			sessionID := strings.TrimSpace(summary.SessionID)
			if _, ok := seen[sessionID]; ok {
				continue
			}
			seen[sessionID] = struct{}{}
			visible = append(visible, summary)
		}
		next := strings.TrimSpace(page.NextCursor)
		if len(visible) >= req.Limit {
			return session.SessionList{
				Sessions:   session.CloneSessionSummaries(visible[:req.Limit]),
				NextCursor: next,
			}, nil
		}
		if next == "" || next == cursor {
			return session.SessionList{Sessions: session.CloneSessionSummaries(visible)}, nil
		}
		cursor = next
	}
	return session.SessionList{Sessions: session.CloneSessionSummaries(visible)}, nil
}
