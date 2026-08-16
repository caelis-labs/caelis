package appserveradapter

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/model"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// SessionClientAdapter is the presentation-facing AppServer client facade. It
// contains no Runtime or Stack handle; every semantic operation is routed
// through a focused typed client.
type SessionClientAdapter struct {
	turns            *appserver.SessionTurnClient
	participants     *appserver.ParticipantTurnClient
	sessionClient    appserver.SessionClient
	statusClient     appserver.StatusClient
	configClient     appserver.ConfigurationClient
	agentClient      appserver.AgentClient
	completionClient appserver.CompletionClient
	pluginClient     appserver.PluginClient
	surface          string
	workspaceKey     string
	preferredID      string

	sessionMu       sync.RWMutex
	sessionChangeMu sync.Mutex
	sessionID       string
	workspaceDir    string

	activeMu      sync.Mutex
	active        *sessionClientTurn
	reconnect     *clientSessionReconnect
	presence      *sessionPresence
	starting      int
	admissionWait chan struct{}
	admissionErr  error
	nextAdmission uint64
	admissions    map[uint64]*turnAdmission

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
	Sessions           appserver.SessionClient
	Participants       appserver.ParticipantClient
	Status             appserver.StatusClient
	Configuration      appserver.ConfigurationClient
	Agents             appserver.AgentClient
	Completion         appserver.CompletionClient
	Plugins            appserver.PluginClient
}

// NewAppServerAdapter composes the complete typed facade used by production
// presentation surfaces.
func NewAppServerAdapter(config AppServerAdapterConfig) (*SessionClientAdapter, error) {
	turns, err := appserver.NewSessionTurnClient(config.Sessions)
	if err != nil {
		return nil, err
	}
	participantTurns, err := appserver.NewParticipantTurnClient(config.Sessions, config.Participants)
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
	return a.startAdmittedTurn(ctx, func(startCtx context.Context) (appserver.TargetTurn, error) {
		state, err := a.ensureSessionForMainPrompt(startCtx)
		if err != nil {
			return nil, err
		}
		return a.turns.Start(startCtx, appserver.SessionTurnStartRequest{
			SessionID:    state.SessionID,
			Input:        rawInput,
			DisplayInput: displayInput,
			ContentParts: contentParts,
		})
	})
}

const (
	interruptAdmissionGrace = 250 * time.Millisecond
	interruptCancelTimeout  = 5 * time.Second
)

