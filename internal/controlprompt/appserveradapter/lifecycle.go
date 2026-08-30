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
	a.setClientSession("", "")
	a.replaceSessionPresence(nil)
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
	result, err := a.sessionClient.Reconnect(ctx, appserver.ReconnectRequest{SessionID: strings.TrimSpace(sessionID)})
	if err != nil {
		return controlprompt.SessionSnapshot{}, err
	}
	if result.Subscription == nil {
		return controlprompt.SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: reconnect returned no continuation")
	}
	if err := a.validateTUISessionController(result.State); err != nil {
		_ = result.Subscription.Close()
		return controlprompt.SessionSnapshot{}, err
	}
	reconnect := &clientSessionReconnect{state: result.State, subscription: result.Subscription, client: a.sessionClient}
	if err := reconnect.prepareBootstrapEvents(); err != nil {
		_ = reconnect.Close()
		return controlprompt.SessionSnapshot{}, err
	}
	var presence *sessionPresence
	abort := true
	defer func() {
		if !abort {
			return
		}
		_ = reconnect.Close()
		if presence != nil {
			_ = presence.Close()
		}
	}()
	if strings.TrimSpace(result.State.SessionID) != strings.TrimSpace(sessionID) {
		return controlprompt.SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: reconnect state belongs to another Session")
	}
	if a.tracksSessionPresence() {
		presence, err = a.openSessionPresence(result.State)
		if err != nil {
			return controlprompt.SessionSnapshot{}, err
		}
	}
	registerActive := result.State.Run.Active || result.State.Approval.Active != nil
	if registerActive {
		target := reconnect.target()
		if target.HandleID == "" || target.RunID == "" || target.TurnID == "" {
			return controlprompt.SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: active reconnect returned no complete Turn target")
		}
	}
	a.closeActiveTurn()
	a.setClientSession(result.State.SessionID, result.State.CWD)
	a.replaceSessionPresence(presence)
	if registerActive {
		reconnect.onClose = func() { a.clearActiveReconnect(reconnect) }
		a.setActiveReconnect(reconnect)
	}
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
	a.cancelTurnAdmissions(nil)
	a.closeActiveTurn()
	a.replaceSessionPresence(nil)
	return nil
}

type sessionPresence struct {
	subscription appserver.FeedSubscription
	cancel       context.CancelFunc
	done         chan struct{}
	once         sync.Once
}

