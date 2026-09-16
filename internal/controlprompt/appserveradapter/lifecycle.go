package appserveradapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/acppermission"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// ResetSession clears the presentation facade's selected Session. A durable
// Session is created only when a subsequent work-bearing request needs one.
func (a *SessionClientAdapter) ResetSession(ctx context.Context) error {
	if a == nil || a.sessionClient == nil {
		return errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	a.sessionChangeMu.Lock()
	defer a.sessionChangeMu.Unlock()
	// Reset changes only this presentation's selected Session. Closing the
	// active view detaches its feed; it does not cancel durable Host work.
	a.closeActiveTurn()
	a.preferredID = ""
	a.setClientSession("", session.WorkspaceRef{})
	return nil
}

// ResumeSession atomically bootstraps transcript replay and live continuation
// through the typed Session client before changing the adapter's active ID.
func (a *SessionClientAdapter) ResumeSession(ctx context.Context, sessionID string) (controlprompt.SessionSnapshot, error) {
	if a == nil || a.sessionClient == nil {
		return controlprompt.SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	a.sessionChangeMu.Lock()
	defer a.sessionChangeMu.Unlock()
	if ctx == nil {
		ctx = context.Background()
	}
	feedCtx, cancelFeed := context.WithCancel(context.WithoutCancel(ctx))
	stopAdmission := context.AfterFunc(ctx, cancelFeed)
	historyTurns := 0
	if a.surface == "cli-tui" {
		historyTurns = 16
	}
	result, err := a.sessionClient.Reconnect(feedCtx, appserver.ReconnectRequest{SessionID: strings.TrimSpace(sessionID), HistoryTurns: historyTurns})
	stopAdmission()
	if err != nil || ctx.Err() != nil {
		cancelFeed()
		if result.Subscription != nil {
			_ = result.Subscription.Close()
		}
		if err == nil {
			err = ctx.Err()
		}
		return controlprompt.SessionSnapshot{}, err
	}
	if result.Subscription == nil {
		cancelFeed()
		return controlprompt.SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: reconnect returned no continuation")
	}
	if err := a.validateTUISessionController(result.State); err != nil {
		cancelFeed()
		_ = result.Subscription.Close()
		return controlprompt.SessionSnapshot{}, err
	}
	reconnect := &clientSessionReconnect{state: result.State, subscription: result.Subscription, client: a.sessionClient, stopFeed: cancelFeed}
	if err := reconnect.prepareBootstrapEvents(); err != nil {
		_ = reconnect.Close()
		return controlprompt.SessionSnapshot{}, err
	}
	abort := true
	defer func() {
		if !abort {
			return
		}
		_ = reconnect.Close()
	}()
	if strings.TrimSpace(result.State.SessionID) != strings.TrimSpace(sessionID) {
		return controlprompt.SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: reconnect state belongs to another Session")
	}
	registerActive := result.State.Run.Active || result.State.Approval.Active != nil
	if registerActive {
		target := reconnect.target()
		if target.HandleID == "" || target.RunID == "" || target.TurnID == "" {
			return controlprompt.SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: active reconnect returned no complete Turn target")
		}
	}
	a.closeActiveTurn()
	a.setClientSession(result.State.SessionID, sessionWorkspaceAddress(result.State))
	reconnect.onClose = func() { a.clearActiveReconnect(reconnect) }
	a.setActiveReconnect(reconnect)
	abort = false
	return controlprompt.SessionSnapshot{SessionID: result.State.SessionID, Reconnect: reconnect}, nil
}

// Close releases presentation-owned Turn and Session observation references.
// It never cancels Host work solely because the presentation exits.
func (a *SessionClientAdapter) Close() error {
	if a == nil {
		return nil
	}
	a.sessionChangeMu.Lock()
	defer a.sessionChangeMu.Unlock()
	a.cancelTurnAdmissions(nil, "")
	a.closeActiveTurn()
	return nil
}

func (a *SessionClientAdapter) validateTUISessionController(state appserver.SessionState) error {
	// ACP-backed main controllers use the same AppServer replay and live-feed
	// contract as kernel sessions. Controller execution remains behind Control;
	// the TUI only projects its envelopes and submits typed input.
	return nil
}

// ListSessions reads the authorized workspace-scoped Session directory through
// AppServer. Presentation-only relative age formatting stays in this adapter.
func (a *SessionClientAdapter) ListSessions(ctx context.Context, limit int) ([]controlprompt.ResumeCandidate, error) {
	if a == nil || a.sessionClient == nil {
		return nil, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	result, err := a.sessionClient.ListSessions(ctx, appserver.ListSessionsRequest{
		CWD: strings.TrimSpace(a.WorkspaceDir()), Limit: normalizeCompletionLimit(limit),
	})
	if err != nil {
		return nil, err
	}
	out := make([]controlprompt.ResumeCandidate, 0, len(result.Sessions))
	for _, item := range result.Sessions {
		out = append(out, controlprompt.ResumeCandidate{
			SessionID: item.SessionID,
			Title:     strings.TrimSpace(item.Title),
			Workspace: strings.TrimSpace(item.CWD),
			Age:       formatResumeAge(item.UpdatedAt, time.Now()),
			UpdatedAt: item.UpdatedAt,
		})
	}
	return out, nil
}

func (a *SessionClientAdapter) closeActiveTurn() {
	a.activeMu.Lock()
	active := a.active
	reconnect := a.reconnect
	a.active = nil
	a.reconnect = nil
	a.activeMu.Unlock()
	if active != nil {
		_ = active.Close()
	}
	if reconnect != nil {
		_ = reconnect.Close()
	}
}

type clientSessionReconnect struct {
	stateMu      sync.RWMutex
	stopFeed     context.CancelFunc
	state        appserver.SessionState
	subscription appserver.FeedSubscription
	client       appserver.SessionClient
	bootstrap    []eventstream.Envelope
	onClose      func()
	closeOnce    sync.Once
}

func (r *clientSessionReconnect) State() appserver.SessionState {
	r.stateMu.RLock()
	defer r.stateMu.RUnlock()
	return cloneReconnectState(r.state)
}
func (r *clientSessionReconnect) HandleID() string { return r.target().HandleID }
func (r *clientSessionReconnect) RunID() string    { return r.target().RunID }
func (r *clientSessionReconnect) TurnID() string   { return r.target().TurnID }
func (r *clientSessionReconnect) Deliveries() <-chan appserver.FeedDelivery {
	if r == nil || r.subscription == nil {
		closed := make(chan appserver.FeedDelivery)
		close(closed)
		return closed
	}
	return r.subscription.Deliveries()
}
func (r *clientSessionReconnect) Err() error {
	if r == nil || r.subscription == nil {
		return nil
	}
	return r.subscription.Err()
}
func (r *clientSessionReconnect) BootstrapEvents() []eventstream.Envelope {
	if r == nil {
		return nil
	}
	return eventstream.CloneEnvelopes(r.bootstrap)
}

func (r *clientSessionReconnect) prepareBootstrapEvents() error {
	if r == nil || r.state.Approval.Active == nil {
		return nil
	}
	active := r.state.Approval.Active
	if strings.TrimSpace(string(active.RequestID)) == "" || active.Permission == nil {
		return errors.New("app/gatewayapp/controladapter: active approval bootstrap is incomplete")
	}
	permission := session.CloneProtocolApproval(*active.Permission)
	wirePermission, err := acppermission.EncodePermissionRequest(session.SessionRef{SessionID: r.state.SessionID}, &permission, nil)
	if err != nil {
		return err
	}
	r.bootstrap = []eventstream.Envelope{{
		Kind: eventstream.KindRequestPermission, SessionID: r.state.SessionID,
		HandleID: strings.TrimSpace(r.state.Run.HandleID), RunID: strings.TrimSpace(r.state.Run.RunID), TurnID: strings.TrimSpace(r.state.Run.TurnID),
		Scope: active.Scope, ScopeID: active.ScopeID, ParticipantID: active.ParticipantID,
		ParentTool: cloneReconnectParentTool(active.ParentTool), ApprovalRequestID: active.RequestID, Permission: &wirePermission,
	}}
	return nil
}

func (r *clientSessionReconnect) SubmitApproval(ctx context.Context, decision controlprompt.ApprovalDecision) error {
	if r == nil || r.client == nil {
		return errors.New("app/gatewayapp/controladapter: reconnect client is unavailable")
	}
	state := r.State()
	base, err := r.writeBase(ctx, "reconnect-approval", state)
	if err != nil {
		return err
	}
	_, err = r.client.ResolveApproval(ctx, appserver.ResolveApprovalRequest{
		WriteBase:         base,
		Target:            reconnectTarget(state),
		ApprovalRequestID: string(decision.RequestID), Outcome: strings.TrimSpace(decision.Outcome),
		OptionID: strings.TrimSpace(decision.OptionID), Approved: decision.Approved,
		Reason: strings.TrimSpace(decision.Reason), ReviewText: strings.TrimSpace(decision.ReviewText),
	})
	return err
}

func (r *clientSessionReconnect) Cancel() {
	_ = r.cancel(context.Background(), "reconnected surface interrupt")
}

func (r *clientSessionReconnect) steer(ctx context.Context, input, displayInput string, contentParts []model.ContentPart) error {
	if r == nil || r.client == nil {
		return appserver.NewOutcomeError(appserver.OutcomeRejected, noActiveTurnSubmissionError())
	}
	state := r.State()
	if !state.Run.Active {
		return appserver.NewOutcomeError(appserver.OutcomeRejected, noActiveTurnSubmissionError())
	}
	base, err := r.writeBase(ctx, "reconnect-steer", state)
	if err != nil {
		return appserver.NewOutcomeError(appserver.OutcomeRejected, err)
	}
	result, err := r.client.Steer(ctx, appserver.SteerRequest{
		WriteBase: base, Target: reconnectTarget(state), Input: input, DisplayInput: displayInput,
		ContentParts: append([]model.ContentPart(nil), contentParts...),
	})
	return appserver.CommandMutationError(result, err)
}

func (r *clientSessionReconnect) cancel(ctx context.Context, reason string) error {
	if r == nil || r.client == nil {
		return noActiveTurnSubmissionError()
	}
	state := r.State()
	if !state.Run.Active {
		return noActiveTurnSubmissionError()
	}
	base, err := r.writeBase(ctx, "reconnect-cancel", state)
	if err != nil {
		return err
	}
	_, err = r.client.Cancel(ctx, appserver.CancelRequest{
		WriteBase: base, Target: reconnectTarget(state), Reason: strings.TrimSpace(reason),
	})
	return err
}

func (r *clientSessionReconnect) writeBase(ctx context.Context, prefix string, observed appserver.SessionState) (appserver.WriteBase, error) {
	if r == nil || r.client == nil {
		return appserver.WriteBase{}, errors.New("app/gatewayapp/controladapter: reconnect client is unavailable")
	}
	state, err := r.client.InspectSession(ctx, appserver.StateRequest{SessionID: observed.SessionID})
	if err != nil {
		return appserver.WriteBase{}, err
	}
	epoch, err := r.observedControllerEpoch(observed, state)
	if err != nil {
		return appserver.WriteBase{}, err
	}
	revision := state.Revision
	return appserver.WriteBase{
		OperationID: prefix + "-" + uuid.NewString(), SessionID: observed.SessionID,
		ExpectedRevision: &revision, ExpectedControllerEpoch: epoch,
	}, nil
}

func (r *clientSessionReconnect) target() appserver.TurnTarget {
	if r == nil {
		return appserver.TurnTarget{}
	}
	return reconnectTarget(r.State())
}

func reconnectTarget(state appserver.SessionState) appserver.TurnTarget {
	return appserver.TurnTarget{
		HandleID: strings.TrimSpace(state.Run.HandleID),
		RunID:    strings.TrimSpace(state.Run.RunID),
		TurnID:   strings.TrimSpace(state.Run.TurnID),
	}
}

func (r *clientSessionReconnect) Close() error {
	if r == nil || r.subscription == nil {
		return nil
	}
	var err error
	r.closeOnce.Do(func() {
		if r.stopFeed != nil {
			r.stopFeed()
		}
		err = r.subscription.Close()
		if r.onClose != nil {
			r.onClose()
		}
	})
	return err
}

func cloneReconnectState(in appserver.SessionState) appserver.SessionState {
	out := in
	out.Metadata = session.CloneState(in.Metadata)
	out.BoundaryPosition = eventstream.CloneFeedPosition(in.BoundaryPosition)
	out.Participants = session.CloneParticipantBindings(in.Participants)
	if in.Approval.Active != nil {
		active := *in.Approval.Active
		active.ParentTool = cloneReconnectParentTool(in.Approval.Active.ParentTool)
		if in.Approval.Active.Permission != nil {
			permission := session.CloneProtocolApproval(*in.Approval.Active.Permission)
			active.Permission = &permission
		}
		out.Approval.Active = &active
	}
	return out
}

func cloneReconnectParentTool(in *eventstream.ParentToolRelation) *eventstream.ParentToolRelation {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

var _ controlprompt.SessionReconnect = (*clientSessionReconnect)(nil)

func formatResumeAge(updatedAt, now time.Time) string {
	if updatedAt.IsZero() {
		return ""
	}
	delta := now.Sub(updatedAt)
	if delta < 0 {
		delta = 0
	}
	switch {
	case delta < time.Minute:
		return "just now"
	case delta < time.Hour:
		return fmt.Sprintf("%dm ago", int(delta/time.Minute))
	case delta < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(delta/time.Hour))
	default:
		return fmt.Sprintf("%dd ago", int(delta/(24*time.Hour)))
	}
}
