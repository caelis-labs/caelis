package credentialstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
)

// ErrRollbackIncomplete reports that a replacement could not be proven to
// have restored every prior source. Callers must treat the effect as unknown.
var ErrRollbackIncomplete = errors.New("credential replacement rollback is incomplete")

// Replacement describes one API-key source to replace transactionally.
type Replacement struct {
	Ref    string
	Source Source
}

type replacementState struct {
	ref      string
	source   Source
	previous Source
	existed  bool
}

// ReplacementTransaction holds every referenced credential lock until the
// caller commits or rolls back the configuration change that will reference
// the replacement. Lookup, Put, and Delete wait for the same locks.
type ReplacementTransaction struct {
	mu       sync.Mutex
	store    *Store
	states   []replacementState
	locks    []*referenceLock
	finished bool
}

// BeginReplacement writes a batch of API-key replacements while retaining all
// per-reference locks. References are locked in sorted order so overlapping
// batches cannot deadlock.
func (s *Store) BeginReplacement(ctx context.Context, replacements []Replacement) (*ReplacementTransaction, error) {
	if s == nil {
		return nil, fmt.Errorf("control/modelconfig/credentialstore: store is unavailable")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	byRef := make(map[string]Source, len(replacements))
	for _, replacement := range replacements {
		ref := normalizeReference(replacement.Ref)
		if err := validateReference(ref); err != nil {
			return nil, err
		}
		source := Source{APIKey: strings.TrimSpace(replacement.Source.APIKey)}
		if source.APIKey == "" {
			return nil, fmt.Errorf("control/modelconfig/credentialstore: API key is required")
		}
		if previous, ok := byRef[ref]; ok && previous != source {
			return nil, fmt.Errorf("control/modelconfig/credentialstore: conflicting replacements for reference %q", ref)
		}
		byRef[ref] = source
	}
	refs := make([]string, 0, len(byRef))
	for ref := range byRef {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	txn := &ReplacementTransaction{store: s, states: make([]replacementState, 0, len(refs))}
	for _, ref := range refs {
		lock, err := s.acquireReferenceLock(ctx, ref)
		if err != nil {
			return nil, errors.Join(err, txn.releaseLocks())
		}
		txn.locks = append(txn.locks, lock)
	}
	for _, ref := range refs {
		if err := ctx.Err(); err != nil {
			return nil, errors.Join(err, txn.releaseLocks())
		}
		state := replacementState{ref: ref, source: byRef[ref]}
		previous, err := s.lookupSourceUnlocked(ref)
		switch {
		case err == nil:
			state.previous = previous
			state.existed = true
		case errors.Is(err, os.ErrNotExist), errors.Is(err, ErrInvalidCredential):
		default:
			return nil, errors.Join(err, txn.releaseLocks())
		}
		txn.states = append(txn.states, state)
	}
	for index, state := range txn.states {
		if err := ctx.Err(); err != nil {
			return nil, replacementFailure(err, txn.rollbackWrites(index), txn.releaseLocks())
		}
		if err := s.putRecordUnlocked(record{Version: schemaVersion, Ref: state.ref, APIKey: state.source.APIKey}); err != nil {
			// atomicWrite can report a durability error after rename. Restoring
			// the current entry as well is safe whether or not rename occurred.
			return nil, replacementFailure(err, txn.rollbackWrites(index+1), txn.releaseLocks())
		}
	}
	return txn, nil
}

// Commit accepts every replacement and releases the transaction locks.
func (t *ReplacementTransaction) Commit() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return nil
	}
	t.finished = true
	return t.releaseLocks()
}

// Rollback restores the sources observed after all reference locks were held,
// then releases those locks. It is safe to call more than once.
func (t *ReplacementTransaction) Rollback() error {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.finished {
		return nil
	}
	t.finished = true
	rollbackErr := t.rollbackWrites(len(t.states))
	return replacementFailure(nil, rollbackErr, t.releaseLocks())
}

func (t *ReplacementTransaction) rollbackWrites(count int) error {
	if t == nil || t.store == nil {
		return nil
	}
	var errs []error
	for index := count - 1; index >= 0; index-- {
		state := t.states[index]
		if state.existed {
			errs = append(errs, t.store.putRecordUnlocked(record{
				Version: schemaVersion,
				Ref:     state.ref,
				APIKey:  state.previous.APIKey,
			}))
		} else {
			errs = append(errs, t.store.deleteUnlocked(state.ref))
		}
	}
	return errors.Join(errs...)
}

func (t *ReplacementTransaction) releaseLocks() error {
	if t == nil {
		return nil
	}
	var errs []error
	for index := len(t.locks) - 1; index >= 0; index-- {
		errs = append(errs, t.locks[index].Close())
	}
	t.locks = nil
	return errors.Join(errs...)
}

func replacementFailure(cause, rollbackErr, releaseErr error) error {
	if rollbackErr != nil {
		rollbackErr = errors.Join(ErrRollbackIncomplete, rollbackErr)
	}
	return errors.Join(cause, rollbackErr, releaseErr)
}