func (a *SessionClientAdapter) openSessionPresence(
	state appserver.SessionState,
) (*sessionPresence, error) {
	if a == nil || a.sessionClient == nil {
		return nil, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	sessionID := strings.TrimSpace(state.SessionID)
	if sessionID == "" {
		return nil, session.ErrInvalidSession
	}
	presenceCtx, cancel := context.WithCancel(context.Background())
	result, err := a.sessionClient.Reconnect(presenceCtx, appserver.ReconnectRequest{
		SessionID: sessionID,
		Cursor:    strings.TrimSpace(state.BoundaryCursor),
	})
	if err != nil {
		cancel()
		return nil, err
	}
	if result.Subscription == nil {
		cancel()
		return nil, errors.New("app/gatewayapp/controladapter: Session presence returned no continuation")
	}
	if strings.TrimSpace(result.State.SessionID) != sessionID {
		_ = result.Subscription.Close()
		cancel()
		return nil, errors.New("app/gatewayapp/controladapter: Session presence belongs to another Session")
	}
	presence := &sessionPresence{
		subscription: result.Subscription,
		cancel:       cancel,
		done:         make(chan struct{}),
	}
	go presence.drain(presenceCtx)
	return presence, nil
}

func (a *SessionClientAdapter) replaceSessionPresence(next *sessionPresence) {
	if a == nil {
		if next != nil {
			_ = next.Close()
		}
		return
	}
	a.activeMu.Lock()
	previous := a.presence
	a.presence = next
	a.activeMu.Unlock()
	if previous != nil && previous != next {
		_ = previous.Close()
	}
}

func (p *sessionPresence) drain(ctx context.Context) {
	defer close(p.done)
	defer p.subscription.Close()
	for _, events := range []<-chan eventstream.Envelope{
		p.subscription.Backfill(),
		p.subscription.Events(),
	} {
		for events != nil {
			select {
			case <-ctx.Done():
				return
			case _, open := <-events:
				if !open {
					events = nil
				}
			}
		}
	}
}

func (p *sessionPresence) Close() error {
	if p == nil {
		return nil
	}
	var err error
	p.once.Do(func() {
		p.cancel()
		err = p.subscription.Close()
		<-p.done
	})
	return err
}

func (a *SessionClientAdapter) tracksSessionPresence() bool {
	return a != nil && strings.EqualFold(strings.TrimSpace(a.surface), "cli-tui")
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
	state        appserver.SessionState
	subscription appserver.FeedSubscription
	client       appserver.SessionClient
	bootstrap    []eventstream.Envelope
	onClose      func()
	closeOnce    sync.Once
}

func (r *clientSessionReconnect) State() appserver.SessionState {
	return cloneReconnectState(r.state)
}
func (r *clientSessionReconnect) HandleID() string { return strings.TrimSpace(r.state.Run.HandleID) }
func (r *clientSessionReconnect) RunID() string    { return strings.TrimSpace(r.state.Run.RunID) }
func (r *clientSessionReconnect) TurnID() string   { return strings.TrimSpace(r.state.Run.TurnID) }
func (r *clientSessionReconnect) Backfill() <-chan eventstream.Envelope {
	if r == nil || r.subscription == nil {
		return closedEnvelopeChannel()
	}
	return r.subscription.Backfill()
}
func (r *clientSessionReconnect) BackfillDone() <-chan struct{} {
	if r == nil || r.subscription == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return r.subscription.BackfillDone()
}
func (r *clientSessionReconnect) Events() <-chan eventstream.Envelope {
	if r == nil || r.subscription == nil {
		return closedEnvelopeChannel()
	}
	return r.subscription.Events()
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
	base, err := r.writeBase(ctx, "reconnect-approval")
	if err != nil {
		return err
	}
	_, err = r.client.ResolveApproval(ctx, appserver.ResolveApprovalRequest{
		WriteBase:         base,
		Target:            r.target(),
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
	if r == nil || r.client == nil || !r.state.Run.Active {
		return appserver.NewOutcomeError(appserver.OutcomeRejected, noActiveTurnSubmissionError())
	}
	base, err := r.writeBase(ctx, "reconnect-steer")
	if err != nil {
		return appserver.NewOutcomeError(appserver.OutcomeRejected, err)
	}
	result, err := r.client.Steer(ctx, appserver.SteerRequest{
		WriteBase: base, Target: r.target(), Input: input, DisplayInput: displayInput,
		ContentParts: append([]model.ContentPart(nil), contentParts...),
	})
	return appserver.CommandMutationError(result, err)
}

func (r *clientSessionReconnect) cancel(ctx context.Context, reason string) error {
	if r == nil || r.client == nil || !r.state.Run.Active {
		return noActiveTurnSubmissionError()
	}
	base, err := r.writeBase(ctx, "reconnect-cancel")
	if err != nil {
		return err
	}
	_, err = r.client.Cancel(ctx, appserver.CancelRequest{
		WriteBase: base, Target: r.target(), Reason: strings.TrimSpace(reason),
	})
	return err
}

func (r *clientSessionReconnect) writeBase(ctx context.Context, prefix string) (appserver.WriteBase, error) {
	if r == nil || r.client == nil {
		return appserver.WriteBase{}, errors.New("app/gatewayapp/controladapter: reconnect client is unavailable")
	}
	state, err := r.client.InspectSession(ctx, appserver.StateRequest{SessionID: r.state.SessionID})
	if err != nil {
		return appserver.WriteBase{}, err
	}
	revision := state.Revision
	return appserver.WriteBase{
		OperationID: prefix + "-" + uuid.NewString(), SessionID: r.state.SessionID,
		ExpectedRevision: &revision, ExpectedControllerEpoch: strings.TrimSpace(r.state.Controller.EpochID),
	}, nil
}

func (r *clientSessionReconnect) target() appserver.TurnTarget {
	if r == nil {
		return appserver.TurnTarget{}
	}
	return appserver.TurnTarget{
		HandleID: strings.TrimSpace(r.state.Run.HandleID),
		RunID:    strings.TrimSpace(r.state.Run.RunID),
		TurnID:   strings.TrimSpace(r.state.Run.TurnID),
	}
}

func (r *clientSessionReconnect) Close() error {
	if r == nil || r.subscription == nil {
		return nil
	}
	var err error
	r.closeOnce.Do(func() { err = r.subscription.Close() })
	if r.onClose != nil {
		r.onClose()
	}
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