func (a *SessionClientAdapter) Interrupt(ctx context.Context) error {
	if a == nil {
		return errors.New("app/gatewayapp/controladapter: Session client adapter is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.activeMu.Lock()
	active := a.active
	reconnect := a.reconnect
	wait := a.admissionWait
	starting := a.starting
	a.activeMu.Unlock()
	if active != nil {
		return cancelTurnWithTimeout(ctx, active.cancel)
	}
	if reconnect != nil {
		return cancelTurnWithTimeout(ctx, reconnect.cancel)
	}
	if starting == 0 || wait == nil {
		return noActiveTurnSubmissionError()
	}
	admissionCtx, stopAdmission := context.WithTimeout(ctx, interruptAdmissionGrace)
	defer stopAdmission()
	select {
	case <-admissionCtx.Done():
		if err := ctx.Err(); err != nil {
			return err
		}
		a.cancelTurnAdmissions(wait)
		a.activeMu.Lock()
		active = a.active
		reconnect = a.reconnect
		currentWait := a.admissionWait
		admissionErr := a.admissionErr
		a.activeMu.Unlock()
		if active != nil {
			return cancelTurnWithTimeout(ctx, active.cancel)
		}
		if reconnect != nil {
			return cancelTurnWithTimeout(ctx, reconnect.cancel)
		}
		if currentWait != wait && admissionErr != nil {
			return admissionErr
		}
		// The exact target was not admitted within the bounded grace period.
		// Cancelling the registered admission releases the Surface immediately;
		// startAdmittedTurn separately cancels any target that arrives late.
		return nil
	case <-wait:
	}
	a.activeMu.Lock()
	active = a.active
	reconnect = a.reconnect
	admissionErr := a.admissionErr
	a.activeMu.Unlock()
	if active != nil {
		return cancelTurnWithTimeout(ctx, active.cancel)
	}
	if reconnect != nil {
		return cancelTurnWithTimeout(ctx, reconnect.cancel)
	}
	if admissionErr != nil {
		return admissionErr
	}
	return noActiveTurnSubmissionError()
}

func cancelTurnWithTimeout(ctx context.Context, cancel func(context.Context, string) error) error {
	if cancel == nil {
		return noActiveTurnSubmissionError()
	}
	cancelCtx, stop := context.WithTimeout(ctx, interruptCancelTimeout)
	defer stop()
	result := make(chan error, 1)
	go func() {
		result <- cancel(cancelCtx, "tui interrupt")
	}()
	select {
	case err := <-result:
		return err
	case <-cancelCtx.Done():
		return cancelCtx.Err()
	}
}

type turnAdmissionResult struct {
	turn appserver.TargetTurn
	err  error
}

type turnAdmission struct {
	cancel    context.CancelFunc
	cancelled bool
}

func (a *SessionClientAdapter) startAdmittedTurn(
	ctx context.Context,
	start func(context.Context) (appserver.TargetTurn, error),
) (controlprompt.Turn, error) {
	if start == nil {
		return nil, errors.New("app/gatewayapp/controladapter: turn admission is unavailable")
	}
	admissionID, admissionCtx := a.markTurnAdmission(ctx)
	resultCh := make(chan turnAdmissionResult, 1)
	go func() {
		turn, err := start(admissionCtx)
		resultCh <- turnAdmissionResult{turn: turn, err: err}
	}()
	var result turnAdmissionResult
	select {
	case result = <-resultCh:
	case <-admissionCtx.Done():
		a.finishTurnAdmission(admissionID, admissionCtx.Err())
		go cleanupLateAdmission(resultCh)
		return nil, admissionCtx.Err()
	}
	turn, err := result.turn, result.err
	if err != nil {
		a.finishTurnAdmission(admissionID, err)
		return nil, err
	}
	if turn == nil {
		err = errors.New("app/gatewayapp/controladapter: turn admission returned no Turn")
		a.finishTurnAdmission(admissionID, err)
		return nil, err
	}
	wrapped := &sessionClientTurn{turn: turn}
	wrapped.onClose = func() { a.clearActiveTurn(wrapped) }
	if !a.setActiveTurnForAdmission(admissionID, admissionCtx, wrapped) {
		err = context.Canceled
		a.finishTurnAdmission(admissionID, err)
		go cleanupAdmittedTurn(turn)
		return nil, err
	}
	a.finishTurnAdmission(admissionID, nil)
	return wrapped, nil
}

func (a *SessionClientAdapter) markTurnAdmission(parent context.Context) (uint64, context.Context) {
	if parent == nil {
		parent = context.Background()
	}
	admissionCtx, cancel := context.WithCancel(parent)
	a.activeMu.Lock()
	defer a.activeMu.Unlock()
	a.nextAdmission++
	id := a.nextAdmission
	if a.admissions == nil {
		a.admissions = map[uint64]*turnAdmission{}
	}
	a.admissions[id] = &turnAdmission{cancel: cancel}
	a.starting++
	if a.admissionWait == nil {
		a.admissionWait = make(chan struct{})
		a.admissionErr = nil
	}
	return id, admissionCtx
}

func (a *SessionClientAdapter) finishTurnAdmission(id uint64, err error) {
	if a == nil {
		return
	}
	a.activeMu.Lock()
	admission := a.admissions[id]
	delete(a.admissions, id)
	if err != nil {
		a.admissionErr = err
	}
	if a.starting > 0 {
		a.starting--
	}
	if a.starting == 0 && a.admissionWait != nil {
		close(a.admissionWait)
		a.admissionWait = nil
	}
	a.activeMu.Unlock()
	if admission != nil && admission.cancel != nil {
		admission.cancel()
	}
}

func (a *SessionClientAdapter) cancelTurnAdmissions(wait chan struct{}) {
	if a == nil {
		return
	}
	a.activeMu.Lock()
	if wait != nil && a.admissionWait != wait {
		a.activeMu.Unlock()
		return
	}
	cancels := make([]context.CancelFunc, 0, len(a.admissions))
	for _, admission := range a.admissions {
		if admission == nil || admission.cancelled {
			continue
		}
		admission.cancelled = true
		cancels = append(cancels, admission.cancel)
	}
	a.activeMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func cleanupLateAdmission(resultCh <-chan turnAdmissionResult) {
	result := <-resultCh
	cleanupAdmittedTurn(result.turn)
}

func cleanupAdmittedTurn(turn appserver.TargetTurn) {
	if turn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), interruptCancelTimeout)
	_ = turn.Cancel(ctx, "tui interrupt during admission")
	cancel()
	_ = turn.Close()
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
	if a == nil {
		return
	}
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

func (a *SessionClientAdapter) setActiveTurnForAdmission(
	id uint64,
	ctx context.Context,
	turn *sessionClientTurn,
) bool {
	if a == nil || turn == nil {
		return false
	}
	a.activeMu.Lock()
	admission := a.admissions[id]
	if admission == nil || admission.cancelled || ctx.Err() != nil {
		a.activeMu.Unlock()
		return false
	}
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
	return true
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
	turn    appserver.TargetTurn
	onClose func()

	cancelMu        sync.Mutex
	cancelWait      chan struct{}
	cancelCommitted bool
	closeOnce       sync.Once
	closeErr        error
}

func (t *sessionClientTurn) steer(
	ctx context.Context,
	input string,
	displayInput string,
	contentParts []model.ContentPart,
) error {
	mainTurn, ok := t.turn.(appserver.SessionTurn)
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
	return t.turn.ResolveApproval(ctx, appserver.ApprovalResolution{
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
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		t.cancelMu.Lock()
		if t.cancelCommitted {
			t.cancelMu.Unlock()
			return nil
		}
		if wait := t.cancelWait; wait != nil {
			t.cancelMu.Unlock()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-wait:
			}
			continue
		}
		wait := make(chan struct{})
		t.cancelWait = wait
		t.cancelMu.Unlock()

		err := t.turn.Cancel(ctx, reason)

		t.cancelMu.Lock()
		if err == nil {
			t.cancelCommitted = true
		}
		t.cancelWait = nil
		close(wait)
		t.cancelMu.Unlock()
		return err
	}
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
