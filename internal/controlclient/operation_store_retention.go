package controlclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	controlport "github.com/caelis-labs/caelis/ports/controlclient"
)

const (
	// DefaultOperationTerminalRetention is the minimum replay guarantee for a
	// proven terminal Control operation.
	DefaultOperationTerminalRetention = 30 * 24 * time.Hour
	// DefaultOperationSweepInterval limits opportunistic maintenance attempts.
	DefaultOperationSweepInterval = time.Hour
	// DefaultOperationTempRetention protects recent writer temp files while
	// allowing old crash residue to be reclaimed.
	DefaultOperationTempRetention = 24 * time.Hour
	// DefaultOperationSweepBatchSize bounds directory entries inspected by one
	// maintenance call.
	DefaultOperationSweepBatchSize = 256
	// DefaultOperationSweepDeleteLimit bounds filesystem removals in one call.
	DefaultOperationSweepDeleteLimit = 128
	// DefaultOperationSweepTimeLimit is a soft processing deadline in addition
	// to the hard entry and deletion limits.
	DefaultOperationSweepTimeLimit = 100 * time.Millisecond

	operationRecordSchemaVersion    = 1
	operationRetentionPolicyVersion = 1
	operationRetentionPolicyFile    = ".retention-policy.json"
	maxOperationStoreJSONSize       = 1 << 20
)

// ErrOperationRetentionPolicyChanged prevents an open store from silently
// mixing its configured guarantee with a newly installed root policy.
var ErrOperationRetentionPolicyChanged = errors.New("controlclient: operation retention policy changed; reopen the store")

// OperationRetentionConfig controls bounded terminal-record maintenance. Zero
// values select the documented defaults for memory and fresh file roots; a
// file root with an existing policy adopts it. Negative durations and counts
// are rejected. Intent-only, accepted, unknown, and malformed records never
// use TerminalRetention.
type OperationRetentionConfig struct {
	TerminalRetention time.Duration
	SweepInterval     time.Duration
	TempRetention     time.Duration
	SweepBatchSize    int
	SweepDeleteLimit  int
	SweepTimeLimit    time.Duration
}

type normalizedOperationRetentionConfig struct {
	TerminalRetention time.Duration
	SweepInterval     time.Duration
	TempRetention     time.Duration
	SweepBatchSize    int
	SweepDeleteLimit  int
	SweepTimeLimit    time.Duration
}

