package appserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/caelis-labs/caelis/control/internal/operationreceipt"
	"github.com/caelis-labs/caelis/control/sessionprofile"
)

// UpdateSessionConfigurationRequest changes explicit Session assembly. Its CAS
// revision is independent of conversation history and controller revisions.
type UpdateSessionConfigurationRequest struct {
	WriteBase
	ExpectedConfigurationRevision uint64                            `json:"expected_configuration_revision"`
	Patch                         sessionprofile.ConfigurationPatch `json:"patch"`
}

func (s *SessionProfileService) UpdateSessionConfiguration(ctx context.Context, p Principal, req UpdateSessionConfigurationRequest) (CommandResult, error) {
	if s.config.ValidateProfile == nil {
		return CommandResult{}, sessionprofile.ErrUnsupported
	}
	if s.config.ReviewerState == nil && req.Patch.Tools != nil && requiresCallbackApproval(*req.Patch.Tools) {
		return CommandResult{}, sessionprofile.ErrUnsupported
	}
	result, err := s.config.Commands.execute(ctx, p, ActionSessionConfigure, req.WriteBase, req.SessionID, req)
	if err == nil && result.Configuration != nil && s.config.ConfigurationCommitted != nil {
		s.config.ConfigurationCommitted(ctx, req.SessionID, *result.Configuration)
	}
	return result, err
}

type transactionOperationStore interface {
	CompleteTransaction(context.Context, *sql.Tx, OperationIntent, CommandResult) (OperationRecord, error)
}

func (s *CommandService) withTransactionReceipt(ctx context.Context, intent OperationIntent) (context.Context, error) {
	store, ok := s.config.Operations.(transactionOperationStore)
	if !ok {
		return ctx, errors.New("control mutation requires an atomic database receipt")
	}
	return operationreceipt.WithTransaction(ctx, func(ctx context.Context, tx *sql.Tx, raw []byte) error {
		result := CommandResult{OperationID: intent.OperationID, SessionID: intent.SessionID, Outcome: OutcomeCommitted}
		switch intent.Action {
		case ActionSessionConfigure:
			result.Configuration = new(sessionprofile.Configuration)
			if err := json.Unmarshal(raw, result.Configuration); err != nil {
				return err
			}
			if result.Configuration.SessionID != intent.SessionID {
				return ErrOperationConflict
			}
		case ActionBackgroundGrantCreate, ActionBackgroundGrantRevoke:
			result.BackgroundGrant = new(sessionprofile.BackgroundGrant)
			if err := json.Unmarshal(raw, result.BackgroundGrant); err != nil {
				return err
			}
			if result.BackgroundGrant.SessionID != intent.SessionID {
				return ErrOperationConflict
			}
		case ActionSessionResource:
			result.SessionResource = new(sessionprofile.Resource)
			if err := json.Unmarshal(raw, result.SessionResource); err != nil {
				return err
			}
			if result.SessionResource.SessionID != intent.SessionID {
				return ErrOperationConflict
			}
		default:
			return ErrOperationConflict
		}
		_, err := store.CompleteTransaction(ctx, tx, intent, result)
		return err
	}), nil
}
