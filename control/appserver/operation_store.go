package appserver

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
	AcquireExecution(context.Context, OperationIntent) (OperationExecutionLease, error)
	Begin(context.Context, OperationIntent) (OperationRecord, bool, error)
	Complete(context.Context, OperationIntent, CommandResult) (OperationRecord, error)
}

// DurableOperationStore is the complete product Host lifecycle for a durable
// operation ledger. Concrete persistence remains an implementation detail of
// Control; callers depend on the idempotency and retention contract.
type DurableOperationStore interface {
	OperationStore
	Initialize(context.Context) error
	EffectiveTerminalRetention(context.Context) (time.Duration, error)
	Sweep(context.Context) (OperationSweepResult, error)
	Close() error
}

// OperationExecutionLease serializes effect execution, observational recovery,
// and receipt completion for one operation identity. Release is idempotent.
type OperationExecutionLease interface {
	Release()
}

type operationExecutionLease struct {
	once    sync.Once
	release func()
}

func (l *operationExecutionLease) Release() {
	if l == nil {
		return
	}
	l.once.Do(l.release)
}

type operationExecutionGate struct {
	token chan struct{}
	refs  int
}

var operationExecutionGates = struct {
	sync.Mutex
	gates map[string]*operationExecutionGate
}{gates: map[string]*operationExecutionGate{}}

func acquireOperationExecutionGate(ctx context.Context, key string) (OperationExecutionLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	operationExecutionGates.Lock()
	gate := operationExecutionGates.gates[key]
	if gate == nil {
		gate = &operationExecutionGate{token: make(chan struct{}, 1)}
		gate.token <- struct{}{}
		operationExecutionGates.gates[key] = gate
	}
	gate.refs++
	operationExecutionGates.Unlock()

	select {
	case <-gate.token:
		if err := ctx.Err(); err != nil {
			gate.token <- struct{}{}
			releaseOperationExecutionGateReference(key, gate)
			return nil, err
		}
		return &operationExecutionLease{release: func() {
			gate.token <- struct{}{}
			releaseOperationExecutionGateReference(key, gate)
		}}, nil
	case <-ctx.Done():
		releaseOperationExecutionGateReference(key, gate)
		return nil, ctx.Err()
	}
}

func releaseOperationExecutionGateReference(key string, gate *operationExecutionGate) {
	operationExecutionGates.Lock()
	defer operationExecutionGates.Unlock()
	gate.refs--
	if gate.refs == 0 && operationExecutionGates.gates[key] == gate {
		delete(operationExecutionGates.gates, key)
	}
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

// NewMemoryOperationStoreWithConfig constructs an in-memory ledger with the same
// terminal-retention semantics as the durable ledger.
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

func (s *MemoryOperationStore) AcquireExecution(ctx context.Context, intent OperationIntent) (OperationExecutionLease, error) {
	key := fmt.Sprintf("memory:%p:%s", s, operationKey(intent.PrincipalID, intent.OperationID))
	return acquireOperationExecutionGate(ctx, key)
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
		if !sameCommandResult(*record.Result, result) {
			return cloneOperationRecord(record), ErrOperationConflict
		}
		return cloneOperationRecord(record), nil
	}
	copyResult := cloneCommandResult(result)
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
		result := cloneCommandResult(*in.Result)
		out.Result = &result
	}
	return out
}

func cloneCommandResult(in CommandResult) CommandResult {
	out := in
	if in.Resource != nil {
		resource := *in.Resource
		out.Resource = &resource
	}
	return out
}

func sameCommandResult(left, right CommandResult) bool {
	leftResource := left.Resource
	rightResource := right.Resource
	left.Resource = nil
	right.Resource = nil
	if left != right {
		return false
	}
	if leftResource == nil || rightResource == nil {
		return leftResource == rightResource
	}
	return *leftResource == *rightResource
}

func requestDigest(request any) (string, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return "", fmt.Errorf("controlclient: canonical request digest: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
