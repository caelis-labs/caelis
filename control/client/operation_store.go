package controlclient

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
)

var ErrOperationConflict = errorcode.New(errorcode.Conflict, "controlclient: operation id is bound to another request")

type OperationIntent struct {
	PrincipalID string    `json:"principal_id"`
	OperationID string    `json:"operation_id"`
	Action      Action    `json:"action"`
	SessionID   string    `json:"session_id,omitempty"`
	Target      string    `json:"target,omitempty"`
	Digest      string    `json:"digest"`
	CreatedAt   time.Time `json:"created_at"`
}

type OperationRecord struct {
	Version                      int             `json:"version,omitempty"`
	Intent                       OperationIntent `json:"intent"`
	Result                       *CommandResult  `json:"result,omitempty"`
	TerminalRetentionNanoseconds int64           `json:"terminal_retention_nanoseconds,omitempty"`
	RetainUntil                  time.Time       `json:"retain_until,omitempty"`
	UpdatedAt                    time.Time       `json:"updated_at"`
}

type OperationStore interface {
	Begin(context.Context, OperationIntent) (OperationRecord, bool, error)
	Complete(context.Context, OperationIntent, CommandResult) (OperationRecord, error)
}

type MemoryOperationStore struct {
	mu          sync.Mutex
	records     map[string]OperationRecord
	order       *list.List
	elements    map[string]*list.Element
	sweepCursor *list.Element
	nextSweep   time.Time
	retention   normalizedOperationRetentionConfig
	now         func() time.Time
	elapsed     func(time.Time) time.Duration
}

func NewMemoryOperationStore() *MemoryOperationStore {
	store, err := NewMemoryOperationStoreWithConfig(OperationRetentionConfig{})
	if err != nil {
		panic(err)
	}
	return store
}

// NewMemoryOperationStoreWithConfig constructs an in-memory ledger with the
// same terminal-retention semantics as FileOperationStore.
func NewMemoryOperationStoreWithConfig(config OperationRetentionConfig) (*MemoryOperationStore, error) {
	retention, err := normalizeOperationRetentionConfig(config)
	if err != nil {
		return nil, err
	}
	return &MemoryOperationStore{
		records:   map[string]OperationRecord{},
		order:     list.New(),
		elements:  map[string]*list.Element{},
		retention: retention,
		now:       time.Now,
		elapsed:   time.Since,
	}, nil
}

func (s *MemoryOperationStore) Begin(ctx context.Context, intent OperationIntent) (OperationRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return OperationRecord{}, false, err
	}
	now := operationStoreNow(s.now)
	s.maybeSweepLocked(ctx, now)
	if err := contextError(ctx); err != nil {
		return OperationRecord{}, false, err
	}
	key := operationKey(intent.PrincipalID, intent.OperationID)
	if record, ok := s.records[key]; ok {
		disposition, _, classifyErr := classifyOperationRecord(record, now, s.retention.TerminalRetention)
		if classifyErr != nil {
			return OperationRecord{}, false, classifyErr
		}
		if disposition == operationRecordExpiredTerminal {
			s.removeRecordLocked(key)
		} else {
			if !sameOperationIntent(record.Intent, intent) {
				return OperationRecord{}, false, ErrOperationConflict
			}
			return cloneOperationRecord(record), false, nil
		}
	}
	intent.CreatedAt = now
	record := OperationRecord{
		Version:                      operationRecordSchemaVersion,
		Intent:                       intent,
		TerminalRetentionNanoseconds: int64(s.retention.TerminalRetention),
		UpdatedAt:                    intent.CreatedAt,
	}
	s.records[key] = record
	s.elements[key] = s.order.PushBack(key)
	return cloneOperationRecord(record), true, nil
}

