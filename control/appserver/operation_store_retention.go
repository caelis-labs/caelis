package appserver

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// DefaultOperationTerminalRetention is the minimum replay guarantee for a
	// proven terminal Control operation.
	DefaultOperationTerminalRetention = 30 * 24 * time.Hour
	// DefaultOperationSweepInterval limits opportunistic maintenance attempts.
	DefaultOperationSweepInterval = time.Hour
	// DefaultOperationSweepBatchSize bounds records inspected by one maintenance
	// call.
	DefaultOperationSweepBatchSize = 256
	// DefaultOperationSweepDeleteLimit bounds record removals in one call.
	DefaultOperationSweepDeleteLimit = 128
	// DefaultOperationSweepTimeLimit is a soft processing deadline in addition
	// to the hard entry and deletion limits.
	DefaultOperationSweepTimeLimit = 100 * time.Millisecond

	operationRecordSchemaVersion    = 1
	operationRetentionPolicyVersion = 1
	maxOperationStoreJSONSize       = 1 << 20
)

// ErrOperationRetentionPolicyChanged prevents an open store from silently
// mixing its configured guarantee with a newly installed root policy.
var ErrOperationRetentionPolicyChanged = errors.New("controlclient: operation retention policy changed; reopen the store")

// OperationRetentionConfig controls bounded terminal-record maintenance. Zero
// values select the documented defaults; a durable ledger adopts the policy
// persisted by its root, and a later policy change fails an already initialized
// store closed. Negative durations and counts are rejected. Intent-only,
// accepted, unknown, and malformed records never use TerminalRetention.
type OperationRetentionConfig struct {
	TerminalRetention time.Duration
	SweepInterval     time.Duration
	SweepBatchSize    int
	SweepDeleteLimit  int
	SweepTimeLimit    time.Duration
}

type normalizedOperationRetentionConfig struct {
	TerminalRetention time.Duration
	SweepInterval     time.Duration
	SweepBatchSize    int
	SweepDeleteLimit  int
	SweepTimeLimit    time.Duration
}

func normalizeOperationRetentionConfig(config OperationRetentionConfig) (normalizedOperationRetentionConfig, error) {
	if config.TerminalRetention < 0 || config.SweepInterval < 0 ||
		config.SweepBatchSize < 0 || config.SweepDeleteLimit < 0 || config.SweepTimeLimit < 0 {
		return normalizedOperationRetentionConfig{}, errors.New("controlclient: operation retention values must not be negative")
	}
	normalized := normalizedOperationRetentionConfig(config)
	if normalized.TerminalRetention == 0 {
		normalized.TerminalRetention = DefaultOperationTerminalRetention
	}
	if normalized.SweepInterval == 0 {
		normalized.SweepInterval = DefaultOperationSweepInterval
	}
	if normalized.SweepBatchSize == 0 {
		normalized.SweepBatchSize = DefaultOperationSweepBatchSize
	}
	if normalized.SweepDeleteLimit == 0 {
		normalized.SweepDeleteLimit = DefaultOperationSweepDeleteLimit
	}
	if normalized.SweepTimeLimit == 0 {
		normalized.SweepTimeLimit = DefaultOperationSweepTimeLimit
	}
	return normalized, nil
}

// OperationSweepResult is a lightweight maintenance summary. More reports
// that the bounded traversal is not yet confirmed complete.
type OperationSweepResult struct {
	Scanned               int
	RemovedTerminal       int
	RetainedTerminal      int
	RetainedIndeterminate int
	Corrupt               int
	More                  bool
}

type operationRecordDisposition uint8

const (
	operationRecordRetainedTerminal operationRecordDisposition = iota
	operationRecordExpiredTerminal
	operationRecordIndeterminate
)

// Sweep inspects at most one configured batch. It never deletes intent-only,
// accepted, unknown, or malformed records.
func (s *MemoryOperationStore) Sweep(ctx context.Context) (OperationSweepResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return OperationSweepResult{}, err
	}
	now := operationStoreNow(s.now)
	result, err := s.sweepLocked(ctx, now)
	if err == nil && result.More {
		s.nextSweep = now
	} else {
		s.nextSweep = now.Add(s.retention.SweepInterval)
	}
	return result, err
}

func (s *MemoryOperationStore) maybeSweepLocked(ctx context.Context, now time.Time) {
	if !s.nextSweep.IsZero() && now.Before(s.nextSweep) {
		return
	}
	result, err := s.sweepLocked(ctx, now)
	if err == nil && result.More {
		s.nextSweep = now
	} else {
		s.nextSweep = now.Add(s.retention.SweepInterval)
	}
}

func (s *MemoryOperationStore) sweepLocked(ctx context.Context, now time.Time) (OperationSweepResult, error) {
	var result OperationSweepResult
	started := time.Now()
	if s.sweepCursor == nil {
		s.sweepCursor = s.order.Front()
	}
	for s.sweepCursor != nil && result.Scanned < s.retention.SweepBatchSize &&
		result.RemovedTerminal < s.retention.SweepDeleteLimit {
		if err := contextError(ctx); err != nil {
			result.More = true
			return result, err
		}
		if result.Scanned > 0 && s.elapsed(started) >= s.retention.SweepTimeLimit {
			result.More = true
			return result, nil
		}
		current := s.sweepCursor
		s.sweepCursor = current.Next()
		key, _ := current.Value.(string)
		record, ok := s.records[key]
		if !ok {
			continue
		}
		result.Scanned++
		disposition, _, err := classifyOperationRecord(record, now, s.retention.TerminalRetention)
		if err != nil {
			result.Corrupt++
			continue
		}
		switch disposition {
		case operationRecordExpiredTerminal:
			s.removeRecordLocked(key)
			result.RemovedTerminal++
		case operationRecordRetainedTerminal:
			result.RetainedTerminal++
		case operationRecordIndeterminate:
			result.RetainedIndeterminate++
		}
	}
	result.More = s.sweepCursor != nil
	return result, nil
}

