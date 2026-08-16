package appserveradapter

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	controlagents "github.com/caelis-labs/caelis/control/agents"
	appserver "github.com/caelis-labs/caelis/control/appserver"
)

type pendingACPPreparationObservation struct {
	operation string
	stage     pendingACPPreparationStage
	parentRef string
	receipt   appserver.CommandResult
}

type pendingACPPreparationStage string

const (
	pendingACPPreparationPrepare pendingACPPreparationStage = "prepare"
	pendingACPPreparationAuth    pendingACPPreparationStage = "prepare-auth"
)

func (a *SessionClientAdapter) observeCommittedACPPreparation(
	ctx context.Context,
	key string,
	operation string,
	stage pendingACPPreparationStage,
	parentRef string,
	receipt appserver.CommandResult,
	commandErr error,
) (controlagents.ACPPreparation, error) {
	pending, ok := newPendingACPPreparationObservation(operation, stage, parentRef, receipt)
	if !ok {
		return a.observeACPPreparationReceipt(ctx, operation, receipt, commandErr)
	}
	if a.acpPending == nil {
		a.acpPending = map[string]pendingACPPreparationObservation{}
	}
	a.acpPending[key] = pending
	return a.observePendingACPPreparation(ctx, key, pending, commandErr)
}

func newPendingACPPreparationObservation(
	operation string,
	stage pendingACPPreparationStage,
	parentRef string,
	receipt appserver.CommandResult,
) (pendingACPPreparationObservation, bool) {
	if receipt.Outcome != appserver.OutcomeCommitted || receipt.Resource == nil ||
		receipt.Resource.Kind != appserver.CommandResourceACPPreparation ||
		strings.TrimSpace(receipt.Resource.Ref) == "" || strings.TrimSpace(receipt.Resource.Digest) == "" {
		return pendingACPPreparationObservation{}, false
	}
	resource := *receipt.Resource
	receipt.Resource = &resource
	return pendingACPPreparationObservation{
		operation: strings.TrimSpace(operation),
		stage:     stage,
		parentRef: strings.TrimSpace(parentRef),
		receipt:   receipt,
	}, true
}

func (a *SessionClientAdapter) observePendingACPPreparation(
	ctx context.Context,
	key string,
	pending pendingACPPreparationObservation,
	commandErr error,
) (controlagents.ACPPreparation, error) {
	preparation, err := a.observeACPPreparationReceipt(ctx, pending.operation, pending.receipt, commandErr)
	if preparation.Ref == "" {
		return preparation, err
	}
	if validationErr := validatePendingACPPreparation(pending, preparation); validationErr != nil {
		return controlagents.ACPPreparation{}, &appserver.CommandReceiptError{
			Receipt: pending.receipt,
			Err:     errors.Join(err, validationErr),
		}
	}
	delete(a.acpPending, key)
	a.cacheACPPreparation(key, preparation)
	return preparation, err
}

func validatePendingACPPreparation(
	pending pendingACPPreparationObservation,
	preparation controlagents.ACPPreparation,
) error {
	preparation = controlagents.NormalizeACPPreparation(preparation)
	if !preparation.ExpiresAt.After(time.Now()) {
		return errors.New("app/gatewayapp/controladapter: observed ACP preparation is expired")
	}
	if preparation.ParentRef != pending.parentRef {
		return errors.New("app/gatewayapp/controladapter: observed ACP preparation belongs to another parent")
	}
	switch pending.stage {
	case pendingACPPreparationPrepare:
		if preparation.State == controlagents.PreparationStateNeedsAuth || preparation.State == controlagents.PreparationStateReady {
			return nil
		}
	case pendingACPPreparationAuth:
		if preparation.State == controlagents.PreparationStateReady {
			return nil
		}
	default:
		return errors.New("app/gatewayapp/controladapter: pending ACP preparation stage is invalid")
	}
	return fmt.Errorf(
		"app/gatewayapp/controladapter: observed %s ACP preparation state is %q",
		pending.stage,
		preparation.State,
	)
}

func (a *SessionClientAdapter) cacheACPPreparation(key string, preparation controlagents.ACPPreparation) {
	if a.acpPreparations == nil {
		a.acpPreparations = map[string]controlagents.ACPPreparation{}
	}
	a.acpPreparations[key] = controlagents.NormalizeACPPreparation(preparation)
}

func (a *SessionClientAdapter) observeACPPreparationReceipt(
	ctx context.Context,
	operation string,
	receipt appserver.CommandResult,
	commandErr error,
) (controlagents.ACPPreparation, error) {
	if receipt.Outcome != appserver.OutcomeCommitted {
		if commandErr == nil {
			commandErr = fmt.Errorf("%s outcome is %q: %s", operation, receipt.Outcome, strings.TrimSpace(receipt.Detail))
		}
		return controlagents.ACPPreparation{}, &appserver.CommandReceiptError{Receipt: receipt, Err: commandErr}
	}
	if receipt.Resource == nil || receipt.Resource.Kind != appserver.CommandResourceACPPreparation ||
		strings.TrimSpace(receipt.Resource.Ref) == "" || strings.TrimSpace(receipt.Resource.Digest) == "" {
		return controlagents.ACPPreparation{}, &appserver.CommandReceiptError{
			Receipt: receipt,
			Err:     fmt.Errorf("app/gatewayapp/controladapter: committed %s returned no preparation resource", operation),
		}
	}
	preparation, observationErr := a.agentClient.ACPPreparation(ctx, appserver.ACPPreparationRequest{Ref: receipt.Resource.Ref})
	if observationErr == nil {
		switch {
		case preparation.Ref != receipt.Resource.Ref:
			observationErr = errors.New("app/gatewayapp/controladapter: ACP preparation reference changed before observation")
		case preparation.ContentDigest != receipt.Resource.Digest:
			observationErr = errors.New("app/gatewayapp/controladapter: ACP preparation changed before observation")
		}
	}
	if observationErr != nil {
		observationErr = fmt.Errorf(
			"app/gatewayapp/controladapter: %s committed as operation %q but preparation observation failed; do not retry blindly: %w",
			operation, receipt.OperationID, observationErr,
		)
	}
	warning := commandErr
	if detail := strings.TrimSpace(receipt.Detail); detail != "" {
		warning = errors.Join(warning, fmt.Errorf(
			"app/gatewayapp/controladapter: %s committed as operation %q with a warning; do not retry blindly: %s",
			operation, receipt.OperationID, detail,
		))
	}
	if warning != nil || observationErr != nil {
		if observationErr != nil {
			preparation = controlagents.ACPPreparation{}
		}
		return preparation, &appserver.CommandReceiptError{
			Receipt: receipt, Err: errors.Join(warning, observationErr),
		}
	}
	return preparation, nil
}
