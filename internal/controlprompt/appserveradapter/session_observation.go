package appserveradapter

import (
	"context"
	"strings"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

// SessionID returns this presentation's selected durable Session address.
func (a *SessionClientAdapter) SessionID() string { return a.clientSessionID() }

// SessionRunning reads live activity without acquiring execution authority.
func (a *SessionClientAdapter) SessionRunning(ctx context.Context, sessionID string) (bool, error) {
	state, err := a.sessionClient.InspectSession(ctx, appserver.StateRequest{SessionID: sessionID})
	return state.Run.Active || state.Approval.Active != nil, err
}

// ObserveEvent advances the selected view's exact input target from the same
// live feed that drives its display. Replayed history must not call this method.
func (r *clientSessionReconnect) ObserveEvent(env eventstream.Envelope) {
	if r == nil || env.Kind != eventstream.KindLifecycle || env.Lifecycle == nil ||
		(env.Scope != "" && env.Scope != eventstream.ScopeMain) || env.ApprovalRequestID != "" {
		return
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if env.SessionID != "" && env.SessionID != r.state.SessionID {
		return
	}
	if env.Lifecycle.State == eventstream.LifecycleStateRunning {
		if strings.TrimSpace(env.HandleID) == "" || strings.TrimSpace(env.RunID) == "" || strings.TrimSpace(env.TurnID) == "" {
			return
		}
		r.state.Run.Active = true
		r.state.Run.HandleID, r.state.Run.RunID, r.state.Run.TurnID = env.HandleID, env.RunID, env.TurnID
		r.state.Run.StartedAt = env.OccurredAt
	} else if eventstream.IsTurnTerminalLifecycle(env) && env.RunID == r.state.Run.RunID && env.TurnID == r.state.Run.TurnID {
		r.state.Run.Active = false
	}
}
