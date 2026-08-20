package acpagentbridge

import (
	"context"
	"fmt"
	"strings"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/protocol/acp"
)

// SteerSession injects standard ACP prompt content into the currently active
// main Turn. Control remains authoritative for whether the inspected target is
// still current when the command is applied.
func (a *RuntimeAgent) SteerSession(
	ctx context.Context,
	request acp.SessionSteeringRequest,
) (acp.SessionSteeringResponse, error) {
	if a == nil || a.sessionClient == nil {
		return acp.SessionSteeringResponse{}, acp.ErrCapabilityUnsupported
	}
	if _, err := a.targetSession(ctx, request.SessionID); err != nil {
		return acp.SessionSteeringResponse{}, err
	}
	input, contentParts, err := promptContent(request.Prompt)
	if err != nil {
		return acp.SessionSteeringResponse{}, err
	}
	options, err := acp.DecodeSessionSteeringOptions(request.Meta)
	if err != nil {
		return acp.SessionSteeringResponse{}, err
	}
	state, err := a.sessionClient.InspectSession(ctx, appserver.StateRequest{
		SessionID: strings.TrimSpace(request.SessionID),
	})
	if err != nil {
		return acp.SessionSteeringResponse{}, err
	}
	if !steerableMainRun(state.Run) {
		return idleSteeringResponse(options), nil
	}
	result, steerErr := a.sessionClient.Steer(ctx, appserver.SteerRequest{
		WriteBase: appserver.WriteBase{
			OperationID:             newACPSessionOperationID("steer"),
			SessionID:               strings.TrimSpace(state.SessionID),
			ExpectedControllerEpoch: strings.TrimSpace(state.Controller.EpochID),
		},
		Target: appserver.TurnTarget{
			HandleID: strings.TrimSpace(state.Run.HandleID),
			RunID:    strings.TrimSpace(state.Run.RunID),
			TurnID:   strings.TrimSpace(state.Run.TurnID),
		},
		Input:        input,
		ContentParts: contentParts,
	})
	if err := committedSteeringError(result, steerErr); err != nil {
		return acp.SessionSteeringResponse{}, err
	}
	return acp.SessionSteeringResponse{Outcome: acp.SessionSteeringInjected}, nil
}

func steerableMainRun(run appserver.RunState) bool {
	return run.Active &&
		run.Kind == appserver.RunKindKernel &&
		strings.TrimSpace(run.HandleID) != "" &&
		strings.TrimSpace(run.RunID) != "" &&
		strings.TrimSpace(run.TurnID) != ""
}

func idleSteeringResponse(options acp.SessionSteeringOptions) acp.SessionSteeringResponse {
	if options.IdleBehavior == acp.SessionSteeringIdlePromptRequired {
		return acp.SessionSteeringResponse{
			Outcome: acp.SessionSteeringPromptRequired,
			Reason:  "noRunningTurn",
		}
	}
	return acp.SessionSteeringResponse{Outcome: acp.SessionSteeringFailed}
}

func committedSteeringError(result appserver.CommandResult, err error) error {
	if result.Outcome == appserver.OutcomeCommitted {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("control steering ended with outcome %q", result.Outcome)
	}
	return &appserver.CommandReceiptError{Receipt: result, Err: err}
}

var _ acp.SessionSteeringAdapter = (*RuntimeAgent)(nil)
