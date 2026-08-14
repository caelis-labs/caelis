package controladapter

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// SessionClientAdapter is the presentation-facing AppServer client facade. It
// contains no Runtime or Stack handle; every semantic operation is routed
// through a focused typed client.
type SessionClientAdapter struct {
	turns            *controlclient.SessionTurnClient
	participants     *controlclient.ParticipantTurnClient
	sessionClient    controlclient.SessionClient
	statusClient     controlclient.StatusClient
	configClient     controlclient.ConfigurationClient
	agentClient      controlclient.AgentClient
	completionClient controlclient.CompletionClient
	pluginClient     controlclient.PluginClient
	surface          string
	workspaceKey     string
	preferredID      string

	sessionMu       sync.RWMutex
	sessionChangeMu sync.Mutex
	sessionID       string
	workspaceDir    string

	activeMu  sync.Mutex
	active    *sessionClientTurn
	reconnect *clientSessionReconnect
	presence  *sessionPresence

	acpPreparationMu sync.Mutex
	acpPreparations  map[string]controlagents.ACPPreparation
	acpPending       map[string]pendingACPPreparationObservation
}

// AppServerAdapterConfig binds one presentation facade to a Session address
// and the focused AppServer capabilities it may consume.
type AppServerAdapterConfig struct {
	SessionID          string
	PreferredSessionID string
	WorkspaceKey       string
	WorkspaceDir       string
	Surface            string
	Sessions           controlclient.SessionClient
	Participants       controlclient.ParticipantClient
	Status             controlclient.StatusClient
	Configuration      controlclient.ConfigurationClient
	Agents             controlclient.AgentClient
	Completion         controlclient.CompletionClient
	Plugins            controlclient.PluginClient
}

// NewAppServerAdapter composes the complete typed facade used by production
// presentation surfaces.
func NewAppServerAdapter(config AppServerAdapterConfig) (*SessionClientAdapter, error) {
	turns, err := controlclient.NewSessionTurnClient(config.Sessions)
	if err != nil {
		return nil, err
	}
	participantTurns, err := controlclient.NewParticipantTurnClient(config.Sessions, config.Participants)
	if err != nil {
		return nil, err
	}
	if config.Status == nil {
		return nil, errors.New("app/gatewayapp/controladapter: status client is required")
	}
	if config.Configuration == nil {
		return nil, errors.New("app/gatewayapp/controladapter: configuration client is required")
	}
	if config.Agents == nil {
		return nil, errors.New("app/gatewayapp/controladapter: Agent client is required")
	}
	if config.Completion == nil {
		return nil, errors.New("app/gatewayapp/controladapter: completion client is required")
	}
	if config.Plugins == nil {
		return nil, errors.New("app/gatewayapp/controladapter: plugin client is required")
	}
	return &SessionClientAdapter{
		turns: turns, participants: participantTurns, sessionClient: config.Sessions,
		statusClient: config.Status, configClient: config.Configuration,
		agentClient: config.Agents, completionClient: config.Completion, pluginClient: config.Plugins,
		surface: strings.TrimSpace(config.Surface), workspaceKey: strings.TrimSpace(config.WorkspaceKey),
		preferredID: strings.TrimSpace(config.PreferredSessionID), sessionID: strings.TrimSpace(config.SessionID),
		workspaceDir:    strings.TrimSpace(config.WorkspaceDir),
		acpPreparations: map[string]controlagents.ACPPreparation{},
		acpPending:      map[string]pendingACPPreparationObservation{},
	}, nil
}

func (a *SessionClientAdapter) Submit(
	ctx context.Context,
	submission controlprompt.Submission,
) (controlprompt.Turn, error) {
	if a == nil || a.turns == nil {
		return nil, errors.New("app/gatewayapp/controladapter: Session client adapter is unavailable")
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
	if rawInput == "" && len(contentParts) == 0 {
		return nil, errors.New("app/gatewayapp/controladapter: prompt input is required")
	}
	if submission.Mode == controlprompt.SubmissionModeActiveTurn {
		active := a.activeTurn()
		if active != nil {
			if err := active.steer(ctx, rawInput, displayInput, contentParts); err != nil {
				return nil, err
			}
			return nil, nil
		}
		if reconnect := a.activeReconnect(); reconnect != nil {
			if err := reconnect.steer(ctx, rawInput, displayInput, contentParts); err != nil {
				return nil, err
			}
			return nil, nil
		}
		return nil, noActiveTurnSubmissionError()
	}
	state, err := a.ensureSessionForMainPrompt(ctx)
	if err != nil {
		return nil, err
	}
	turn, err := a.turns.Start(ctx, controlclient.SessionTurnStartRequest{
		SessionID:    state.SessionID,
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
	if a == nil {
		return errors.New("app/gatewayapp/controladapter: Session client adapter is unavailable")
	}
	if active := a.activeTurn(); active != nil {
		return active.cancel(ctx, "tui interrupt")
	}
	if reconnect := a.activeReconnect(); reconnect != nil {
		return reconnect.cancel(ctx, "tui interrupt")
	}
	return noActiveTurnSubmissionError()
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
	previousReconnect := a.reconnect
	a.active = turn
	a.reconnect = nil
	a.activeMu.Unlock()
	if previous != nil && previous != turn {
		_ = previous.Close()
	}
	if previousReconnect != nil {
		_ = previousReconnect.Close()
	}
}

func (a *SessionClientAdapter) clearActiveTurn(turn *sessionClientTurn) {
	a.activeMu.Lock()
	if a.active == turn {
		a.active = nil
	}
	a.activeMu.Unlock()
}

func (a *SessionClientAdapter) activeReconnect() *clientSessionReconnect {
	if a == nil {
		return nil
	}
	a.activeMu.Lock()
	defer a.activeMu.Unlock()
	return a.reconnect
}

func (a *SessionClientAdapter) setActiveReconnect(reconnect *clientSessionReconnect) {
	a.activeMu.Lock()
	previous := a.active
	previousReconnect := a.reconnect
	a.active = nil
	a.reconnect = reconnect
	a.activeMu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	if previousReconnect != nil && previousReconnect != reconnect {
		_ = previousReconnect.Close()
	}
}

func (a *SessionClientAdapter) clearActiveReconnect(reconnect *clientSessionReconnect) {
	a.activeMu.Lock()
	if a.reconnect == reconnect {
		a.reconnect = nil
	}
	a.activeMu.Unlock()
}

type sessionClientTurn struct {
	turn    controlclient.TargetTurn
	onClose func()

	cancelOnce sync.Once
	cancelErr  error
	closeOnce  sync.Once
	closeErr   error
}

func (t *sessionClientTurn) steer(
	ctx context.Context,
	input string,
	displayInput string,
	contentParts []model.ContentPart,
) error {
	mainTurn, ok := t.turn.(controlclient.SessionTurn)
	if !ok {
		return noActiveTurnSubmissionError()
	}
	return mainTurn.Steer(ctx, input, displayInput, contentParts)
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

func closedEnvelopeChannel() <-chan eventstream.Envelope {
	closed := make(chan eventstream.Envelope)
	close(closed)
	return closed
}

var _ controlprompt.Service = (*SessionClientAdapter)(nil)
var _ controlprompt.Turn = (*sessionClientTurn)(nil)
