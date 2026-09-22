package bot

import (
	"context"
	"encoding/json"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// WorkOperation is a permanent dispatch anchor. The shared Control ledger owns
// receipts; this record prevents a retired receipt from admitting the effect
// again and preserves a native address for observational recovery.
type WorkOperation struct {
	PrincipalID string    `json:"principal_id"`
	BotID       string    `json:"bot_id"`
	WorkID      string    `json:"work_id"`
	OperationID string    `json:"operation_id"`
	SourceID    string    `json:"source_id,omitempty"`
	Digest      string    `json:"digest"`
	Execution   Execution `json:"execution"`
	Outcome     string    `json:"outcome"`
}

// ReserveOperation records intent before dispatch and never renews an existing
// anchor, including unknown and rejected effects.
func (s *WorkStore) ReserveOperation(ctx context.Context, op WorkOperation) (WorkOperation, bool, error) {
	id := opaqueID("work-operation-", op.PrincipalID, op.OperationID)
	var old WorkOperation
	err := s.db.Get(ctx, "operation", id, &old)
	if err != nil && errorcode.CodeOf(err) != errorcode.NotFound {
		return old, false, err
	}
	if err == nil {
		if old.Digest != op.Digest || old.BotID != op.BotID || old.WorkID != op.WorkID || old.SourceID != op.SourceID {
			return old, false, errorcode.New(errorcode.Conflict, "bot: work operation conflicts")
		}
		return old, false, nil
	}
	data, err := json.Marshal(op)
	if err != nil {
		return op, false, err
	}
	// An uncertain dispatch fences further mutations from the same request.
	// Changing assignment text or an operation ID cannot turn it into a retry.
	result, err := s.db.db.ExecContext(ctx, `INSERT INTO bot_authority(kind,id,body)
 SELECT 'operation',?,? WHERE ?='' OR NOT EXISTS (
 SELECT 1 FROM bot_authority WHERE kind='operation' AND json_extract(body,'$.principal_id')=?
 AND json_extract(body,'$.bot_id')=? AND json_extract(body,'$.source_id')=?
 AND json_extract(body,'$.outcome') IN ('','unknown')) ON CONFLICT(kind,id) DO NOTHING`, id, string(data), op.SourceID, op.PrincipalID, op.BotID, op.SourceID)
	if err != nil {
		return op, false, err
	}
	count, err := result.RowsAffected()
	if err != nil || count == 1 {
		return op, count == 1, err
	}
	if err := s.db.Get(ctx, "operation", id, &old); errorcode.CodeOf(err) == errorcode.NotFound {
		return op, false, errorcode.New(errorcode.UnknownOutcome, "bot: this source has an uncertain work operation; query its receipt before further dispatch")
	} else if err != nil {
		return old, false, err
	}
	if old.Digest != op.Digest || old.BotID != op.BotID || old.WorkID != op.WorkID || old.SourceID != op.SourceID {
		return old, false, errorcode.New(errorcode.Conflict, "bot: work operation conflicts")
	}
	return old, false, nil
}

// CompleteOperation records the native result observed after dispatch.
func (s *WorkStore) CompleteOperation(ctx context.Context, op WorkOperation) error {
	id := opaqueID("work-operation-", op.PrincipalID, op.OperationID)
	var old WorkOperation
	if err := s.db.Get(ctx, "operation", id, &old); err != nil {
		return err
	}
	if old.Digest != op.Digest || old.BotID != op.BotID || old.WorkID != op.WorkID || old.SourceID != op.SourceID {
		return errorcode.New(errorcode.Conflict, "bot: work operation conflicts")
	}
	if old.Outcome != "" && old != op {
		return errorcode.New(errorcode.Conflict, "bot: work operation already settled")
	}
	return s.db.Replace(ctx, "operation", id, op)
}

// Operation looks up a dispatch anchor without beginning or replaying a request.
func (s *WorkStore) Operation(ctx context.Context, principalID, botID, operationID string) (WorkOperation, error) {
	var op WorkOperation
	err := s.db.Get(ctx, "operation", opaqueID("work-operation-", principalID, operationID), &op)
	if err != nil {
		return op, err
	}
	if op.PrincipalID != principalID || op.BotID != botID {
		return WorkOperation{}, errorcode.New(errorcode.PermissionDenied, "bot: operation is not owned")
	}
	if op.Outcome == "" {
		op.Outcome = "unknown"
	}
	return op, nil
}
