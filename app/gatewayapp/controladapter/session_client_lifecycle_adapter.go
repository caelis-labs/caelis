package controladapter

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
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/semantic"
)

// NewSession creates a durable Session through the typed AppServer lifecycle.
func (a *SessionClientAdapter) NewSession(ctx context.Context) (controlprompt.SessionSnapshot, error) {
	if a == nil || a.sessionClient == nil {
		return controlprompt.SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	a.sessionChangeMu.Lock()
	defer a.sessionChangeMu.Unlock()
	result, err := a.sessionClient.CreateSession(ctx, controlclient.CreateSessionRequest{
		WriteBase:    controlclient.WriteBase{OperationID: "session-new-" + uuid.NewString()},
		WorkspaceKey: strings.TrimSpace(a.workspaceKey),
		CWD:          strings.TrimSpace(a.workspaceDir),
		Metadata:     map[string]any{"surface": strings.TrimSpace(a.surface)},
	})
	if err != nil {
		return controlprompt.SessionSnapshot{}, err
	}
	state, err := a.sessionClient.InspectSession(ctx, controlclient.StateRequest{SessionID: result.SessionID})
	if err != nil {
		return controlprompt.SessionSnapshot{}, err
	}
	a.closeActiveTurn()
	a.setClientSession(state.SessionID, state.CWD)
	return controlprompt.SessionSnapshot{SessionID: state.SessionID}, nil
}

// ResumeSession atomically bootstraps transcript replay and live continuation
// through the typed Session client before changing the adapter's active ID.
func (a *SessionClientAdapter) ResumeSession(ctx context.Context, sessionID string) (controlprompt.SessionSnapshot, error) {
	if a == nil || a.sessionClient == nil {
		return controlprompt.SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	a.sessionChangeMu.Lock()
	defer a.sessionChangeMu.Unlock()
	result, err := a.sessionClient.Reconnect(ctx, controlclient.ReconnectRequest{SessionID: strings.TrimSpace(sessionID)})
	if err != nil {
		return controlprompt.SessionSnapshot{}, err
	}
	if result.Subscription == nil {
		return controlprompt.SessionSnapshot{}, errors.New("app/gatewayapp/controladapter: reconnect returned no continuation")
	}
	reconnect := &clientSessionReconnect{state: result.State, subscription: result.Subscription, client: a.sessionClient}
	if err := reconnect.prepareBootstrapEvents(); err != nil {
		_ = reconnect.Close()
		return controlprompt.SessionSnapshot{}, err
	}
	abort := true
	defer func() {
		if abort {
			_ = reconnect.Close()
		}
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
	a.setClientSession(result.State.SessionID, result.State.CWD)
	if registerActive {
		reconnect.onClose = func() { a.clearActiveReconnect(reconnect) }
		a.setActiveReconnect(reconnect)
	}
	abort = false
	return controlprompt.SessionSnapshot{SessionID: result.State.SessionID, Reconnect: reconnect}, nil
}

// ListSessions reads the authorized workspace-scoped Session directory through
// AppServer. Presentation-only relative age formatting stays in this adapter.
func (a *SessionClientAdapter) ListSessions(ctx context.Context, limit int) ([]controlprompt.ResumeCandidate, error) {
	if a == nil || a.sessionClient == nil {
		return nil, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	result, err := a.sessionClient.ListSessions(ctx, controlclient.ListSessionsRequest{
		WorkspaceKey: strings.TrimSpace(a.workspaceKey), Limit: normalizeCompletionLimit(limit),
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
	state        controlclient.SessionState
	subscription controlclient.FeedSubscription
	client       controlclient.SessionClient
	bootstrap    []eventstream.Envelope
	onClose      func()
	closeOnce    sync.Once
}

func (r *clientSessionReconnect) State() controlclient.SessionState {
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
	wirePermission, err := semantic.EncodePermissionRequest(session.SessionRef{SessionID: r.state.SessionID}, &permission, nil)
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
	_, err = r.client.ResolveApproval(ctx, controlclient.ResolveApprovalRequest{
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
		return noActiveTurnSubmissionError()
	}
	base, err := r.writeBase(ctx, "reconnect-steer")
	if err != nil {
		return err
	}
	_, err = r.client.Steer(ctx, controlclient.SteerRequest{
		WriteBase: base, Target: r.target(), Input: input, DisplayInput: displayInput,
		ContentParts: append([]model.ContentPart(nil), contentParts...),
	})
	return err
}

func (r *clientSessionReconnect) cancel(ctx context.Context, reason string) error {
	if r == nil || r.client == nil || !r.state.Run.Active {
		return noActiveTurnSubmissionError()
	}
	base, err := r.writeBase(ctx, "reconnect-cancel")
	if err != nil {
		return err
	}
	_, err = r.client.Cancel(ctx, controlclient.CancelRequest{
		WriteBase: base, Target: r.target(), Reason: strings.TrimSpace(reason),
	})
	return err
}

func (r *clientSessionReconnect) writeBase(ctx context.Context, prefix string) (controlclient.WriteBase, error) {
	if r == nil || r.client == nil {
		return controlclient.WriteBase{}, errors.New("app/gatewayapp/controladapter: reconnect client is unavailable")
	}
	state, err := r.client.InspectSession(ctx, controlclient.StateRequest{SessionID: r.state.SessionID})
	if err != nil {
		return controlclient.WriteBase{}, err
	}
	revision := state.Revision
	return controlclient.WriteBase{
		OperationID: prefix + "-" + uuid.NewString(), SessionID: r.state.SessionID,
		ExpectedRevision: &revision, ExpectedControllerEpoch: strings.TrimSpace(r.state.Controller.EpochID),
	}, nil
}

func (r *clientSessionReconnect) target() controlclient.TurnTarget {
	if r == nil {
		return controlclient.TurnTarget{}
	}
	return controlclient.TurnTarget{
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

func cloneReconnectState(in controlclient.SessionState) controlclient.SessionState {
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
