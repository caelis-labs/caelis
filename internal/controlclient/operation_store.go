package controlclient

import (
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

	controlport "github.com/caelis-labs/caelis/ports/controlclient"
)

var ErrOperationConflict = errors.New("controlclient: operation id is bound to another request")

type OperationIntent struct {
	PrincipalID string             `json:"principal_id"`
	OperationID string             `json:"operation_id"`
	Action      controlport.Action `json:"action"`
	SessionID   string             `json:"session_id,omitempty"`
	Target      string             `json:"target,omitempty"`
	Digest      string             `json:"digest"`
	CreatedAt   time.Time          `json:"created_at"`
}

type OperationRecord struct {
	Intent    OperationIntent            `json:"intent"`
	Result    *controlport.CommandResult `json:"result,omitempty"`
	UpdatedAt time.Time                  `json:"updated_at"`
}

type OperationStore interface {
	Begin(context.Context, OperationIntent) (OperationRecord, bool, error)
	Complete(context.Context, OperationIntent, controlport.CommandResult) (OperationRecord, error)
}

type MemoryOperationStore struct {
	mu      sync.Mutex
	records map[string]OperationRecord
	now     func() time.Time
}

func NewMemoryOperationStore() *MemoryOperationStore {
	return &MemoryOperationStore{records: map[string]OperationRecord{}, now: time.Now}
}

func (s *MemoryOperationStore) Begin(_ context.Context, intent OperationIntent) (OperationRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := operationKey(intent.PrincipalID, intent.OperationID)
	if record, ok := s.records[key]; ok {
		if !sameOperationIntent(record.Intent, intent) {
			return OperationRecord{}, false, ErrOperationConflict
		}
		return cloneOperationRecord(record), false, nil
	}
	intent.CreatedAt = s.now()
	record := OperationRecord{Intent: intent, UpdatedAt: intent.CreatedAt}
	s.records[key] = record
	return record, true, nil
}

func (s *MemoryOperationStore) Complete(_ context.Context, intent OperationIntent, result controlport.CommandResult) (OperationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := operationKey(intent.PrincipalID, intent.OperationID)
	record, ok := s.records[key]
	if !ok || !sameOperationIntent(record.Intent, intent) {
		return OperationRecord{}, ErrOperationConflict
	}
	copyResult := result
	record.Result = &copyResult
	record.UpdatedAt = s.now()
	s.records[key] = record
	return cloneOperationRecord(record), nil
}

type FileOperationStore struct {
	root string
	mu   sync.Mutex
	now  func() time.Time
}

func NewFileOperationStore(root string) *FileOperationStore {
	return &FileOperationStore{root: strings.TrimSpace(root), now: time.Now}
}

func (s *FileOperationStore) Begin(_ context.Context, intent OperationIntent) (OperationRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return OperationRecord{}, false, err
	}
	path := s.path(intent)
	record, err := readOperationRecord(path)
	if err == nil {
		if !sameOperationIntent(record.Intent, intent) {
			return OperationRecord{}, false, ErrOperationConflict
		}
		return record, false, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return OperationRecord{}, false, err
	}
	intent.CreatedAt = s.now()
	record = OperationRecord{Intent: intent, UpdatedAt: intent.CreatedAt}
	if err := writeOperationRecord(path, record); err != nil {
		return OperationRecord{}, false, err
	}
	return record, true, nil
}

func (s *FileOperationStore) Complete(_ context.Context, intent OperationIntent, result controlport.CommandResult) (OperationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.path(intent)
	record, err := readOperationRecord(path)
	if err != nil {
		return OperationRecord{}, err
	}
	if !sameOperationIntent(record.Intent, intent) {
		return OperationRecord{}, ErrOperationConflict
	}
	copyResult := result
	record.Result = &copyResult
	record.UpdatedAt = s.now()
	if err := writeOperationRecord(path, record); err != nil {
		return OperationRecord{}, err
	}
	return record, nil
}

func (s *FileOperationStore) path(intent OperationIntent) string {
	digest := sha256.Sum256([]byte(operationKey(intent.PrincipalID, intent.OperationID)))
	return filepath.Join(s.root, hex.EncodeToString(digest[:])+".json")
}

func readOperationRecord(path string) (OperationRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return OperationRecord{}, err
	}
	var record OperationRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return OperationRecord{}, err
	}
	return cloneOperationRecord(record), nil
}

func writeOperationRecord(path string, record OperationRecord) error {
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
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
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
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
