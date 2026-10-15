package appserver

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

// OperationReader observes the one operation namespace without creating intent,
// refreshing retention, or authorizing another dispatch.
type OperationReader interface {
	Read(context.Context, string, string) (OperationRecord, bool, error)
}

type OperationService interface {
	Operation(context.Context, Principal, string) (CommandResult, error)
}

type OperationClient interface {
	Operation(context.Context, string) (CommandResult, error)
}

type boundOperationClient struct {
	service   OperationService
	principal Principal
}

func (c *boundOperationClient) Operation(ctx context.Context, id string) (CommandResult, error) {
	return c.service.Operation(ctx, c.principal, id)
}

// Operation returns the original receipt even when the Session has since closed
// or changed. Missing/expired records never prove that an effect did not happen.
func (s *CommandService) Operation(ctx context.Context, p Principal, id string) (CommandResult, error) {
	if strings.TrimSpace(p.ID) == "" {
		return CommandResult{}, ErrUnauthorized
	}
	if strings.TrimSpace(id) == "" {
		return CommandResult{}, errorcode.New(errorcode.InvalidArgument, "operation ID is required")
	}
	reader, ok := s.config.Operations.(OperationReader)
	if !ok {
		return CommandResult{}, errorcode.New(errorcode.Unavailable, "operation observation is unavailable")
	}
	record, found, err := reader.Read(ctx, p.ID, id)
	if err != nil {
		return CommandResult{}, err
	}
	if !found {
		return CommandResult{}, errorcode.New(errorcode.NotFound, "operation is absent or outside receipt retention; absence does not authorize replay")
	}
	if record.Result != nil {
		return *record.Result, nil
	}
	if recovery, ok := s.config.Backend.(CommandRecoveryBackend); ok && recovery.CanRecoverControlCommand(record.Intent.Action) {
		// Serialize with the original executor, then re-read: it may have completed
		// while this observer waited. Never overwrite its canonical warning receipt.
		lease, err := s.config.Operations.AcquireExecution(ctx, record.Intent)
		if err != nil {
			return CommandResult{}, err
		}
		defer lease.Release()
		record, found, err = reader.Read(ctx, p.ID, id)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, errorcode.New(errorcode.NotFound, "operation receipt is no longer retained")
		}
		if record.Result != nil {
			return *record.Result, nil
		}
		result, proven, err := recovery.RecoverControlCommand(withOperationIntent(ctx, record.Intent), p, record.Intent, nil)
		if err != nil {
			return CommandResult{}, err
		}
		if proven {
			result.OperationID = record.Intent.OperationID
			if result.SessionID == "" {
				result.SessionID = record.Intent.SessionID
			}
			if !result.Outcome.Valid() {
				result.Outcome = OutcomeCommitted
			}
			completion, cancel := operationCompletionContext(ctx)
			defer cancel()
			if _, err = s.config.Operations.Complete(completion, record.Intent, result); err != nil {
				return CommandResult{}, err
			}
			return result, nil
		}
	}
	return CommandResult{OperationID: record.Intent.OperationID, SessionID: record.Intent.SessionID, Outcome: OutcomeUnknown, Detail: "intent exists without a proven receipt; do not redispatch"}, nil
}

func retainedOperation(record OperationRecord, now func() time.Time, retention time.Duration) (OperationRecord, bool, error) {
	disposition, _, err := classifyOperationRecord(record, operationStoreNow(now), retention)
	if err != nil || disposition == operationRecordExpiredTerminal {
		return OperationRecord{}, false, err
	}
	return cloneOperationRecord(record), true, nil
}

func (s *MemoryOperationStore) Read(ctx context.Context, principal, id string) (OperationRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return OperationRecord{}, false, err
	}
	record, found := s.records[operationKey(principal, id)]
	if !found {
		return OperationRecord{}, false, nil
	}
	return retainedOperation(record, s.now, s.retention.TerminalRetention)
}

func (s *SQLiteOperationStore) Read(ctx context.Context, principal, id string) (OperationRecord, bool, error) {
	retention, err := s.ensureRetentionPolicy(ctx)
	if err != nil {
		return OperationRecord{}, false, err
	}
	record, _, err := s.loadOperationRecord(contextOrBackground(ctx), strings.TrimSpace(principal), strings.TrimSpace(id))
	if errors.Is(err, sql.ErrNoRows) {
		return OperationRecord{}, false, nil
	}
	if err != nil {
		return OperationRecord{}, false, err
	}
	return retainedOperation(record, s.now, retention)
}
