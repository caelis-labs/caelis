package controlclient

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

type ClientConfig struct {
	Commands   controlport.CommandClient
	State      controlport.StateReader
	Feeds      controlport.FeedRegistry
	Authorizer Authorizer
	Sessions   interface {
		ListSessions(context.Context, session.ListSessionsRequest) (session.SessionList, error)
	}
}

type Client struct{ config ClientConfig }

func NewClient(config ClientConfig) (*Client, error) {
	if config.Commands == nil || config.State == nil || config.Feeds == nil || config.Authorizer == nil || config.Sessions == nil {
		return nil, errors.New("controlclient: client dependencies are required")
	}
	return &Client{config: config}, nil
}

func (c *Client) CreateSession(ctx context.Context, p controlport.Principal, req controlport.CreateSessionRequest) (controlport.CommandResult, error) {
	return c.config.Commands.CreateSession(ctx, p, req)
}
func (c *Client) CloseSession(ctx context.Context, p controlport.Principal, req controlport.CloseSessionRequest) (controlport.CommandResult, error) {
	return c.config.Commands.CloseSession(ctx, p, req)
}
func (c *Client) Prompt(ctx context.Context, p controlport.Principal, req controlport.PromptRequest) (controlport.CommandResult, error) {
	return c.config.Commands.Prompt(ctx, p, req)
}
func (c *Client) Steer(ctx context.Context, p controlport.Principal, req controlport.SteerRequest) (controlport.CommandResult, error) {
	return c.config.Commands.Steer(ctx, p, req)
}
func (c *Client) Cancel(ctx context.Context, p controlport.Principal, req controlport.CancelRequest) (controlport.CommandResult, error) {
	return c.config.Commands.Cancel(ctx, p, req)
}
func (c *Client) ResolveApproval(ctx context.Context, p controlport.Principal, req controlport.ResolveApprovalRequest) (controlport.CommandResult, error) {
	return c.config.Commands.ResolveApproval(ctx, p, req)
}
func (c *Client) AttachParticipant(ctx context.Context, p controlport.Principal, req controlport.AttachParticipantRequest) (controlport.CommandResult, error) {
	return c.config.Commands.AttachParticipant(ctx, p, req)
}
func (c *Client) PromptParticipant(ctx context.Context, p controlport.Principal, req controlport.PromptParticipantRequest) (controlport.CommandResult, error) {
	return c.config.Commands.PromptParticipant(ctx, p, req)
}
func (c *Client) CancelParticipant(ctx context.Context, p controlport.Principal, req controlport.CancelParticipantRequest) (controlport.CommandResult, error) {
	return c.config.Commands.CancelParticipant(ctx, p, req)
}
func (c *Client) DetachParticipant(ctx context.Context, p controlport.Principal, req controlport.DetachParticipantRequest) (controlport.CommandResult, error) {
	return c.config.Commands.DetachParticipant(ctx, p, req)
}
func (c *Client) Handoff(ctx context.Context, p controlport.Principal, req controlport.HandoffRequest) (controlport.CommandResult, error) {
	return c.config.Commands.Handoff(ctx, p, req)
}

func (c *Client) ListSessions(ctx context.Context, principal controlport.Principal, req controlport.ListSessionsRequest) (session.SessionList, error) {
	if err := c.config.Authorizer.Authorize(ctx, principal, controlport.ActionSessionList, ""); err != nil {
		return session.SessionList{}, err
	}
	listReq := session.ListSessionsRequest{WorkspaceKey: strings.TrimSpace(req.WorkspaceKey), Cursor: strings.TrimSpace(req.Cursor), Limit: req.Limit}
	if !hasRole(principal.Roles, "admin") {
		listReq.UserID = strings.TrimSpace(principal.ID)
	}
	return c.config.Sessions.ListSessions(ctx, listReq)
}

func (c *Client) InspectSession(ctx context.Context, principal controlport.Principal, req controlport.StateRequest) (controlport.SessionState, error) {
	if err := c.config.Authorizer.Authorize(ctx, principal, controlport.ActionSessionInspect, req.SessionID); err != nil {
		return controlport.SessionState{}, err
	}
	return c.config.State.State(ctx, req)
}

// Reconnect authorizes and delegates the atomic state/feed bootstrap.
func (c *Client) Reconnect(ctx context.Context, principal controlport.Principal, req controlport.ReconnectRequest) (controlport.ReconnectResult, error) {
	if err := c.config.Authorizer.Authorize(ctx, principal, controlport.ActionSessionInspect, req.SessionID); err != nil {
		return controlport.ReconnectResult{}, err
	}
	reconnect, ok := c.config.State.(controlport.ReconnectReader)
	if !ok {
		return controlport.ReconnectResult{}, errors.New("controlclient: reconnect service is unavailable")
	}
	return reconnect.Reconnect(ctx, req)
}

func (c *Client) Subscribe(ctx context.Context, principal controlport.Principal, req controlport.SubscribeRequest) (controlport.SubscribeResult, error) {
	if err := c.config.Authorizer.Authorize(ctx, principal, controlport.ActionSessionInspect, req.SessionID); err != nil {
		return controlport.SubscribeResult{}, err
	}
	feed, err := c.config.Feeds.Session(session.SessionRef{SessionID: strings.TrimSpace(req.SessionID)})
	if err != nil {
		return controlport.SubscribeResult{}, err
	}
	return feed.Subscribe(ctx, req)
}

func (c *Client) Events(ctx context.Context, principal controlport.Principal, req controlport.SubscribeRequest) (controlport.EventBatch, error) {
	result, err := c.Subscribe(ctx, principal, req)
	if err != nil {
		return controlport.EventBatch{}, err
	}
	defer result.Subscription.Close()
	out := controlport.EventBatch{ResumeMode: result.Mode, TransientGap: result.TransientGap, BoundaryCursor: result.BoundaryCursor}
	for {
		select {
		case <-ctx.Done():
			return controlport.EventBatch{}, ctx.Err()
		case envelope, open := <-result.Subscription.Backfill():
			if !open {
				if err := result.Subscription.Err(); err != nil {
					return controlport.EventBatch{}, err
				}
				return out, nil
			}
			out.Events = append(out.Events, eventstream.CloneEnvelope(envelope))
		}
	}
}