func (s *MemoryOperationStore) Complete(ctx context.Context, intent OperationIntent, result CommandResult) (OperationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return OperationRecord{}, err
	}
	if strings.TrimSpace(result.OperationID) != strings.TrimSpace(intent.OperationID) {
		return OperationRecord{}, ErrOperationConflict
	}
	if !result.Outcome.Valid() {
		return OperationRecord{}, errors.New("controlclient: valid operation outcome is required")
	}
	key := operationKey(intent.PrincipalID, intent.OperationID)
	record, ok := s.records[key]
	if !ok || !sameOperationIntent(record.Intent, intent) {
		return OperationRecord{}, ErrOperationConflict
	}
	if record.Result != nil {
		if *record.Result != result {
			return cloneOperationRecord(record), ErrOperationConflict
		}
		return cloneOperationRecord(record), nil
	}
	copyResult := result
	record.Result = &copyResult
	record.Version = operationRecordSchemaVersion
	if record.TerminalRetentionNanoseconds <= 0 {
		record.TerminalRetentionNanoseconds = int64(s.retention.TerminalRetention)
	}
	record.UpdatedAt = monotonicOperationTime(operationStoreNow(s.now), record.UpdatedAt, record.Intent.CreatedAt)
	if terminalOperationOutcome(result.Outcome) {
		record.RetainUntil = record.UpdatedAt.Add(time.Duration(record.TerminalRetentionNanoseconds))
	} else {
		record.RetainUntil = time.Time{}
	}
	s.records[key] = record
	return cloneOperationRecord(record), nil
}

func (s *MemoryOperationStore) removeRecordLocked(key string) {
	delete(s.records, key)
	if element := s.elements[key]; element != nil {
		if s.sweepCursor == element {
			s.sweepCursor = element.Next()
		}
		s.order.Remove(element)
		delete(s.elements, key)
	}
}

type FileOperationStore struct {
	root                     string
	now                      func() time.Time
	elapsed                  func(time.Time) time.Duration
	retention                normalizedOperationRetentionConfig
	retentionExplicit        bool
	initialized              bool
	effectiveRetention       time.Duration
	effectiveLegacyRetention time.Duration
	syncDirectory            func(string) error
	sweepMu                  sync.Mutex
	scanDirectory            *os.File
	scanPending              []os.DirEntry
	scanEOF                  bool
}

const operationStoreLockFilename = ".operations.lock"

var operationStoreRootLocks sync.Map

type operationStoreRootLock struct {
	once  sync.Once
	token chan struct{}

	maintenanceMu sync.Mutex
	nextSweep     time.Time
}

func (l *operationStoreRootLock) reserveSweep(now time.Time, interval time.Duration) bool {
	l.maintenanceMu.Lock()
	defer l.maintenanceMu.Unlock()
	if !l.nextSweep.IsZero() && now.Before(l.nextSweep) {
		return false
	}
	l.nextSweep = now.Add(interval)
	return true
}

func (l *operationStoreRootLock) markSweep(now time.Time, interval time.Duration, more bool) {
	l.maintenanceMu.Lock()
	if more {
		// A traversal with bounded work remaining is eligible to continue on
		// the next Begin. Each caller still performs at most one batch.
		l.nextSweep = now
	} else {
		l.nextSweep = now.Add(interval)
	}
	l.maintenanceMu.Unlock()
}

func operationStoreRootLockFor(root string) *operationStoreRootLock {
	value, _ := operationStoreRootLocks.LoadOrStore(root, &operationStoreRootLock{})
	return value.(*operationStoreRootLock)
}

func (l *operationStoreRootLock) lock(ctx context.Context) error {
	l.once.Do(func() {
		l.token = make(chan struct{}, 1)
		l.token <- struct{}{}
	})
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-l.token:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *operationStoreRootLock) unlock() {
	l.token <- struct{}{}
}

func NewFileOperationStore(root string) *FileOperationStore {
	store, err := NewFileOperationStoreWithConfig(root, OperationRetentionConfig{})
	if err != nil {
		panic(err)
	}
	return store
}

