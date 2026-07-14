package controladapter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclientport "github.com/caelis-labs/caelis/ports/controlclient"
	"github.com/caelis-labs/caelis/ports/gateway"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const clientProtocolSessionListLimit = 200

func (d *Adapter) ListSessionSnapshots(ctx context.Context, req schema.SessionListRequest) (schema.SessionListResponse, error) {
	gw, err := d.gatewaySessions()
	if err != nil {
		return schema.SessionListResponse{}, err
	}
	result, err := gw.ListSessions(ctx, gateway.ListSessionsRequest{
		AppName: d.stack.Session.AppName,
		UserID:  d.stack.Session.UserID,
		Cursor:  strings.TrimSpace(req.Cursor),
		Limit:   clientProtocolSessionListLimit,
	})
	if err != nil {
		return schema.SessionListResponse{}, err
	}
	out := schema.SessionListResponse{
		Sessions:   make([]schema.SessionSummary, 0, len(result.Sessions)),
		NextCursor: strings.TrimSpace(result.NextCursor),
	}
	cwdFilter := strings.TrimSpace(req.CWD)
	for _, summary := range result.Sessions {
		if cwdFilter != "" && strings.TrimSpace(summary.CWD) != cwdFilter {
			continue
		}
		out.Sessions = append(out.Sessions, protocolSessionSummary(summary))
	}
	return out, nil
}

func (d *Adapter) Replay(ctx context.Context, req eventstream.ReplayRequest) (eventstream.ReplayResult, error) {
	ref, err := d.protocolSessionRef(req.SessionID)
	if err != nil {
		return eventstream.ReplayResult{}, err
	}
	result, err := d.replayControlFeed(ctx, ref.SessionID, req)
	if err != nil {
		return eventstream.ReplayResult{}, err
	}
	var active gateway.ActiveTurnState
	var hasActive bool
	if turns, err := d.gatewayTurns(); err == nil && turns != nil {
		active, hasActive = activeTurnStateForSession(turns.ActiveTurns(), ref)
	}
	state, err := d.gatewayControlPlane()
	if err != nil {
		return eventstream.ReplayResult{}, err
	}
	controlState, err := state.ControlPlaneState(ctx, gateway.ControlPlaneStateRequest{SessionRef: ref, BindingKey: d.bindingKey})
	if err != nil {
		return eventstream.ReplayResult{}, err
	}
	runState := protocolRunStateFromGateway(controlState, active, hasActive)
	result.RunState = runState
	result.HasLiveHandle = hasActive || controlState.HasActiveTurn
	return result, nil
}

func (d *Adapter) replayControlFeed(ctx context.Context, sessionID string, req eventstream.ReplayRequest) (eventstream.ReplayResult, error) {
	if d == nil || d.stack == nil || d.stack.ControlFeeds == nil {
		return eventstream.ReplayResult{}, missingRuntimeDependency("control client feed")
	}
	feed, err := d.stack.ControlFeeds.Session(session.SessionRef{SessionID: strings.TrimSpace(sessionID)})
	if err != nil {
		return eventstream.ReplayResult{}, err
	}
	subscribed, err := feed.Subscribe(ctx, controlclientport.SubscribeRequest{SessionID: strings.TrimSpace(sessionID), Cursor: strings.TrimSpace(req.Cursor)})
	if err != nil {
		return eventstream.ReplayResult{}, err
	}
	defer subscribed.Subscription.Close()
	out := eventstream.ReplayResult{
		SessionID: strings.TrimSpace(sessionID), NextCursor: strings.TrimSpace(subscribed.BoundaryCursor), Durable: true,
	}
	consume := func(envelope eventstream.Envelope) bool {
		if !req.IncludeTransient && envelope.Delivery != nil && envelope.Delivery.Mode == eventstream.DeliveryTransient {
			return true
		}
		out.Events = append(out.Events, eventstream.CloneEnvelope(envelope))
		out.NextCursor = strings.TrimSpace(envelope.Cursor)
		return req.Limit <= 0 || len(out.Events) < req.Limit
	}
	if len(subscribed.Backfill) > 0 {
		for _, envelope := range subscribed.Backfill {
			if !consume(envelope) {
				return out, nil
			}
		}
		return out, nil
	}
	for {
		select {
		case <-ctx.Done():
			return eventstream.ReplayResult{}, ctx.Err()
		case envelope, open := <-subscribed.Subscription.Backfill():
			if !open {
				if err := subscribed.Subscription.Err(); err != nil {
					return eventstream.ReplayResult{}, err
				}
				return out, nil
			}
			if !consume(envelope) {
				return out, nil
			}
		}
	}
}

func (d *Adapter) RunState(ctx context.Context) (eventstream.RunState, error) {
	activeSession, ok := d.currentSession()
	if !ok {
		return eventstream.RunState{Status: eventstream.RunStateIdle}, nil
	}
	gw, err := d.gatewayControlPlane()
	if err != nil {
		return eventstream.RunState{}, err
	}
	state, err := gw.ControlPlaneState(ctx, gateway.ControlPlaneStateRequest{
		SessionRef: activeSession.SessionRef,
		BindingKey: d.bindingKey,
	})
	if err != nil {
		return eventstream.RunState{}, err
	}
	var active gateway.ActiveTurnState
	var hasActive bool
	if turns, err := d.gatewayTurns(); err == nil && turns != nil {
		active, hasActive = activeTurnStateForSession(turns.ActiveTurns(), activeSession.SessionRef)
	}
	return protocolRunStateFromGateway(state, active, hasActive), nil
}

func (d *Adapter) protocolSessionRef(sessionID string) (session.SessionRef, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return session.SessionRef{}, fmt.Errorf("app/gatewayapp/controladapter: session id is required")
	}
	return session.SessionRef{
		AppName: d.stack.Session.AppName, UserID: d.stack.Session.UserID, SessionID: sessionID,
	}, nil
}

func protocolSessionSummary(summary session.SessionSummary) schema.SessionSummary {
	updatedAt := ""
	if !summary.UpdatedAt.IsZero() {
		updatedAt = summary.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return schema.SessionSummary{
		SessionID: strings.TrimSpace(summary.SessionID),
		CWD:       strings.TrimSpace(summary.CWD),
		Title:     strings.TrimSpace(summary.Title),
		UpdatedAt: updatedAt,
	}
}

func protocolRunStateFromGateway(state gateway.ControlPlaneState, active gateway.ActiveTurnState, hasActive bool) eventstream.RunState {
	hasActiveTurn := hasActive || state.HasActiveTurn
	status, waitingApproval := eventstream.NormalizeRunStatus(string(state.RunState.Status), state.RunState.WaitingApproval, hasActiveTurn)
	out := eventstream.RunState{
		SessionID:       strings.TrimSpace(state.SessionRef.SessionID),
		RunID:           strings.TrimSpace(firstNonEmpty(state.RunState.ActiveRunID, active.RunID)),
		TurnID:          strings.TrimSpace(active.TurnID),
		HandleID:        strings.TrimSpace(active.HandleID),
		ActiveTurnKind:  strings.TrimSpace(string(active.Kind)),
		Status:          status,
		HasActiveTurn:   hasActiveTurn,
		WaitingApproval: waitingApproval,
		LastError:       strings.TrimSpace(state.RunState.LastError),
		StartedAt:       active.StartedAt,
		UpdatedAt:       state.RunState.UpdatedAt,
	}
	return out
}
