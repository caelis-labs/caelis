package controladapter

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/kernel"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/semantic"
)

type gatewaySessionBinder interface {
	BindSession(context.Context, kernel.BindSessionRequest) error
}

// ResumeSession stages target resolution and Control bootstrap before changing
// either the gateway binding or the Adapter's current Session.
func (d *Adapter) ResumeSession(ctx context.Context, sessionID string) (SessionSnapshot, error) {
	if d == nil || d.stack == nil {
		return SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: stack is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	d.sessionChangeMu.Lock()
	defer d.sessionChangeMu.Unlock()
	gw, err := d.gatewaySessions()
	if err != nil {
		return SessionSnapshot{}, err
	}
	target, err := gw.ResumeSession(ctx, kernel.ResumeSessionRequest{
		AppName: d.stack.Session.AppName, UserID: d.stack.Session.UserID,
		SessionID: strings.TrimSpace(sessionID), MetadataOnly: true,
		// Empty BindingKey makes target resolution read-only. The binding is
		// committed only after reconnect state and continuation are ready.
		BindingKey: "",
	})
	if err != nil {
		return SessionSnapshot{}, err
	}
	if d.stack.ControlReconnect == nil {
		return SessionSnapshot{}, missingRuntimeDependency("control client reconnect")
	}
	bootstrapped, err := d.stack.ControlReconnect.Reconnect(ctx, controlclient.ReconnectRequest{
		SessionID: target.Session.SessionID,
	})
	if err != nil {
		return SessionSnapshot{}, err
	}
	if bootstrapped.Subscription == nil {
		return SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: reconnect returned no continuation")
	}
	reconnect := &sessionReconnect{
		state: bootstrapped.State, subscription: bootstrapped.Subscription,
		turns: d.stack.Gateway.TurnServiceFn, ref: target.Session.SessionRef, bindingKey: d.bindingKey,
	}
	if err := reconnect.prepareBootstrapEvents(); err != nil {
		_ = reconnect.Close()
		return SessionSnapshot{}, err
	}
	abort := true
	defer func() {
		if abort {
			_ = reconnect.Close()
		}
	}()
	if strings.TrimSpace(bootstrapped.State.SessionID) != strings.TrimSpace(target.Session.SessionID) {
		return SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: reconnect state belongs to another session")
	}
	binder, ok := gw.(gatewaySessionBinder)
	if !ok {
		return SessionSnapshot{}, missingRuntimeDependency("gateway session binding")
	}
	if err := binder.BindSession(ctx, kernel.BindSessionRequest{
		SessionRef: target.Session.SessionRef,
		BindingKey: d.bindingKey,
		Binding:    kernel.BindingDescriptor{Surface: d.bindingKey, Owner: d.stack.Session.AppName},
	}); err != nil {
		return SessionSnapshot{}, err
	}
	d.mu.Lock()
	d.session = session.CloneSession(target.Session)
	d.hasSession = true
	d.mu.Unlock()
	d.refreshSessionDisplay(ctx, target.Session)
	abort = false
	return SessionSnapshot{SessionID: target.Session.SessionID, Reconnect: reconnect}, nil
}

type sessionReconnect struct {
	state        controlclient.SessionState
	subscription controlclient.FeedSubscription
	turns        func() GatewayTurnService
	ref          session.SessionRef
	bindingKey   string
	bootstrap    []eventstream.Envelope
	closeOnce    sync.Once
}

var _ control.SessionReconnect = (*sessionReconnect)(nil)

func (r *sessionReconnect) State() controlclient.SessionState {
	if r == nil {
		return controlclient.SessionState{}
	}
	return cloneReconnectState(r.state)
}

func (r *sessionReconnect) HandleID() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.state.Run.HandleID)
}

func (r *sessionReconnect) RunID() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.state.Run.RunID)
}

func (r *sessionReconnect) TurnID() string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.state.Run.TurnID)
}

func (r *sessionReconnect) Backfill() <-chan eventstream.Envelope {
	if r == nil || r.subscription == nil {
		return closedEnvelopeChannel()
	}
	return r.subscription.Backfill()
}

func (r *sessionReconnect) BackfillDone() <-chan struct{} {
	if r == nil || r.subscription == nil {
		done := make(chan struct{})
		close(done)
		return done
	}
	return r.subscription.BackfillDone()
}