// NewFileOperationStoreWithConfig constructs a durable operation ledger. Call
// Initialize during application assembly to persist or adopt the root-wide
// retention policy before other processes start using the same root.
func NewFileOperationStoreWithConfig(root string, config OperationRetentionConfig) (*FileOperationStore, error) {
	retention, err := normalizeOperationRetentionConfig(config)
	if err != nil {
		return nil, err
	}
	root = strings.TrimSpace(root)
	if root != "" {
		if absolute, err := filepath.Abs(root); err == nil {
			root = absolute
		}
		root = filepath.Clean(root)
	}
	return &FileOperationStore{
		root:              root,
		now:               time.Now,
		elapsed:           time.Since,
		retention:         retention,
		retentionExplicit: config.TerminalRetention != 0,
		syncDirectory:     syncOperationStoreDirectory,
	}, nil
}

// Initialize persists an explicitly configured root-wide retention policy or
// adopts the existing policy. Later policy changes make an already initialized
// store fail closed until it is reopened.
func (s *FileOperationStore) Initialize(ctx context.Context) error {
	return s.withRootLock(ctx, func() error {
		_, err := s.ensureRetentionPolicyLocked()
		return err
	})
}

// EffectiveTerminalRetention returns the root-wide retention applied to new
// operation records. Initialize must have succeeded before callers publish
// this value to child processes.
func (s *FileOperationStore) EffectiveTerminalRetention(ctx context.Context) (time.Duration, error) {
	var retention time.Duration
	err := s.withRootLock(ctx, func() error {
		var err error
		retention, err = s.ensureRetentionPolicyLocked()
		return err
	})
	return retention, err
}

