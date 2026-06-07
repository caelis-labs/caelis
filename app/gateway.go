package app

import (
	"context"
	"fmt"
	"iter"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/gateway"
	"github.com/OnslaughtSnail/caelis/runner"
	"github.com/OnslaughtSnail/caelis/session"
)

type runtimeGateway struct {
	sessions session.Service
	runner   *runner.Runner

	mu    sync.Mutex
	turns map[string]runtimeTurn
}

type runtimeTurn struct {
	sessionRef session.Ref
	branch     string
}

func newRuntimeGateway(sessions session.Service, r *runner.Runner) gateway.Service {
	return &runtimeGateway{
		sessions: sessions,
		runner:   r,
		turns:    make(map[string]runtimeTurn),
	}
}

func (g *runtimeGateway) CreateSession(ctx context.Context, req gateway.CreateSessionRequest) (session.Session, error) {
	return g.sessions.Create(ctx, session.CreateRequest{
		AppName:      req.AppName,
		UserID:       req.UserID,
		WorkspaceKey: req.WorkspaceKey,
		Title:        req.Title,
		Workspace:    req.Workspace,
		Controller:   req.Controller,
		Participants: req.Participants,
	})
}

func (g *runtimeGateway) ListSessions(ctx context.Context, req gateway.ListSessionsRequest) (gateway.ListSessionsResponse, error) {
	resp, err := g.sessions.List(ctx, session.ListRequest{
		AppName:      req.AppName,
		UserID:       req.UserID,
		WorkspaceKey: req.WorkspaceKey,
		Cursor:       req.Cursor,
		Limit:        req.Limit,
	})
	if err != nil {
		return gateway.ListSessionsResponse{}, err
	}
	return gateway.ListSessionsResponse{Sessions: resp.Sessions, Cursor: resp.Cursor}, nil
}

func (g *runtimeGateway) DeleteSession(ctx context.Context, req gateway.DeleteSessionRequest) error {
	return g.sessions.Delete(ctx, req.SessionRef)
}

func (g *runtimeGateway) BeginTurn(_ context.Context, req gateway.TurnRequest) (gateway.Turn, error) {
	if err := req.SessionRef.Validate(); err != nil {
		return gateway.Turn{}, err
	}
	turnID := fmt.Sprintf("turn-%d", time.Now().UnixNano())
	g.mu.Lock()
	g.turns[turnID] = runtimeTurn{sessionRef: req.SessionRef, branch: req.Branch}
	g.mu.Unlock()
	return gateway.Turn{TurnID: turnID, SessionID: req.SessionRef.String()}, nil
}

func (g *runtimeGateway) Submit(ctx context.Context, req gateway.SubmitRequest) error {
	g.mu.Lock()
	turn, ok := g.turns[req.TurnID]
	g.mu.Unlock()
	if !ok {
		return fmt.Errorf("turn not found: %s", req.TurnID)
	}
	for _, err := range g.runner.Run(ctx, runner.RunRequest{
		SessionRef:  turn.sessionRef,
		Branch:      turn.branch,
		UserMessage: req.UserMessage,
		Metadata:    req.Metadata,
	}) {
		if err != nil {
			return err
		}
	}
	return nil
}

func (g *runtimeGateway) Cancel(_ context.Context, req gateway.CancelRequest) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	if _, ok := g.turns[req.TurnID]; !ok {
		return fmt.Errorf("turn not found: %s", req.TurnID)
	}
	delete(g.turns, req.TurnID)
	return nil
}

func (g *runtimeGateway) Replay(ctx context.Context, req gateway.ReplayRequest) (gateway.ReplayResponse, error) {
	events, err := g.sessions.Events(ctx, session.EventsRequest{
		SessionRef: req.SessionRef,
		AfterID:    req.AfterID,
		Limit:      req.Limit,
	})
	if err != nil {
		return gateway.ReplayResponse{}, err
	}
	out := make([]gateway.EventEnvelope, 0, len(events))
	for _, event := range events {
		out = append(out, eventEnvelope(event))
	}
	var cursor string
	if len(events) > 0 {
		cursor = events[len(events)-1].ID
	}
	return gateway.ReplayResponse{Events: out, Cursor: cursor}, nil
}

func (g *runtimeGateway) Subscribe(ctx context.Context, req gateway.SubscribeRequest) iter.Seq2[gateway.EventEnvelope, error] {
	return func(yield func(gateway.EventEnvelope, error) bool) {
		events, err := g.sessions.Events(ctx, session.EventsRequest{
			SessionRef: req.SessionRef,
			AfterID:    req.AfterID,
		})
		if err != nil {
			yield(gateway.EventEnvelope{}, err)
			return
		}
		for _, event := range events {
			if !yield(eventEnvelope(event), nil) {
				return
			}
		}
	}
}

func eventEnvelope(event session.Event) gateway.EventEnvelope {
	return gateway.EventEnvelope{
		Kind:      string(event.Kind),
		SessionID: event.SessionRef.String(),
		RunID:     event.RunID,
		Payload:   event,
	}
}
