package controlclient

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

type ClientConfig struct {
	Commands           CommandClient
	State              StateReader
	Feeds              FeedRegistry
	Authorizer         Authorizer
	ParticipantHandles ParticipantHandleReader
	Sessions           interface {
		ListSessions(context.Context, session.ListSessionsRequest) (session.SessionList, error)
	}
}

type Client struct {
	CommandClient
	config ClientConfig
}

func NewClient(config ClientConfig) (*Client, error) {
	if config.Commands == nil || config.State == nil || config.Feeds == nil || config.Authorizer == nil || config.Sessions == nil {
		return nil, errors.New("controlclient: client dependencies are required")
	}
	return &Client{CommandClient: config.Commands, config: config}, nil
}

func (c *Client) ListSessions(ctx context.Context, principal Principal, req ListSessionsRequest) (session.SessionList, error) {
	if err := c.config.Authorizer.Authorize(ctx, principal, ActionSessionList, ""); err != nil {
		return session.SessionList{}, err
	}
	listReq := session.ListSessionsRequest{WorkspaceKey: strings.TrimSpace(req.WorkspaceKey), Cursor: strings.TrimSpace(req.Cursor), Limit: req.Limit}
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

// StartParticipant delegates the focused participant capability implemented by
// the underlying command service. AppServer transports expose this operation
// through ParticipantClient rather than expanding SessionClient.
func (c *Client) StartParticipant(ctx context.Context, principal Principal, req StartParticipantRequest) (CommandResult, error) {
	starter, ok := c.CommandClient.(ParticipantStarter)
	if !ok {
		return CommandResult{}, errors.New("controlclient: participant start service is unavailable")
	}
	return starter.StartParticipant(ctx, principal, req)
}

// ListParticipantHandles returns the Session Runtime's frozen direct-command
// catalog when activated, or the current catalog for an idle Session.
func (c *Client) ListParticipantHandles(ctx context.Context, principal Principal, sessionID string) ([]string, error) {
	sessionID = strings.TrimSpace(sessionID)
	if err := c.config.Authorizer.Authorize(ctx, principal, ActionParticipantList, sessionID); err != nil {
		return nil, err
	}
	if c.config.ParticipantHandles == nil {
		return nil, errors.New("controlclient: participant handle service is unavailable")
	}
	handles, err := c.config.ParticipantHandles.ParticipantHandles(ctx, sessionID)
	return append([]string(nil), handles...), err
}

func (c *Client) InspectSession(ctx context.Context, principal Principal, req StateRequest) (SessionState, error) {
	if err := c.config.Authorizer.Authorize(ctx, principal, ActionSessionInspect, req.SessionID); err != nil {
		return SessionState{}, err
	}
	return c.config.State.State(ctx, req)
}

// Reconnect authorizes and delegates the atomic state/feed bootstrap.
func (c *Client) Reconnect(ctx context.Context, principal Principal, req ReconnectRequest) (ReconnectResult, error) {
	if err := c.config.Authorizer.Authorize(ctx, principal, ActionSessionInspect, req.SessionID); err != nil {
		return ReconnectResult{}, err
	}
	reconnect, ok := c.config.State.(ReconnectReader)
	if !ok {
		return ReconnectResult{}, errors.New("controlclient: reconnect service is unavailable")
	}
	// Exact-target observation of system-managed Sessions is available to any
	// principal that already passed Session authorization (owner/admin). This is
	// the Host-side capability product ACP and Agent-message delivery use so
	// Surface tokens do not need RoleSystemSessionRuntime. List/load/resume still
	// hide managed Sessions from discovery via userVisibleSessionSummaries.
	return reconnect.Reconnect(ctx, req)
}

func (c *Client) Subscribe(ctx context.Context, principal Principal, req SubscribeRequest) (SubscribeResult, error) {
	if err := c.config.Authorizer.Authorize(ctx, principal, ActionSessionInspect, req.SessionID); err != nil {
		return SubscribeResult{}, err
	}
	feed, err := c.config.Feeds.Session(session.SessionRef{SessionID: strings.TrimSpace(req.SessionID)})
	if err != nil {
		return SubscribeResult{}, err
	}
	return feed.Subscribe(ctx, req)
}

func (c *Client) Events(ctx context.Context, principal Principal, req SubscribeRequest) (EventBatch, error) {
	result, err := c.Subscribe(ctx, principal, req)
	if err != nil {
		return EventBatch{}, err
	}
	defer result.Subscription.Close()
	out := EventBatch{ResumeMode: result.Mode, TransientGap: result.TransientGap, BoundaryCursor: result.BoundaryCursor}
	for {
		select {
		case <-ctx.Done():
			return EventBatch{}, ctx.Err()
		case envelope, open := <-result.Subscription.Backfill():
			if !open {
				if err := result.Subscription.Err(); err != nil {
					return EventBatch{}, err
				}
				return out, nil
			}
			out.Events = append(out.Events, eventstream.CloneEnvelope(envelope))
		}
	}
}

var _ Service = (*Client)(nil)