func (r *sessionReconnect) Events() <-chan eventstream.Envelope {
	if r == nil || r.subscription == nil {
		return closedEnvelopeChannel()
	}
	return r.subscription.Events()
}

func (r *sessionReconnect) Err() error {
	if r == nil || r.subscription == nil {
		return nil
	}
	return r.subscription.Err()
}

func (r *sessionReconnect) BootstrapEvents() []eventstream.Envelope {
	if r == nil {
		return nil
	}
	return eventstream.CloneEnvelopes(r.bootstrap)
}

func (r *sessionReconnect) prepareBootstrapEvents() error {
	if r == nil || r.state.Approval.Active == nil {
		return nil
	}
	active := r.state.Approval.Active
	if strings.TrimSpace(string(active.RequestID)) == "" {
		return errors.New("app/gatewayapp/controladapter: active approval bootstrap has no request ID")
	}
	if active.Permission == nil {
		return errors.New("app/gatewayapp/controladapter: active approval bootstrap has no permission payload")
	}
	permission := session.CloneProtocolApproval(*active.Permission)
	wirePermission, err := semantic.EncodePermissionRequest(session.SessionRef{SessionID: r.state.SessionID}, &permission, nil)
	if err != nil {
		return err
	}
	handleID := firstNonEmpty(strings.TrimSpace(r.state.Run.HandleID))
	runID := firstNonEmpty(strings.TrimSpace(r.state.Run.RunID))
	turnID := firstNonEmpty(strings.TrimSpace(r.state.Run.TurnID))
	r.bootstrap = []eventstream.Envelope{{
		Kind: eventstream.KindRequestPermission, SessionID: r.state.SessionID,
		HandleID: handleID, RunID: runID, TurnID: turnID,
		Scope: active.Scope, ScopeID: active.ScopeID, ParticipantID: active.ParticipantID,
		ParentTool:        cloneReconnectParentTool(active.ParentTool),
		ApprovalRequestID: active.RequestID, Permission: &wirePermission,
	}}
	return nil
}

func (r *sessionReconnect) SubmitApproval(ctx context.Context, decision ApprovalDecision) error {
	turns, err := r.gatewayTurns()
	if err != nil {
		return err
	}
	return turns.SubmitActiveTurn(ctx, kernel.SubmitActiveTurnRequest{
		SessionRef: r.ref, Kind: kernel.SubmissionKindApproval,
		Approval: &kernel.ApprovalDecision{
			RequestID: decision.RequestID, Outcome: strings.TrimSpace(decision.Outcome),
			OptionID: strings.TrimSpace(decision.OptionID), Approved: decision.Approved,
			Reason: strings.TrimSpace(decision.Reason), ReviewText: strings.TrimSpace(decision.ReviewText),
		},
	})
}

func (r *sessionReconnect) Cancel() {
	turns, err := r.gatewayTurns()
	if err != nil {
		return
	}
	_ = turns.Interrupt(context.Background(), kernel.InterruptRequest{
		SessionRef: r.ref, BindingKey: r.bindingKey, Reason: "reconnected surface interrupt",
	})
}

func (r *sessionReconnect) Close() error {
	if r == nil || r.subscription == nil {
		return nil
	}
	var err error
	r.closeOnce.Do(func() { err = r.subscription.Close() })
	return err
}

func (r *sessionReconnect) gatewayTurns() (GatewayTurnService, error) {
	if r == nil || r.turns == nil {
		return nil, missingRuntimeDependency("gateway turn service")
	}
	turns := r.turns()
	if turns == nil {
		return nil, missingRuntimeDependency("gateway turn service")
	}
	return turns, nil
}

func cloneReconnectState(in controlclient.SessionState) controlclient.SessionState {
	out := in
	out.Metadata = session.CloneState(in.Metadata)
	out.BoundaryPosition = eventstream.CloneFeedPosition(in.BoundaryPosition)
	out.Participants = append([]session.ParticipantBinding(nil), in.Participants...)
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

func closedEnvelopeChannel() <-chan eventstream.Envelope {
	closed := make(chan eventstream.Envelope)
	close(closed)
	return closed
}

func cloneReconnectParentTool(in *eventstream.ParentToolRelation) *eventstream.ParentToolRelation {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