func normalizeOperationRetentionConfig(config OperationRetentionConfig) (normalizedOperationRetentionConfig, error) {
	if config.TerminalRetention < 0 || config.SweepInterval < 0 || config.TempRetention < 0 ||
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
	if normalized.TempRetention == 0 {
		normalized.TempRetention = DefaultOperationTempRetention
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
	RemovedTemporary      int
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

type persistedOperationRetentionPolicy struct {
	Version                            int   `json:"version"`
	TerminalRetentionNanoseconds       int64 `json:"terminal_retention_nanoseconds"`
	LegacyTerminalRetentionNanoseconds int64 `json:"legacy_terminal_retention_nanoseconds"`
}

func (s *FileOperationStore) ensureRetentionPolicyLocked() (time.Duration, error) {
	path := filepath.Join(s.root, operationRetentionPolicyFile)
	policy, err := readOperationRetentionPolicy(path)
	switch {
	case err == nil:
		persisted := time.Duration(policy.TerminalRetentionNanoseconds)
		legacy := time.Duration(policy.LegacyTerminalRetentionNanoseconds)
		if s.initialized {
			if persisted != s.effectiveRetention || legacy != s.effectiveLegacyRetention {
				return 0, ErrOperationRetentionPolicyChanged
			}
			return persisted, nil
		}
		if s.retentionExplicit && persisted != s.retention.TerminalRetention {
			policy.TerminalRetentionNanoseconds = int64(s.retention.TerminalRetention)
			policy.LegacyTerminalRetentionNanoseconds = int64(maxOperationRetention(
				legacy,
				persisted,
				s.retention.TerminalRetention,
			))
			if err := writeOperationStoreJSON(path, policy, s.syncDirectory); err != nil {
				return 0, err
			}
			persisted = s.retention.TerminalRetention
			legacy = time.Duration(policy.LegacyTerminalRetentionNanoseconds)
		}
		s.effectiveRetention = persisted
		s.effectiveLegacyRetention = legacy
		s.initialized = true
		return persisted, nil
	case errors.Is(err, os.ErrNotExist):
		if s.initialized {
			return 0, ErrOperationRetentionPolicyChanged
		}
		policy = persistedOperationRetentionPolicy{
			Version:                            operationRetentionPolicyVersion,
			TerminalRetentionNanoseconds:       int64(s.retention.TerminalRetention),
			LegacyTerminalRetentionNanoseconds: int64(s.retention.TerminalRetention),
		}
		if err := writeOperationStoreJSON(path, policy, s.syncDirectory); err != nil {
			return 0, err
		}
		s.effectiveRetention = s.retention.TerminalRetention
		s.effectiveLegacyRetention = s.retention.TerminalRetention
		s.initialized = true
		return s.effectiveRetention, nil
	default:
		return 0, err
	}
}

func readOperationRetentionPolicy(path string) (persistedOperationRetentionPolicy, error) {
	data, err := readBoundedOperationStoreJSON(path)
	if err != nil {
		return persistedOperationRetentionPolicy{}, err
	}
	var policy persistedOperationRetentionPolicy
	if err := json.Unmarshal(data, &policy); err != nil {
		return persistedOperationRetentionPolicy{}, fmt.Errorf("controlclient: decode operation retention policy: %w", err)
	}
	if policy.Version != operationRetentionPolicyVersion || policy.TerminalRetentionNanoseconds <= 0 ||
		policy.LegacyTerminalRetentionNanoseconds < policy.TerminalRetentionNanoseconds {
		return persistedOperationRetentionPolicy{}, errors.New("controlclient: invalid operation retention policy")
	}
	return policy, nil
}

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

// Sweep inspects one bounded directory batch while holding both the in-process
// root lock and the cross-process root lock.
func (s *FileOperationStore) Sweep(ctx context.Context) (OperationSweepResult, error) {
	if err := contextError(ctx); err != nil {
		return OperationSweepResult{}, err
	}
	now := operationStoreNow(s.now)
	result, err := s.sweep(ctx, now)
	operationStoreRootLockFor(s.root).markSweep(now, s.retention.SweepInterval, err == nil && result.More)
	return result, err
}

func (s *FileOperationStore) opportunisticSweep(ctx context.Context) {
	if s == nil || strings.TrimSpace(s.root) == "" || s.root == "." {
		return
	}
	now := operationStoreNow(s.now)
	if !operationStoreRootLockFor(s.root).reserveSweep(now, s.retention.SweepInterval) {
		return
	}
	sweepCtx, cancel := context.WithTimeout(contextOrBackground(ctx), s.retention.SweepTimeLimit)
	defer cancel()
	result, err := s.sweep(sweepCtx, now)
	operationStoreRootLockFor(s.root).markSweep(now, s.retention.SweepInterval, err == nil && result.More)
}

func (s *FileOperationStore) sweep(ctx context.Context, now time.Time) (OperationSweepResult, error) {
	var result OperationSweepResult
	var sweepErr error
	err := s.withRootLock(ctx, func() error {
		s.sweepMu.Lock()
		defer s.sweepMu.Unlock()
		_, err := s.ensureRetentionPolicyLocked()
		if err != nil {
			return err
		}
		return s.sweepDirectoryLocked(ctx, now, s.effectiveLegacyRetention, &result)
	})
	if err != nil {
		sweepErr = errors.Join(sweepErr, err)
	}
	return result, sweepErr
}

func (s *FileOperationStore) sweepDirectoryLocked(
	ctx context.Context,
	now time.Time,
	legacyRetention time.Duration,
	result *OperationSweepResult,
) error {
	started := time.Now()
	var (
		removed      bool
		cleanupPaths []string
		sweepErr     error
	)
	for result.Scanned < s.retention.SweepBatchSize &&
		result.RemovedTerminal+result.RemovedTemporary < s.retention.SweepDeleteLimit {
		if err := contextError(ctx); err != nil {
			sweepErr = errors.Join(sweepErr, err)
			break
		}
		if result.Scanned > 0 && s.elapsed(started) >= s.retention.SweepTimeLimit {
			break
		}
		if len(s.scanPending) == 0 {
			if err := s.fillScanPendingLocked(); err != nil {
				sweepErr = errors.Join(sweepErr, err)
				break
			}
			if len(s.scanPending) == 0 {
				break
			}
		}
		entry := s.scanPending[0]
		s.scanPending = s.scanPending[1:]
		result.Scanned++
		path := filepath.Join(s.root, entry.Name())
		switch {
		case operationRecordFilename(entry.Name()):
			cleanupPath, deleted, err := s.sweepOperationRecordLocked(path, entry.Name(), now, legacyRetention, result)
			if err != nil {
				sweepErr = errors.Join(sweepErr, err)
				continue
			}
			if deleted {
				removed = true
				result.RemovedTerminal++
				if cleanupPath != "" {
					cleanupPaths = append(cleanupPaths, cleanupPath)
				}
			}
		case operationTempFilename(entry.Name()):
			deleted, err := sweepOperationTemp(path, now, s.retention.TempRetention)
			if err != nil {
				sweepErr = errors.Join(sweepErr, err)
				continue
			}
			if deleted {
				removed = true
				result.RemovedTemporary++
			}
		}
	}
	if removed {
		if err := s.syncDirectory(s.root); err != nil {
			sweepErr = errors.Join(sweepErr, err)
		}
	}
	for _, path := range cleanupPaths {
		if err := cleanupRemovedOperationStoreRecord(path); err != nil {
			sweepErr = errors.Join(sweepErr, err)
		}
	}
	completedTraversal := len(s.scanPending) == 0 && s.scanEOF
	if completedTraversal {
		if err := s.closeScanDirectoryLocked(); err != nil {
			sweepErr = errors.Join(sweepErr, err)
		}
	}
	result.More = !completedTraversal
	return sweepErr
}

func (s *FileOperationStore) fillScanPendingLocked() error {
	if len(s.scanPending) > 0 || s.scanEOF {
		return nil
	}
	if s.scanDirectory == nil {
		directory, err := os.Open(s.root)
		if err != nil {
			return err
		}
		s.scanDirectory = directory
	}
	entries, err := s.scanDirectory.ReadDir(s.retention.SweepBatchSize)
	if errors.Is(err, io.EOF) {
		s.scanEOF = true
		err = nil
	}
	if err != nil {
		return err
	}
	s.scanPending = append(s.scanPending, entries...)
	if len(entries) == 0 {
		s.scanEOF = true
	}
	return nil
}

func (s *FileOperationStore) sweepOperationRecordLocked(
	path string,
	filename string,
	now time.Time,
	retention time.Duration,
	result *OperationSweepResult,
) (string, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, nil
		}
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		result.Corrupt++
		return "", false, nil
	}
	record, err := readOperationRecord(path)
	if err != nil || !s.operationRecordMatchesPath(path, record) || filepath.Base(path) != filename {
		result.Corrupt++
		return "", false, nil
	}
	disposition, _, err := classifyOperationRecord(record, now, retention)
	if err != nil {
		result.Corrupt++
		return "", false, nil
	}
	switch disposition {
	case operationRecordExpiredTerminal:
		cleanupPath, err := prepareOperationStoreRecordRemoval(path)
		return cleanupPath, err == nil, err
	case operationRecordRetainedTerminal:
		result.RetainedTerminal++
		materialized, changed, err := materializeTerminalRetention(record, retention)
		if err != nil {
			result.Corrupt++
			return "", false, nil
		}
		if changed {
			if err := s.writeOperationRecord(path, materialized); err != nil {
				return "", false, err
			}
		}
	case operationRecordIndeterminate:
		result.RetainedIndeterminate++
	}
	return "", false, nil
}

