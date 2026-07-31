package controladapter

import (
	"context"
	"errors"
	"strings"
	"sync"

	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// SessionClientAdapter is the reversible TUI migration boundary for main-Turn
// ingress. It delegates slash/status/participant facets to Adapter while
// ordinary prompt, steer, approval, cancel, and observation use the common
// typed Session client.
//
// TODO(control-client-parity): remove the embedded Adapter only after Session
// lifecycle, status/config commands, and Side ACP participant operations all
// have narrow typed clients. Until then NewSession and ResumeSession retain
// the default-Stack ownership marker so private facets cannot target a
// different Runtime than the typed main Turn.
type SessionClientAdapter struct {
	*Adapter

	turns *controlclient.SessionTurnClient

	activeMu sync.Mutex
	active   *sessionClientTurn
}

// NewSessionClientAdapter wraps one already-bound private Adapter. The
// Adapter's existing Session binding remains the transitional source for
// private slash and Side ACP facets; accepted main Turns have only the typed
// Session client as their ingress and observation path.
func NewSessionClientAdapter(
	adapter *Adapter,
	client controlclient.SessionClient,
) (*SessionClientAdapter, error) {
	if adapter == nil {
		return nil, errors.New("app/gatewayapp/controladapter: Adapter is required")
	}
	turns, err := controlclient.NewSessionTurnClient(client)
	if err != nil {
		return nil, err
	}
	return &SessionClientAdapter{Adapter: adapter, turns: turns}, nil
}

func (a *SessionClientAdapter) Submit(
	ctx context.Context,
	submission controlprompt.Submission,
) (controlprompt.Turn, error) {
	if a == nil || a.Adapter == nil || a.turns == nil {
		return nil, errors.New("app/gatewayapp/controladapter: Session client adapter is unavailable")
	}
	activeSession, err := a.ensureSession(ctx)
	if err != nil {
		return nil, err
	}
	rawInput := strings.TrimSpace(submission.Text)
	displayInput := strings.TrimSpace(submission.DisplayText)
	if displayInput == rawInput {
		displayInput = ""
	}
	contentParts, err := contentPartsFromSubmission(
		rawInput,
		submission.Attachments,
		a.WorkspaceDir(),
	)
	if err != nil {
		return nil, err
	}
	if submission.Mode == controlprompt.SubmissionModeActiveTurn {
		active := a.activeTurn()
		if active == nil {
			return nil, noActiveTurnSubmissionError()
		}
		if err := active.turn.Steer(ctx, rawInput, displayInput, contentParts); err != nil {
			return nil, err
		}
		return nil, nil
	}
	turn, err := a.turns.Start(ctx, controlclient.SessionTurnStartRequest{
		SessionID:    activeSession.SessionID,
		Input:        rawInput,
		DisplayInput: displayInput,
		ContentParts: contentParts,
	})
	if err != nil {
		return nil, err
	}
	wrapped := &sessionClientTurn{turn: turn}
	wrapped.onClose = func() { a.clearActiveTurn(wrapped) }
	a.setActiveTurn(wrapped)
	return wrapped, nil
}

func (a *SessionClientAdapter) Interrupt(ctx context.Context) error {
	if a == nil || a.Adapter == nil {
		return errors.New("app/gatewayapp/controladapter: Session client adapter is unavailable")
	}
	cancelCommand := a.activeCommandInterrupt()
	if cancelCommand != nil {
		cancelCommand()
	}
	if active := a.activeTurn(); active != nil {
		return active.cancel(ctx, "tui interrupt")
	}
	return a.Adapter.Interrupt(ctx)
}

func (a *SessionClientAdapter) activeTurn() *sessionClientTurn {
	if a == nil {
		return nil
	}
	a.activeMu.Lock()
	defer a.activeMu.Unlock()
	return a.active
}

func (a *SessionClientAdapter) setActiveTurn(turn *sessionClientTurn) {
	a.activeMu.Lock()
	previous := a.active
	a.active = turn
	a.activeMu.Unlock()
	if previous != nil && previous != turn {
		_ = previous.Close()
	}
}

func (a *SessionClientAdapter) clearActiveTurn(turn *sessionClientTurn) {
	a.activeMu.Lock()
	if a.active == turn {
		a.active = nil
	}
	a.activeMu.Unlock()
}

type sessionClientTurn struct {
	turn    controlclient.SessionTurn
	onClose func()

	cancelOnce sync.Once
	cancelErr  error
	closeOnce  sync.Once
	closeErr   error
}

func (t *sessionClientTurn) HandleID() string {
	if t == nil || t.turn == nil {
		return ""
	}
	return t.turn.Target().HandleID
}

func (t *sessionClientTurn) RunID() string {
	if t == nil || t.turn == nil {
		return ""
	}
	return t.turn.Target().RunID
}

func (t *sessionClientTurn) TurnID() string {
	if t == nil || t.turn == nil {
		return ""
	}
	return t.turn.Target().TurnID
}

func (t *sessionClientTurn) Events() <-chan eventstream.Envelope {
	if t == nil || t.turn == nil {
		return closedEnvelopeChannel()
	}
	return t.turn.Events()
}

func (t *sessionClientTurn) SubmitApproval(
	ctx context.Context,
	decision controlprompt.ApprovalDecision,
) error {
	if t == nil || t.turn == nil {
		return errors.New("app/gatewayapp/controladapter: Session Turn is unavailable")
	}
	return t.turn.ResolveApproval(ctx, controlclient.ApprovalResolution{
		RequestID:  decision.RequestID,
		Outcome:    strings.TrimSpace(decision.Outcome),
		OptionID:   strings.TrimSpace(decision.OptionID),
		Approved:   decision.Approved,
		Reason:     strings.TrimSpace(decision.Reason),
		ReviewText: strings.TrimSpace(decision.ReviewText),
	})
}

func (t *sessionClientTurn) Cancel() {
	_ = t.cancel(context.Background(), "tui interrupt")
}

func (t *sessionClientTurn) cancel(ctx context.Context, reason string) error {
	if t == nil || t.turn == nil {
		return nil
	}
	t.cancelOnce.Do(func() {
		t.cancelErr = t.turn.Cancel(ctx, reason)
	})
	return t.cancelErr
}

func (t *sessionClientTurn) Close() error {
	if t == nil {
		return nil
	}
	t.closeOnce.Do(func() {
		if t.turn != nil {
			t.closeErr = t.turn.Close()
		}
		if t.onClose != nil {
			t.onClose()
		}
	})
	return t.closeErr
}

func (t *sessionClientTurn) Err() error {
	if t == nil || t.turn == nil {
		return nil
	}
	return t.turn.Err()
}

var _ controlprompt.Service = (*SessionClientAdapter)(nil)
var _ controlprompt.Turn = (*sessionClientTurn)(nil)