func classifyOperationRecord(
	record OperationRecord,
	now time.Time,
	fallbackRetention time.Duration,
) (operationRecordDisposition, time.Time, error) {
	if err := validateOperationRecord(record); err != nil {
		return operationRecordIndeterminate, time.Time{}, err
	}
	if !operationRecordHasReclaimableTerminalOutcome(record) {
		return operationRecordIndeterminate, time.Time{}, nil
	}
	deadline, err := operationRecordRetentionDeadline(record, fallbackRetention)
	if err != nil {
		return operationRecordIndeterminate, time.Time{}, err
	}
	if now.Before(deadline) {
		return operationRecordRetainedTerminal, deadline, nil
	}
	return operationRecordExpiredTerminal, deadline, nil
}

func validateOperationRecord(record OperationRecord) error {
	if record.Version != 0 && record.Version != operationRecordSchemaVersion {
		return fmt.Errorf("controlclient: unsupported operation record version %d", record.Version)
	}
	if strings.TrimSpace(record.Intent.PrincipalID) == "" || strings.TrimSpace(record.Intent.OperationID) == "" ||
		strings.TrimSpace(string(record.Intent.Action)) == "" || strings.TrimSpace(record.Intent.Digest) == "" {
		return errors.New("controlclient: invalid operation record identity")
	}
	if record.Intent.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.Intent.CreatedAt) {
		return errors.New("controlclient: invalid operation record timestamps")
	}
	if record.TerminalRetentionNanoseconds < 0 {
		return errors.New("controlclient: invalid operation record retention")
	}
	if record.Result != nil {
		if strings.TrimSpace(record.Result.OperationID) != strings.TrimSpace(record.Intent.OperationID) || !record.Result.Outcome.Valid() {
			return errors.New("controlclient: invalid operation result")
		}
	}
	switch record.Version {
	case 0:
		if record.TerminalRetentionNanoseconds != 0 || !record.RetainUntil.IsZero() {
			return errors.New("controlclient: legacy operation record contains retention metadata")
		}
	case operationRecordSchemaVersion:
		if record.TerminalRetentionNanoseconds <= 0 {
			return errors.New("controlclient: versioned operation record has no retention snapshot")
		}
		reclaimable := operationRecordHasReclaimableTerminalOutcome(record)
		if reclaimable {
			retention := time.Duration(record.TerminalRetentionNanoseconds)
			if retention <= 0 || record.RetainUntil.IsZero() ||
				!record.RetainUntil.Equal(record.UpdatedAt.Add(retention)) {
				return errors.New("controlclient: inconsistent terminal operation retention metadata")
			}
		} else if !record.RetainUntil.IsZero() {
			return errors.New("controlclient: indeterminate operation has a retention deadline")
		}
	}
	return nil
}

func materializeTerminalRetention(record OperationRecord, fallback time.Duration) (OperationRecord, bool, error) {
	if record.Version != 0 || !operationRecordHasReclaimableTerminalOutcome(record) {
		return record, false, nil
	}
	deadline, err := operationRecordRetentionDeadline(record, fallback)
	if err != nil {
		return OperationRecord{}, false, err
	}
	if record.TerminalRetentionNanoseconds <= 0 {
		record.TerminalRetentionNanoseconds = int64(fallback)
	}
	record.Version = operationRecordSchemaVersion
	record.RetainUntil = deadline
	return record, true, nil
}

func operationRecordRetentionDeadline(record OperationRecord, fallback time.Duration) (time.Time, error) {
	if !record.RetainUntil.IsZero() {
		return record.RetainUntil, nil
	}
	retention := time.Duration(record.TerminalRetentionNanoseconds)
	if retention <= 0 {
		retention = fallback
	}
	if retention <= 0 {
		return time.Time{}, errors.New("controlclient: operation terminal retention is unavailable")
	}
	return record.UpdatedAt.Add(retention), nil
}

func terminalOperationOutcome(outcome Outcome) bool {
	switch outcome {
	case OutcomeCommitted, OutcomeConflicted, OutcomeRejected:
		return true
	default:
		return false
	}
}

func operationRecordHasReclaimableTerminalOutcome(record OperationRecord) bool {
	if record.Result == nil || !terminalOperationOutcome(record.Result.Outcome) {
		return false
	}
	// Before schema v1, unclassified backend failures were persisted as
	// rejected even when their external effect was unknown. Those records are
	// permanent tombstones unless an explicit reconciliation contract is added.
	return record.Version != 0 || record.Result.Outcome != OutcomeRejected
}

func operationStoreNow(now func() time.Time) time.Time {
	if now == nil {
		return time.Now()
	}
	return now()
}

func monotonicOperationTime(now time.Time, prior ...time.Time) time.Time {
	for _, candidate := range prior {
		if now.Before(candidate) {
			now = candidate
		}
	}
	return now
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