func (s *FileOperationStore) Begin(ctx context.Context, intent OperationIntent) (OperationRecord, bool, error) {
	s.opportunisticSweep(ctx)
	var record OperationRecord
	var created bool
	err := s.withRootLock(ctx, func() error {
		retention, err := s.ensureRetentionPolicyLocked()
		if err != nil {
			return err
		}
		path := s.path(intent)
		persisted, err := readOperationRecord(path)
		if err == nil {
			if !s.operationRecordMatchesPath(path, persisted) {
				return errors.New("controlclient: operation record path does not match its intent")
			}
			disposition, _, classifyErr := classifyOperationRecord(
				persisted,
				operationStoreNow(s.now),
				s.effectiveLegacyRetention,
			)
			if classifyErr != nil {
				return classifyErr
			}
			if disposition == operationRecordExpiredTerminal {
				if err := s.removeCanonicalRecordLocked(path); err != nil {
					return err
				}
				err = os.ErrNotExist
			} else {
				materialized, changed, err := materializeTerminalRetention(persisted, s.effectiveLegacyRetention)
				if err != nil {
					return err
				}
				if changed {
					if err := s.writeOperationRecord(path, materialized); err != nil {
						return err
					}
					persisted = materialized
				}
				if !sameOperationIntent(persisted.Intent, intent) {
					return ErrOperationConflict
				}
				record = persisted
				return nil
			}
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		intent.CreatedAt = operationStoreNow(s.now)
		record = OperationRecord{
			Version:                      operationRecordSchemaVersion,
			Intent:                       intent,
			TerminalRetentionNanoseconds: int64(retention),
			UpdatedAt:                    intent.CreatedAt,
		}
		if err := s.writeOperationRecord(path, record); err != nil {
			return err
		}
		created = true
		return nil
	})
	if err != nil {
		return OperationRecord{}, false, err
	}
	return cloneOperationRecord(record), created, nil
}

func (s *FileOperationStore) Complete(ctx context.Context, intent OperationIntent, result CommandResult) (OperationRecord, error) {
	if strings.TrimSpace(result.OperationID) != strings.TrimSpace(intent.OperationID) {
		return OperationRecord{}, ErrOperationConflict
	}
	if !result.Outcome.Valid() {
		return OperationRecord{}, errors.New("controlclient: valid operation outcome is required")
	}
	var record OperationRecord
	err := s.withRootLock(ctx, func() error {
		if !s.initialized {
			if _, err := s.ensureRetentionPolicyLocked(); err != nil {
				return err
			}
		}
		path := s.path(intent)
		persisted, err := readOperationRecord(path)
		if err != nil {
			return err
		}
		if !s.operationRecordMatchesPath(path, persisted) {
			return errors.New("controlclient: operation record path does not match its intent")
		}
		if !sameOperationIntent(persisted.Intent, intent) {
			return ErrOperationConflict
		}
		if persisted.Result != nil {
			record = persisted
			if *persisted.Result != result {
				return ErrOperationConflict
			}
			return nil
		}
		copyResult := result
		persisted.Result = &copyResult
		persisted.Version = operationRecordSchemaVersion
		if persisted.TerminalRetentionNanoseconds <= 0 {
			persisted.TerminalRetentionNanoseconds = int64(maxOperationRetention(
				s.effectiveRetention,
				s.effectiveLegacyRetention,
			))
		}
		persisted.UpdatedAt = monotonicOperationTime(operationStoreNow(s.now), persisted.UpdatedAt, persisted.Intent.CreatedAt)
		if terminalOperationOutcome(result.Outcome) {
			persisted.RetainUntil = persisted.UpdatedAt.Add(time.Duration(persisted.TerminalRetentionNanoseconds))
		} else {
			persisted.RetainUntil = time.Time{}
		}
		if err := s.writeOperationRecord(path, persisted); err != nil {
			return err
		}
		record = persisted
		return nil
	})
	if err != nil {
		return cloneOperationRecord(record), err
	}
	return cloneOperationRecord(record), nil
}

func (s *FileOperationStore) withRootLock(ctx context.Context, fn func() error) error {
	if s == nil || strings.TrimSpace(s.root) == "" || s.root == "." {
		return errors.New("controlclient: operation store root is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rootLock := operationStoreRootLockFor(s.root)
	if err := rootLock.lock(ctx); err != nil {
		return err
	}
	defer rootLock.unlock()
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(s.root, 0o700); err != nil {
		return err
	}
	file, err := lockOperationStoreRoot(ctx, s.root)
	if err != nil {
		return err
	}
	defer func() {
		_ = unlockOperationStoreRoot(file)
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	return fn()
}

func (s *FileOperationStore) path(intent OperationIntent) string {
	digest := sha256.Sum256([]byte(operationKey(intent.PrincipalID, intent.OperationID)))
	return filepath.Join(s.root, hex.EncodeToString(digest[:])+".json")
}

func (s *FileOperationStore) operationRecordMatchesPath(path string, record OperationRecord) bool {
	return filepath.Clean(s.path(record.Intent)) == filepath.Clean(path)
}

func readOperationRecord(path string) (OperationRecord, error) {
	data, err := readBoundedOperationStoreJSON(path)
	if err != nil {
		return OperationRecord{}, err
	}
	var record OperationRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return OperationRecord{}, err
	}
	if err := validateOperationRecord(record); err != nil {
		return OperationRecord{}, err
	}
	return cloneOperationRecord(record), nil
}

func (s *FileOperationStore) writeOperationRecord(path string, record OperationRecord) error {
	return writeOperationStoreJSON(path, record, s.syncDirectory)
}

func writeOperationStoreJSON(path string, value any, syncDirectory func(string) error) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxOperationStoreJSONSize {
		return errors.New("controlclient: operation store JSON exceeds size limit")
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".operation-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceOperationStoreFile(tempPath, path); err != nil {
		return err
	}
	if syncDirectory == nil {
		syncDirectory = syncOperationStoreDirectory
	}
	return syncDirectory(filepath.Dir(path))
}

func operationKey(principalID, operationID string) string {
	return strings.TrimSpace(principalID) + "\x00" + strings.TrimSpace(operationID)
}

func sameOperationIntent(left, right OperationIntent) bool {
	return strings.TrimSpace(left.PrincipalID) == strings.TrimSpace(right.PrincipalID) &&
		strings.TrimSpace(left.OperationID) == strings.TrimSpace(right.OperationID) &&
		left.Action == right.Action && strings.TrimSpace(left.SessionID) == strings.TrimSpace(right.SessionID) &&
		strings.TrimSpace(left.Target) == strings.TrimSpace(right.Target) && left.Digest == right.Digest
}

func cloneOperationRecord(in OperationRecord) OperationRecord {
	out := in
	if in.Result != nil {
		result := *in.Result
		out.Result = &result
	}
	return out
}

func requestDigest(request any) (string, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("controlclient: canonical request digest: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
