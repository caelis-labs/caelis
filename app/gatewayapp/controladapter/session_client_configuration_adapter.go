package controladapter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	controlclient "github.com/caelis-labs/caelis/control/client"
	controlstatus "github.com/caelis-labs/caelis/control/status"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func (a *SessionClientAdapter) CycleSessionMode(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	if a == nil || a.statusClient == nil {
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter: status client is unavailable")
	}
	observed, err := a.addressedStatus(ctx, a.clientSessionID(), false)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	next := "manual"
	if strings.EqualFold(strings.TrimSpace(observed.Session.SessionMode), "manual") {
		next = "auto-review"
	}
	return a.configureSessionMode(ctx, next)
}

func (a *SessionClientAdapter) SetSessionMode(ctx context.Context, mode string) (controlstatus.StatusSnapshot, error) {
	return a.configureSessionMode(ctx, mode)
}

func (a *SessionClientAdapter) configureSessionMode(ctx context.Context, mode string) (controlstatus.StatusSnapshot, error) {
	state, err := a.currentClientSessionState(ctx)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	revision := state.Revision
	result, err := a.configClient.ConfigureSessionMode(ctx, controlclient.SessionModeRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:             "session-mode-" + uuid.NewString(),
			SessionID:               state.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: state.Controller.EpochID,
		},
		Mode: strings.TrimSpace(mode),
	})
	return a.observeCommittedSessionConfiguration(ctx, "session mode", result, err)
}

func (a *SessionClientAdapter) Connect(ctx context.Context, config controlprompt.ConnectConfig) (controlstatus.StatusSnapshot, error) {
	if a == nil || a.configClient == nil || a.statusClient == nil {
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter: configuration client is unavailable")
	}
	a.sessionChangeMu.Lock()
	selectedSessionID := a.clientSessionID()
	a.sessionChangeMu.Unlock()
	before, err := a.addressedStatus(ctx, "", false)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	revision := before.Configuration.Revision
	result, commandErr := a.configClient.ConnectModel(ctx, controlclient.ConnectModelRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:      "model-connect-" + uuid.NewString(),
			ExpectedRevision: &revision,
		},
		Config: config,
	})
	connected, err := a.observeCommittedHostConfiguration(ctx, "model connection", result, commandErr)
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	if !statusHasConfiguredModel(before) && strings.TrimSpace(selectedSessionID) != "" {
		if connected.Configuration.Revision != result.Revision {
			observationErr := fmt.Errorf(
				"app/gatewayapp/controladapter: Host model connection committed as operation %q at revision %d, but status advanced to revision %d before Session selection; selection was not attempted and the connection must not be retried blindly",
				result.OperationID,
				result.Revision,
				connected.Configuration.Revision,
			)
			return connected, &controlclient.CommandReceiptError{Receipt: result, Err: observationErr}
		}
		model := strings.TrimSpace(connected.ModelStatus.Alias)
		if model == "" {
			model = strings.TrimSpace(connected.ModelStatus.Name)
		}
		if model != "" {
			// Host connection and Session selection are distinct mutations. The
			// latter therefore receives its own operation identity and exact CAS.
			selected, selectionErr := a.useModelForSelectedSession(ctx, selectedSessionID, model, "")
			if selectionErr != nil {
				selectionErr = fmt.Errorf(
					"app/gatewayapp/controladapter: Host model connection committed as operation %q, but Session selection failed; do not retry the connection blindly: %w",
					result.OperationID,
					selectionErr,
				)
				var receiptErr *controlclient.CommandReceiptError
				if !errors.As(selectionErr, &receiptErr) {
					selectionErr = &controlclient.CommandReceiptError{Receipt: result, Err: selectionErr}
				}
				return connected, selectionErr
			}
			return selected, nil
		}
	}
	if strings.TrimSpace(selectedSessionID) == "" {
		return connected, nil
	}
	return a.addressedStatus(ctx, selectedSessionID, true)
}

func statusHasConfiguredModel(snapshot controlstatus.StatusSnapshot) bool {
	model := snapshot.ModelStatus
	return strings.TrimSpace(model.Alias) != "" || strings.TrimSpace(model.Name) != "" || strings.TrimSpace(model.Display) != ""
}

