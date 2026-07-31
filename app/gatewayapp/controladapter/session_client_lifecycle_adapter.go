package controladapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

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
	a.closeActiveTurn()
	a.setClientSession(result.State.SessionID, result.State.CWD)
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
	if active := a.activeTurn(); active != nil {
		_ = active.Close()
	}
}

type clientSessionReconnect struct {
	state        controlclient.SessionState
	subscription controlclient.FeedSubscription
	client       controlclient.SessionClient
	bootstrap    []eventstream.Envelope
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
	state, err := r.client.InspectSession(ctx, controlclient.StateRequest{SessionID: r.state.SessionID})
	if err != nil {
		return err
	}
	revision := state.Revision
	_, err = r.client.ResolveApproval(ctx, controlclient.ResolveApprovalRequest{
		WriteBase: controlclient.WriteBase{
			OperationID: "reconnect-approval-" + uuid.NewString(), SessionID: state.SessionID,
			ExpectedRevision: &revision, ExpectedControllerEpoch: state.Controller.EpochID,
		},
		Target:            controlclient.TurnTarget{HandleID: state.Run.HandleID, RunID: state.Run.RunID, TurnID: state.Run.TurnID},
		ApprovalRequestID: string(decision.RequestID), Outcome: strings.TrimSpace(decision.Outcome),
		OptionID: strings.TrimSpace(decision.OptionID), Approved: decision.Approved,
		Reason: strings.TrimSpace(decision.Reason), ReviewText: strings.TrimSpace(decision.ReviewText),
	})
	return err
}

func (r *clientSessionReconnect) Cancel() {
	if r == nil || r.client == nil || !r.state.Run.Active {
		return
	}
	state, err := r.client.InspectSession(context.Background(), controlclient.StateRequest{SessionID: r.state.SessionID})
	if err != nil || !state.Run.Active {
		return
	}
	revision := state.Revision
	_, _ = r.client.Cancel(context.Background(), controlclient.CancelRequest{
		WriteBase: controlclient.WriteBase{
			OperationID: "reconnect-cancel-" + uuid.NewString(), SessionID: state.SessionID,
			ExpectedRevision: &revision, ExpectedControllerEpoch: state.Controller.EpochID,
		},
		Target: controlclient.TurnTarget{HandleID: state.Run.HandleID, RunID: state.Run.RunID, TurnID: state.Run.TurnID},
		Reason: "reconnected surface interrupt",
	})
}

func (r *clientSessionReconnect) Close() error {
	if r == nil || r.subscription == nil {
		return nil
	}
	var err error
	r.closeOnce.Do(func() { err = r.subscription.Close() })
	return err
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
