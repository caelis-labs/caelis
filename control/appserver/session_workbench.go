package appserver

import (
	"context"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// CapabilitySessionWorkbench identifies the paged Session overview and read receipts.
const CapabilitySessionWorkbench = "session-workbench-v1"

// SessionWorkbenchRequest pages through the authorized Session directory.
// RefreshIDs rechecks already mounted rows without walking earlier pages.
type SessionWorkbenchRequest struct {
	Cursor     string
	Limit      int
	RefreshIDs []string
}

// SessionWorkbenchRow is the metadata needed to render one Session in the overview.
type SessionWorkbenchRow struct {
	SessionID    string    `json:"session_id"`
	WorkspaceKey string    `json:"workspace_key,omitempty"`
	CWD          string    `json:"cwd,omitempty"`
	Title        string    `json:"title,omitempty"`
	UpdatedAt    time.Time `json:"updated_at"`
	Active       bool      `json:"active,omitempty"`
	Unread       bool      `json:"unread,omitempty"`
	Failed       bool      `json:"failed,omitempty"`
}

// SessionWorkbenchPage contains the next directory page and explicitly refreshed rows.
type SessionWorkbenchPage struct {
	Sessions   []SessionWorkbenchRow `json:"sessions"`
	Refreshed  []SessionWorkbenchRow `json:"refreshed"`
	NextCursor string                `json:"next_cursor,omitempty"`
}

// SessionReadReceipt advances the caller's read position through one observed result.
type SessionReadReceipt struct {
	ThroughSeq uint64 `json:"through_seq,string"`
}

// SessionWorkbenchClient is the optional principal-bound overview capability.
type SessionWorkbenchClient interface {
	ListSessionWorkbench(context.Context, SessionWorkbenchRequest) (SessionWorkbenchPage, error)
	AcknowledgeSessionRead(context.Context, string, SessionReadReceipt) error
}

// SessionWorkbenchService exposes the overview to authenticated transports.
type SessionWorkbenchService interface {
	HasSessionWorkbench() bool
	ListSessionWorkbench(context.Context, Principal, SessionWorkbenchRequest) (SessionWorkbenchPage, error)
	AcknowledgeSessionRead(context.Context, Principal, string, SessionReadReceipt) error
}

// ActiveSessionsReader samples existing execution without creating observations.
type ActiveSessionsReader interface {
	ActiveSessionIDs(context.Context) ([]string, error)
}

// HasSessionWorkbench reports whether this Host can serve the overview.
func (c *Client) HasSessionWorkbench() bool {
	return c.config.SessionWorkbench != nil && c.config.ActiveSessions != nil
}

// ListSessionWorkbench lists active and unread Sessions before the remaining
// directory. The same authorization and visibility rules apply to both pages.
func (c *Client) ListSessionWorkbench(ctx context.Context, principal Principal, req SessionWorkbenchRequest) (SessionWorkbenchPage, error) {
	if !c.HasSessionWorkbench() {
		return SessionWorkbenchPage{}, errorcode.New(errorcode.Unsupported, "Session workbench unavailable")
	}
	if err := c.config.Authorizer.Authorize(ctx, principal, ActionSessionList, ""); err != nil {
		return SessionWorkbenchPage{}, err
	}
	limit := req.Limit
	if limit == 0 {
		limit = 40
	}
	if limit < 1 || limit > 100 || len(req.RefreshIDs) > 100 {
		return SessionWorkbenchPage{}, errorcode.New(errorcode.InvalidArgument, "invalid Session workbench page size")
	}
	lane, cursor := "priority", ""
	if req.Cursor != "" {
		var ok bool
		lane, cursor, ok = strings.Cut(req.Cursor, ":")
		if !ok || cursor == "" || lane != "priority" && lane != "recent" {
			return SessionWorkbenchPage{}, errorcode.New(errorcode.InvalidArgument, "invalid Session workbench cursor")
		}
	}

	activeIDs, err := c.config.ActiveSessions.ActiveSessionIDs(ctx)
	if err != nil {
		return SessionWorkbenchPage{}, err
	}
	active := make(map[string]bool, len(activeIDs))
	for _, id := range activeIDs {
		active[id] = true
	}
	unreadIDs, err := c.config.SessionWorkbench.unreadSessions(ctx, principal)
	if err != nil {
		return SessionWorkbenchPage{}, err
	}
	priorityIDs := append(activeIDs, unreadIDs...)
	var summaries []session.SessionSummary
	next := ""
	if lane == "priority" && len(priorityIDs) != 0 {
		list, err := c.listSessionDirectory(ctx, principal, session.ListSessionsRequest{
			IncludeIDs: priorityIDs, Cursor: cursor, Limit: limit,
		})
		if err != nil {
			return SessionWorkbenchPage{}, err
		}
		summaries = list.Sessions
		if list.NextCursor != "" {
			next = "priority:" + list.NextCursor
		}
	}
	if next == "" {
		recentCursor := ""
		if lane == "recent" {
			recentCursor = cursor
			if recentCursor == "start" {
				recentCursor = ""
			}
		}
		if remaining := limit - len(summaries); remaining > 0 {
			list, err := c.listSessionDirectory(ctx, principal, session.ListSessionsRequest{
				ExcludeIDs: priorityIDs, Cursor: recentCursor, Limit: remaining,
			})
			if err != nil {
				return SessionWorkbenchPage{}, err
			}
			summaries = append(summaries, list.Sessions...)
			if list.NextCursor != "" {
				next = "recent:" + list.NextCursor
			}
		} else {
			list, err := c.listSessionDirectory(ctx, principal, session.ListSessionsRequest{ExcludeIDs: priorityIDs, Limit: 1})
			if err != nil {
				return SessionWorkbenchPage{}, err
			}
			if len(list.Sessions) != 0 {
				next = "recent:start"
			}
		}
	}
	var refreshed []session.SessionSummary
	if len(req.RefreshIDs) != 0 {
		list, err := c.listSessionDirectory(ctx, principal, session.ListSessionsRequest{
			IncludeIDs: req.RefreshIDs, Limit: len(req.RefreshIDs),
		})
		if err != nil {
			return SessionWorkbenchPage{}, err
		}
		refreshed = list.Sessions
	}
	ids := make([]string, 0, len(summaries)+len(refreshed))
	for _, rows := range [][]session.SessionSummary{summaries, refreshed} {
		for _, row := range rows {
			ids = append(ids, row.SessionID)
		}
	}
	states, err := c.config.SessionWorkbench.states(ctx, principal.ID, ids)
	if err != nil {
		return SessionWorkbenchPage{}, err
	}
	project := func(rows []session.SessionSummary) []SessionWorkbenchRow {
		result := make([]SessionWorkbenchRow, 0, len(rows))
		for _, row := range rows {
			state := states[row.SessionID]
			result = append(result, SessionWorkbenchRow{
				SessionID: row.SessionID, WorkspaceKey: row.WorkspaceKey, CWD: row.CWD,
				Title: row.Title, UpdatedAt: row.UpdatedAt,
				Active: active[row.SessionID], Unread: state.unread, Failed: state.failed,
			})
		}
		return result
	}
	return SessionWorkbenchPage{Sessions: project(summaries), Refreshed: project(refreshed), NextCursor: next}, nil
}

// AcknowledgeSessionRead records an authorized, monotonic read position.
func (c *Client) AcknowledgeSessionRead(ctx context.Context, principal Principal, id string, receipt SessionReadReceipt) error {
	if !c.HasSessionWorkbench() {
		return errorcode.New(errorcode.Unsupported, "Session workbench unavailable")
	}
	if receipt.ThroughSeq == 0 {
		return errorcode.New(errorcode.InvalidArgument, "read sequence is required")
	}
	id = strings.TrimSpace(id)
	if err := c.config.Authorizer.Authorize(ctx, principal, ActionSessionInspect, id); err != nil {
		return err
	}
	return c.config.SessionWorkbench.acknowledge(ctx, principal.ID, id, receipt.ThroughSeq)
}