func (a *SessionClientAdapter) UseModel(ctx context.Context, model string, effort ...string) (controlstatus.StatusSnapshot, error) {
	if a == nil || a.configClient == nil || a.statusClient == nil {
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter: configuration client is unavailable")
	}
	reasoning := ""
	if len(effort) > 0 {
		reasoning = strings.TrimSpace(effort[0])
	}
	model = strings.TrimSpace(model)

	// Freeze the presentation Session address while choosing the mutation
	// scope. With no selected Session, /model use changes the canonical Host
	// default used by future Sessions. Once a Session is selected, the same
	// command changes only that Session's model selection.
	a.sessionChangeMu.Lock()
	defer a.sessionChangeMu.Unlock()
	if a.clientSessionID() == "" {
		before, err := a.addressedStatus(ctx, "", false)
		if err != nil {
			return controlstatus.StatusSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: read Host configuration revision: %w", err)
		}
		revision := before.Configuration.Revision
		result, commandErr := a.configClient.UseModel(ctx, controlclient.UseModelRequest{
			WriteBase: controlclient.WriteBase{
				OperationID:      "model-use-" + uuid.NewString(),
				ExpectedRevision: &revision,
			},
			Model: model, ReasoningEffort: reasoning,
		})
		return a.observeCommittedHostConfiguration(ctx, "Host default model", result, commandErr)
	}
	return a.useModelForSelectedSessionLocked(ctx, a.clientSessionID(), model, reasoning)
}

func (a *SessionClientAdapter) useModelForSelectedSession(
	ctx context.Context,
	selectedSessionID string,
	model string,
	reasoning string,
) (controlstatus.StatusSnapshot, error) {
	a.sessionChangeMu.Lock()
	defer a.sessionChangeMu.Unlock()
	currentSessionID := a.clientSessionID()
	if currentSessionID != strings.TrimSpace(selectedSessionID) {
		return controlstatus.StatusSnapshot{}, fmt.Errorf(
			"app/gatewayapp/controladapter: selected Session changed from %q to %q before model selection",
			strings.TrimSpace(selectedSessionID),
			currentSessionID,
		)
	}
	return a.useModelForSelectedSessionLocked(ctx, currentSessionID, strings.TrimSpace(model), strings.TrimSpace(reasoning))
}

// useModelForSelectedSessionLocked requires sessionChangeMu. It binds the
// mutation to the Session identity that owned the presentation action instead
// of re-evaluating the scope after another lifecycle operation.
func (a *SessionClientAdapter) useModelForSelectedSessionLocked(
	ctx context.Context,
	selectedSessionID string,
	model string,
	reasoning string,
) (controlstatus.StatusSnapshot, error) {
	if a.sessionClient == nil {
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter: Session client is unavailable")
	}
	state, err := a.sessionClient.InspectSession(ctx, controlclient.StateRequest{SessionID: selectedSessionID})
	if err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	if err := a.validateTUISessionController(state); err != nil {
		return controlstatus.StatusSnapshot{}, err
	}
	if state.SessionID != selectedSessionID {
		return controlstatus.StatusSnapshot{}, fmt.Errorf(
			"app/gatewayapp/controladapter: selected Session %q resolved to %q",
			selectedSessionID,
			state.SessionID,
		)
	}
	revision := state.Revision
	result, err := a.configClient.UseSessionModel(ctx, controlclient.SessionModelRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:             "session-model-" + uuid.NewString(),
			SessionID:               state.SessionID,
			ExpectedRevision:        &revision,
			ExpectedControllerEpoch: state.Controller.EpochID,
		},
		Model: model, ReasoningEffort: reasoning,
	})
	return a.observeCommittedSessionConfiguration(ctx, "session model", result, err)
}

func (a *SessionClientAdapter) observeCommittedSessionConfiguration(
	ctx context.Context,
	label string,
	result controlclient.CommandResult,
	commandErr error,
) (controlstatus.StatusSnapshot, error) {
	if result.Outcome != controlclient.OutcomeCommitted {
		if commandErr == nil {
			commandErr = fmt.Errorf(
				"app/gatewayapp/controladapter: %s outcome is %q: %s",
				label,
				result.Outcome,
				strings.TrimSpace(result.Detail),
			)
		}
		return controlstatus.StatusSnapshot{}, &controlclient.CommandReceiptError{Receipt: result, Err: commandErr}
	}
	observed, observationErr := a.addressedStatus(ctx, a.clientSessionID(), true)
	if observationErr != nil {
		observationErr = fmt.Errorf(
			"app/gatewayapp/controladapter: %s committed as operation %q but status observation failed; do not retry blindly: %w",
			label,
			result.OperationID,
			observationErr,
		)
	}
	resultErr := errors.Join(commandErr, observationErr)
	if resultErr != nil {
		resultErr = &controlclient.CommandReceiptError{Receipt: result, Err: resultErr}
	}
	return observed, resultErr
}

