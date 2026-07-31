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
	return c.config.Sessions.ListSessions(ctx, listReq)
}

// StartParticipant delegates the in-process-only participant start extension
// without adding it to the bounded HTTP command contract.
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
