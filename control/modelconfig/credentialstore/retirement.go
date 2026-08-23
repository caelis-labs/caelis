package credentialstore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// RetirementTransaction removes sources while holding every referenced lock
// until the caller commits or rolls back the configuration change that retires
// their final canonical references.
type RetirementTransaction struct {
	mu          sync.Mutex
	store       *Store
	record      retirementRecord
	receiptPath string
	journalLock *referenceLock
	locks       []*referenceLock
	finished    bool
}

const (
	retirementSchemaVersion = 1
	retirementJournalRef    = "credential-retirement-journal"
)

type retirementRecord struct {
	Version                   int               `json:"version"`
	BaseConfigurationRevision uint64            `json:"base_configuration_revision"`
	Credentials               []retirementEntry `json:"credentials"`
}

type retirementEntry struct {
	Ref      string `json:"ref"`
	Existed  bool   `json:"existed"`
	SHA256   string `json:"sha256,omitempty"`
	Previous []byte `json:"previous,omitempty"`
}

// BeginRetirement removes a batch of API-key sources while retaining all
// per-reference locks. References are sorted so retirement and replacement
// transactions cannot deadlock. Before deleting any source, it persists a
// secure exact-byte recovery receipt tied to the configuration revision on
// which the caller will compare-and-save. Commit removes that receipt after the
// canonical references are dropped; rollback restores the prior records before
// unblocking readers.
func (s *Store) BeginRetirement(ctx context.Context, references []string, baseConfigurationRevision uint64) (*RetirementTransaction, error) {
	if s == nil {
		return nil, fmt.Errorf("control/modelconfig/credentialstore: store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	seen := make(map[string]struct{}, len(references))
	refs := make([]string, 0, len(references))
	for _, raw := range references {
		ref := normalizeReference(raw)
		if err := validateReference(ref); err != nil {
			return nil, err
		}
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	txn := &RetirementTransaction{store: s}
	if len(refs) == 0 {
		return txn, nil
	}
	journalLock, err := s.acquireReferenceLock(ctx, retirementJournalRef)
	if err != nil {
		return nil, err
	}
	txn.journalLock = journalLock
	txn.record = retirementRecord{
		Version:                   retirementSchemaVersion,
		BaseConfigurationRevision: baseConfigurationRevision,
		Credentials:               make([]retirementEntry, 0, len(refs)),
	}
	for _, ref := range refs {
		lock, err := s.acquireReferenceLock(ctx, ref)
		if err != nil {
			return nil, errors.Join(err, txn.releaseLocks())
		}
		txn.locks = append(txn.locks, lock)
	}
	for _, ref := range refs {
		previous, existed, err := s.readCredentialBytesUnlocked(ref)
		if err != nil {
			return nil, errors.Join(err, txn.releaseLocks())
		}
		entry := retirementEntry{Ref: ref, Existed: existed, Previous: previous}
		if existed {
			sum := sha256.Sum256(previous)
			entry.SHA256 = hex.EncodeToString(sum[:])
		}
		txn.record.Credentials = append(txn.record.Credentials, entry)
	}
	receiptPath, err := s.writeRetirementReceipt(txn.record)
	if err != nil {
		return nil, errors.Join(err, txn.releaseLocks())
	}
	txn.receiptPath = receiptPath
	for _, entry := range txn.record.Credentials {
		if err := ctx.Err(); err != nil {
			return nil, replacementFailure(err, txn.restorePreviousAndRemoveReceipt(), txn.releaseLocks())
		}
		if !entry.Existed {
			continue
		}
		if err := s.deleteUnlocked(entry.Ref); err != nil {
			return nil, replacementFailure(err, txn.restorePreviousAndRemoveReceipt(), txn.releaseLocks())
		}
	}
	return txn, nil
}

// Commit accepts the removals, clears their durable recovery receipt, and
// releases the transaction locks.
func (t *RetirementTransaction) Commit() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return nil
	}
	t.finished = true
	return errors.Join(t.removeReceipt(), t.releaseLocks())
}

// Rollback restores every removed source, clears its durable recovery receipt,
// and then releases the retirement locks. It is safe to call more than once.
func (t *RetirementTransaction) Rollback() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return nil
	}
	t.finished = true
	return replacementFailure(nil, t.restorePreviousAndRemoveReceipt(), t.releaseLocks())
}

