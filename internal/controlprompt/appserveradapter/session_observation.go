package appserveradapter

import (
	"context"
	"errors"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
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
		if env.HandleID != r.state.Run.HandleID || env.RunID != r.state.Run.RunID || env.TurnID != r.state.Run.TurnID {
			// The feed carries no controller binding. Invalidate the previous
			// Turn's binding until a matching inspected snapshot supplies it.
			r.state.Controller = session.ControllerBinding{}
		}
		r.state.Run.Active = true
		r.state.Run.HandleID, r.state.Run.RunID, r.state.Run.TurnID = env.HandleID, env.RunID, env.TurnID
		r.state.Run.StartedAt = env.OccurredAt
	} else if eventstream.IsTurnTerminalLifecycle(env) && env.RunID == r.state.Run.RunID && env.TurnID == r.state.Run.TurnID {
		r.state.Run.Active = false
	}
}

// observedControllerEpoch pins the controller for a newly observed Turn on its
// first write. An already pinned request keeps its epoch, even across a handoff.
// Inspection must never lend a newer Turn's authority to an older input target.
func (r *clientSessionReconnect) observedControllerEpoch(observed, inspected appserver.SessionState) (string, error) {
	if epoch := strings.TrimSpace(observed.Controller.EpochID); epoch != "" {
		return epoch, nil
	}
	if inspected.SessionID != observed.SessionID || reconnectTarget(inspected) != reconnectTarget(observed) ||
		strings.TrimSpace(inspected.Controller.EpochID) == "" {
		return "", errors.New("app/gatewayapp/controladapter: observed Turn controller is unavailable")
	}
	r.stateMu.Lock()
	defer r.stateMu.Unlock()
	if r.state.SessionID != observed.SessionID || reconnectTarget(r.state) != reconnectTarget(observed) {
		return "", noActiveTurnSubmissionError()
	}
	if strings.TrimSpace(r.state.Controller.EpochID) == "" {
		r.state.Controller = inspected.Controller
	}
	return strings.TrimSpace(r.state.Controller.EpochID), nil
}