func (a *SessionClientAdapter) DeleteModel(ctx context.Context, model string) error {
	if a == nil || a.configClient == nil || a.statusClient == nil {
		return errors.New("app/gatewayapp/controladapter: configuration client is unavailable")
	}
	before, err := a.addressedStatus(ctx, "", true)
	if err != nil {
		return fmt.Errorf("app/gatewayapp/controladapter: read Host configuration revision: %w", err)
	}
	revision := before.Configuration.Revision
	result, commandErr := a.configClient.DeleteModel(ctx, controlclient.DeleteModelRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:      "model-delete-" + uuid.NewString(),
			ExpectedRevision: &revision,
		},
		Model: strings.TrimSpace(model),
	})
	_, err = a.observeCommittedHostConfiguration(ctx, "model deletion", result, commandErr)
	return err
}

func (a *SessionClientAdapter) observeCommittedHostConfiguration(
	ctx context.Context,
	label string,
	result controlclient.CommandResult,
	commandErr error,
) (controlstatus.StatusSnapshot, error) {
	if result.Outcome != controlclient.OutcomeCommitted {
		if commandErr == nil {
			commandErr = fmt.Errorf(
				"app/gatewayapp/controladapter: %s outcome is %q: %s",
				label,
				result.Outcome,
				strings.TrimSpace(result.Detail),
			)
		}
		return controlstatus.StatusSnapshot{}, &controlclient.CommandReceiptError{Receipt: result, Err: commandErr}
	}
	observed, observationErr := a.addressedStatus(ctx, "", true)
	if observationErr != nil {
		observationErr = fmt.Errorf(
			"app/gatewayapp/controladapter: %s committed as operation %q but Host status observation failed; do not retry blindly: %w",
			label,
			result.OperationID,
			observationErr,
		)
	}
	resultErr := errors.Join(commandErr, observationErr)
	if resultErr != nil {
		resultErr = &controlclient.CommandReceiptError{Receipt: result, Err: resultErr}
	}
	return observed, resultErr
}

func (a *SessionClientAdapter) SetSandboxBackend(ctx context.Context, backend string) (controlstatus.StatusSnapshot, error) {
	return a.configureSandbox(ctx, "set", backend)
}

func (a *SessionClientAdapter) PrepareSandbox(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	return a.configureSandbox(ctx, "prepare", "")
}

func (a *SessionClientAdapter) RepairSandbox(ctx context.Context) (controlstatus.StatusSnapshot, error) {
	return a.configureSandbox(ctx, "repair", "")
}

// RefreshSandbox runs the Host-owned background refresh through the typed
// configuration capability without creating or requiring a Session.
func (a *SessionClientAdapter) RefreshSandbox(ctx context.Context) error {
	_, err := a.configureSandbox(ctx, "refresh", "")
	return err
}

func (a *SessionClientAdapter) configureSandbox(ctx context.Context, action, backend string) (controlstatus.StatusSnapshot, error) {
	if a == nil || a.configClient == nil || a.statusClient == nil {
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter: configuration client is unavailable")
	}
	before, err := a.addressedStatus(ctx, "", true)
	if err != nil {
		return controlstatus.StatusSnapshot{}, fmt.Errorf("app/gatewayapp/controladapter: read Host configuration revision: %w", err)
	}
	expectedRevision := before.Configuration.Revision
	request := controlclient.SandboxRequest{
		WriteBase: controlclient.WriteBase{
			OperationID:      "sandbox-" + action + "-" + uuid.NewString(),
			ExpectedRevision: &expectedRevision,
		},
		Backend: strings.TrimSpace(backend),
	}
	var result controlclient.CommandResult
	switch action {
	case "set":
		result, err = a.configClient.SetSandboxBackend(ctx, request)
	case "prepare":
		result, err = a.configClient.PrepareSandbox(ctx, request)
	case "repair":
		result, err = a.configClient.RepairSandbox(ctx, request)
	case "refresh":
		result, err = a.configClient.RefreshSandbox(ctx, request)
	default:
		return controlstatus.StatusSnapshot{}, errors.New("app/gatewayapp/controladapter: unknown sandbox action")
	}
	if result.Outcome != controlclient.OutcomeCommitted {
		if err == nil {
			err = fmt.Errorf(
				"app/gatewayapp/controladapter: sandbox %s outcome is %q: %s",
				action,
				result.Outcome,
				strings.TrimSpace(result.Detail),
			)
		}
		return controlstatus.StatusSnapshot{}, &controlclient.CommandReceiptError{Receipt: result, Err: err}
	}
	observed, observationErr := a.addressedStatus(ctx, a.clientSessionID(), true)
	if observationErr != nil {
		observationErr = fmt.Errorf(
			"app/gatewayapp/controladapter: sandbox %s committed as operation %q but status observation failed; do not retry blindly: %w",
			action,
			result.OperationID,
			observationErr,
		)
	}
	resultErr := errors.Join(err, observationErr)
	if resultErr != nil {
		resultErr = &controlclient.CommandReceiptError{Receipt: result, Err: resultErr}
	}
	return observed, resultErr
}