func (t *RetirementTransaction) restorePreviousAndRemoveReceipt() error {
	if t == nil || t.store == nil {
		return nil
	}
	var restoreErrs []error
	for _, entry := range t.record.Credentials {
		if entry.Existed {
			restoreErrs = append(restoreErrs, atomicWrite(t.store.path(entry.Ref), entry.Previous))
		} else {
			restoreErrs = append(restoreErrs, t.store.deleteUnlocked(entry.Ref))
		}
	}
	if restoreErr := errors.Join(restoreErrs...); restoreErr != nil {
		return restoreErr
	}
	return t.removeReceipt()
}

func (t *RetirementTransaction) removeReceipt() error {
	if t == nil || strings.TrimSpace(t.receiptPath) == "" {
		return nil
	}
	err := os.Remove(t.receiptPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (t *RetirementTransaction) releaseLocks() error {
	if t == nil {
		return nil
	}
	var errs []error
	for index := len(t.locks) - 1; index >= 0; index-- {
		errs = append(errs, t.locks[index].Close())
	}
	t.locks = nil
	if t.journalLock != nil {
		errs = append(errs, t.journalLock.Close())
		t.journalLock = nil
	}
	return errors.Join(errs...)
}

// RecoverRetirements reconciles durable receipts left by an interrupted
// retirement. The callback is invoked only after the receipt's sorted reference
// locks are held, and must return the API-key references reachable from one
// current canonical configuration snapshot. A still-reachable reference regains
// its exact prior bytes only when no newer source has appeared; an unreachable
// reference remains retired. Recovery never overwrites or deletes a differing
// source written after the interrupted transaction.
func (s *Store) RecoverRetirements(ctx context.Context, reachable func(context.Context) (map[string]struct{}, error)) (returnErr error) {
	if s == nil {
		return fmt.Errorf("control/modelconfig/credentialstore: store is unavailable")
	}
	if reachable == nil {
		return fmt.Errorf("control/modelconfig/credentialstore: retirement reachability resolver is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	journalLock, err := s.acquireReferenceLock(ctx, retirementJournalRef)
	if err != nil {
		return err
	}
	defer func() {
		returnErr = errors.Join(returnErr, journalLock.Close())
	}()
	if err := s.cleanupRetirementTemps(); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.retirementRoot())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var paths []string
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		paths = append(paths, filepath.Join(s.retirementRoot(), entry.Name()))
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := s.recoverRetirement(ctx, path, reachable); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) recoverRetirement(ctx context.Context, receiptPath string, reachable func(context.Context) (map[string]struct{}, error)) (returnErr error) {
	record, err := s.loadRetirementReceipt(receiptPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	lockedRefs := retirementReferences(record)
	locks := make([]*referenceLock, 0, len(lockedRefs))
	releaseLocks := func() error {
		var errs []error
		for index := len(locks) - 1; index >= 0; index-- {
			errs = append(errs, locks[index].Close())
		}
		return errors.Join(errs...)
	}
	for _, ref := range lockedRefs {
		lock, lockErr := s.acquireReferenceLock(ctx, ref)
		if lockErr != nil {
			return errors.Join(lockErr, releaseLocks())
		}
		locks = append(locks, lock)
	}
	defer func() {
		returnErr = errors.Join(returnErr, releaseLocks())
	}()

	// Another process may have recovered the same receipt while these locks
	// were pending. Re-read it under the reference locks before taking action.
	record, err = s.loadRetirementReceipt(receiptPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !equalStrings(lockedRefs, retirementReferences(record)) {
		return fmt.Errorf("control/modelconfig/credentialstore: retirement receipt references changed while locking")
	}
	references, err := reachable(ctx)
	if err != nil {
		return err
	}
	for _, entry := range record.Credentials {
		_, isReachable := references[entry.Ref]
		if err := s.recoverRetirementEntry(entry, isReachable); err != nil {
			return err
		}
	}
	if err := os.Remove(receiptPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) recoverRetirementEntry(entry retirementEntry, reachable bool) error {
	current, exists, err := s.readCredentialBytesUnlocked(entry.Ref)
	if err != nil {
		return err
	}
	if reachable {
		switch {
		case !entry.Existed:
			return nil
		case !exists:
			return atomicWrite(s.path(entry.Ref), entry.Previous)
		case bytes.Equal(current, entry.Previous):
			return nil
		default:
			// A newer replacement owns the reference; never overwrite it.
			return nil
		}
	}
	if !entry.Existed || !exists || !bytes.Equal(current, entry.Previous) {
		return nil
	}
	return s.deleteUnlocked(entry.Ref)
}

func (s *Store) writeRetirementReceipt(record retirementRecord) (string, error) {
	if err := ensureDir(s.retirementRoot()); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return "", fmt.Errorf("control/modelconfig/credentialstore: encode retirement receipt: %w", err)
	}
	data = append(data, '\n')
	for attempt := 0; attempt < 4; attempt++ {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return "", fmt.Errorf("control/modelconfig/credentialstore: generate retirement receipt ID: %w", err)
		}
		path := filepath.Join(s.retirementRoot(), hex.EncodeToString(nonce[:])+".json")
		if _, err := os.Lstat(path); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if err := atomicWrite(path, data); err != nil {
			return "", err
		}
		return path, nil
	}
	return "", fmt.Errorf("control/modelconfig/credentialstore: allocate unique retirement receipt")
}

func (s *Store) loadRetirementReceipt(path string) (retirementRecord, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return retirementRecord{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return retirementRecord{}, fmt.Errorf("control/modelconfig/credentialstore: retirement receipt is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return retirementRecord{}, err
	}
	var record retirementRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return retirementRecord{}, fmt.Errorf("control/modelconfig/credentialstore: decode retirement receipt: %w", err)
	}
	if err := validateRetirementRecord(record); err != nil {
		return retirementRecord{}, err
	}
	return record, nil
}

func validateRetirementRecord(record retirementRecord) error {
	if record.Version != retirementSchemaVersion || len(record.Credentials) == 0 {
		return fmt.Errorf("control/modelconfig/credentialstore: invalid retirement receipt")
	}
	previousRef := ""
	for _, entry := range record.Credentials {
		if err := validateReference(entry.Ref); err != nil {
			return fmt.Errorf("control/modelconfig/credentialstore: invalid retirement receipt: %w", err)
		}
		if entry.Ref != normalizeReference(entry.Ref) || entry.Ref <= previousRef {
			return fmt.Errorf("control/modelconfig/credentialstore: invalid retirement receipt references")
		}
		previousRef = entry.Ref
		if !entry.Existed {
			if entry.SHA256 != "" || len(entry.Previous) != 0 {
				return fmt.Errorf("control/modelconfig/credentialstore: invalid retirement receipt source")
			}
			continue
		}
		sum := sha256.Sum256(entry.Previous)
		if entry.SHA256 != hex.EncodeToString(sum[:]) {
			return fmt.Errorf("control/modelconfig/credentialstore: invalid retirement receipt digest")
		}
	}
	return nil
}

func retirementReferences(record retirementRecord) []string {
	refs := make([]string, 0, len(record.Credentials))
	for _, entry := range record.Credentials {
		refs = append(refs, entry.Ref)
	}
	return refs
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *Store) readCredentialBytesUnlocked(ref string) ([]byte, bool, error) {
	path := s.path(ref)
	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return nil, false, fmt.Errorf("control/modelconfig/credentialstore: credential path is not a regular file")
		}
		data, readErr := os.ReadFile(path)
		return data, true, readErr
	case errors.Is(err, os.ErrNotExist):
		return nil, false, nil
	default:
		return nil, false, err
	}
}

func (s *Store) cleanupRetirementTemps() error {
	entries, err := os.ReadDir(s.retirementRoot())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".credential.") || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return fmt.Errorf("control/modelconfig/credentialstore: retirement temporary path is not a regular file")
		}
		if err := os.Remove(filepath.Join(s.retirementRoot(), name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Store) retirementRoot() string {
	return filepath.Join(s.root, ".retirements")
}