func sweepOperationTemp(path string, now time.Time, retention time.Duration) (bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	if !info.Mode().IsRegular() || info.ModTime().IsZero() || now.Before(info.ModTime().Add(retention)) {
		return false, nil
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func (s *FileOperationStore) removeCanonicalRecordLocked(path string) error {
	cleanupPath, err := prepareOperationStoreRecordRemoval(path)
	if err != nil {
		return err
	}
	if err := s.syncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	_ = cleanupRemovedOperationStoreRecord(cleanupPath)
	return nil
}

// Close releases an in-progress bounded directory cursor. It does not disable
// later store use; a later Sweep starts a new traversal.
func (s *FileOperationStore) Close() error {
	if s == nil {
		return nil
	}
	s.sweepMu.Lock()
	defer s.sweepMu.Unlock()
	return s.closeScanDirectoryLocked()
}

func (s *FileOperationStore) closeScanDirectoryLocked() error {
	var err error
	if s.scanDirectory != nil {
		err = s.scanDirectory.Close()
	}
	s.scanDirectory = nil
	s.scanPending = nil
	s.scanEOF = false
	return err
}

func operationRecordFilename(name string) bool {
	if len(name) != 64+len(".json") || !strings.HasSuffix(name, ".json") {
		return false
	}
	for _, char := range strings.TrimSuffix(name, ".json") {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return false
		}
	}
	return true
}

func operationTempFilename(name string) bool {
	return strings.HasPrefix(name, ".operation-") && strings.HasSuffix(name, ".tmp")
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

func terminalOperationOutcome(outcome controlport.Outcome) bool {
	switch outcome {
	case controlport.OutcomeCommitted, controlport.OutcomeConflicted, controlport.OutcomeRejected:
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
	return record.Version != 0 || record.Result.Outcome != controlport.OutcomeRejected
}

func maxOperationRetention(values ...time.Duration) time.Duration {
	var maximum time.Duration
	for _, value := range values {
		if value > maximum {
			maximum = value
		}
	}
	return maximum
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

func readBoundedOperationStoreJSON(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("controlclient: operation store JSON is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return nil, errors.New("controlclient: operation store JSON changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxOperationStoreJSONSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxOperationStoreJSONSize {
		return nil, errors.New("controlclient: operation store JSON exceeds size limit")
	}
	return data, nil
}
