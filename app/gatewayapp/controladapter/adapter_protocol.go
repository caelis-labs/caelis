package controladapter

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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
		AppName:      d.stack.Session.AppName,
		UserID:       d.stack.Session.UserID,
		WorkspaceKey: firstNonEmpty(strings.TrimSpace(req.CWD), d.stack.Session.Workspace.Key),
		Cursor:       strings.TrimSpace(req.Cursor),
		Limit:        clientProtocolSessionListLimit,
	})
	if err != nil {
		return schema.SessionListResponse{}, err
	}
	out := schema.SessionListResponse{
		Sessions:   make([]schema.SessionSummary, 0, len(result.Sessions)),
		NextCursor: strings.TrimSpace(result.NextCursor),
	}
	for _, summary := range result.Sessions {
		out.Sessions = append(out.Sessions, protocolSessionSummary(summary))
	}
	return out, nil
}

func (d *Adapter) Replay(ctx context.Context, req eventstream.ReplayRequest) (eventstream.ReplayResult, error) {
	ref, err := d.protocolSessionRef(req.SessionID)
	if err != nil {
		return eventstream.ReplayResult{}, err
	}
	gw, err := d.gatewaySessions()
	if err != nil {
		return eventstream.ReplayResult{}, err
	}
	result, err := gw.ReplayEvents(ctx, gateway.ReplayEventsRequest{
		SessionRef:       ref,
		BindingKey:       d.bindingKey,
		Cursor:           strings.TrimSpace(req.Cursor),
		Limit:            req.Limit,
		IncludeTransient: req.IncludeTransient,
	})
	if err != nil {
		return eventstream.ReplayResult{}, err
	}
	var active gateway.ActiveTurnState
	var hasActive bool
	if turns, err := d.gatewayTurns(); err == nil && turns != nil {
		active, hasActive = activeTurnStateForSession(turns.ActiveTurns(), result.SessionRef)
	}
	runState := protocolRunStateFromGateway(result.ControlPlane, active, hasActive)
	return eventstream.ReplayResult{
		SessionID:     strings.TrimSpace(result.SessionRef.SessionID),
		Events:        eventstream.CloneEnvelopes(result.Events),
		NextCursor:    strings.TrimSpace(result.NextCursor),
		Durable:       result.Durable,
		HasLiveHandle: result.HasLiveHandle,
		RunState:      runState,
	}, nil
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
	if sessionID != "" {
		return session.SessionRef{
			AppName:      d.stack.Session.AppName,
			UserID:       d.stack.Session.UserID,
			WorkspaceKey: d.stack.Session.Workspace.Key,
			SessionID:    sessionID,
		}, nil
	}
	activeSession, ok := d.currentSession()
	if !ok {
		return session.SessionRef{}, fmt.Errorf("app/gatewayapp/controladapter: no active session")
	}
	return activeSession.SessionRef, nil
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
