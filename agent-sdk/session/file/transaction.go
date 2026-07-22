package file

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	transactionKind    = "caelis.sdk.session.transaction"
	transactionVersion = 1
	transactionSuffix  = ".txn.json"
)

// CommittedError is the file-store alias for the durable post-commit signal.
// Prefer session.CommittedError / session.IsCommitted in new call sites.
type CommittedError = session.CommittedError

type persistedTransaction struct {
	Kind     string            `json:"kind"`
	Version  int               `json:"version"`
	Document persistedDocument `json:"document"`
	Events   []*session.Event  `json:"events"`
}

func transactionPath(documentPath string) string { return documentPath + transactionSuffix }

// writeRecoverableDocumentTransaction commits one document plus any canonical
// events behind a durable WAL. Once the WAL rename succeeds, every later error
// is a committed reporting error: recovery owns completing the document, index,
// and WAL cleanup in that order.
func (s *Store) writeRecoverableDocumentTransaction(doc persistedDocument, events []*session.Event) error {
	if s.writeDocumentFault != nil {
		if err := s.writeDocumentFault(); err != nil {
			return err
		}
	}
	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return err
	}
	txnPath := transactionPath(path)
	record := persistedTransaction{
		Kind: transactionKind, Version: transactionVersion, Document: doc, Events: persistedEvents(events),
	}
	if err := s.writeTransaction(txnPath, record); err != nil {
		return err
	}
	if err := s.injectTransactionFault("after_commit"); err != nil {
		return committedDocumentWrite(err)
	}
	if err := s.applyTransaction(txnPath, record); err != nil {
		return committedDocumentWrite(err)
	}
	if err := s.clearTransactionRecoveryMarker(); err != nil {
		return committedDocumentWrite(err)
	}
	return nil
}

func (s *Store) transactionRecoveryMarkerPath() string {
	return filepath.Join(s.normalizedRootDir(), transactionRecoveryMarkerFilename)
}

func (s *Store) transactionRecoveryPending() (bool, error) {
	_, err := os.Stat(s.transactionRecoveryMarkerPath())
	switch {
	case err == nil:
		return true, nil
	case os.IsNotExist(err):
		return false, nil
	default:
		return false, err
	}
}

func (s *Store) markTransactionRecoveryPending() error {
	root := s.normalizedRootDir()
	if err := os.MkdirAll(root, 0o700); err != nil {
		return err
	}
	path := s.transactionRecoveryMarkerPath()
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.WriteString("pending\n"); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncDir(root)
}

func (s *Store) clearTransactionRecoveryMarker() error {
	path := s.transactionRecoveryMarkerPath()
	if err := os.Remove(path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (s *Store) writeTransaction(path string, record persistedTransaction) error {
	if err := s.markTransactionRecoveryPending(); err != nil {
		return err
	}
	record.Kind = transactionKind
	record.Version = transactionVersion
	record.Document.Session = session.CloneSession(record.Document.Session)
	record.Document.State = cloneState(record.Document.State)
	record.Document.PendingApprovals = clonePendingApprovals(record.Document.PendingApprovals)
	record.Events = session.CloneEvents(record.Events)
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return fmt.Errorf("agent-sdk/session/file: encode transaction: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tmpName, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return committedDocumentWrite(err)
	}
	if err := syncDir(dir); err != nil {
		return committedDocumentWrite(err)
	}
	return nil
}

func (s *Store) recoverTransactions() error {
	if s != nil && s.transactionRecoveryScan != nil {
		s.transactionRecoveryScan()
	}
	root := s.normalizedRootDir()
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), transactionSuffix) {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(paths)
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		record, report, err := decodePersistedTransactionWithReport(data)
		if err != nil {
			return fmt.Errorf("agent-sdk/session/file: decode committed transaction %s: %w", path, err)
		}
		s.recordMigrationReport(report)
		if err := s.applyTransaction(path, record); err != nil {
			return err
		}
	}
	return s.clearTransactionRecoveryMarker()
}

func decodePersistedTransaction(data []byte) (persistedTransaction, error) {
	record, _, err := decodePersistedTransactionWithReport(data)
	return record, err
}

func decodePersistedTransactionWithReport(data []byte) (persistedTransaction, MigrationReport, error) {
	var raw struct {
		Kind     string            `json:"kind"`
		Version  int               `json:"version"`
		Document json.RawMessage   `json:"document"`
		Events   []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return persistedTransaction{}, MigrationReport{}, err
	}
	record := persistedTransaction{Kind: raw.Kind, Version: raw.Version}
	document, report, err := decodePersistedDocumentWithReport(raw.Document)
	if err != nil {
		return persistedTransaction{}, MigrationReport{}, err
	}
	record.Document = document
	record.Events = make([]*session.Event, 0, len(raw.Events))
	for index, eventRaw := range raw.Events {
		migrated, err := session.MigrateEventJSON(eventRaw)
		if err != nil {
			return persistedTransaction{}, MigrationReport{}, fmt.Errorf("migrate event %d: %w", index, err)
		}
		var event session.Event
		if err := json.Unmarshal(migrated, &event); err != nil {
			return persistedTransaction{}, MigrationReport{}, fmt.Errorf("decode event %d: %w", index, err)
		}
		if err := session.ValidateDurableCoreEvent(&event); err != nil {
			return persistedTransaction{}, MigrationReport{}, fmt.Errorf("validate event %d: %w", index, err)
		}
		record.Events = append(record.Events, &event)
	}
	return record, report, nil
}

func (s *Store) applyTransaction(path string, record persistedTransaction) error {
	if record.Kind != transactionKind || record.Version != transactionVersion {
		return fmt.Errorf("agent-sdk/session/file: unsupported transaction %q version %d", record.Kind, record.Version)
	}
	documentPath := strings.TrimSuffix(path, transactionSuffix)
	resolvedPath, err := s.resolveWritePath(record.Document.Session)
	if err != nil {
		return err
	}
	if filepath.Clean(resolvedPath) != filepath.Clean(documentPath) {
		return fmt.Errorf("agent-sdk/session/file: transaction path does not match session identity")
	}
	if err := s.appendMissingTransactionEvents(documentPath, record.Events); err != nil {
		return err
	}
	if err := s.injectTransactionFault("after_event_log"); err != nil {
		return err
	}
	if err := s.writeDocumentInternal(record.Document, false, false); err != nil {
		return err
	}
	if err := s.injectTransactionFault("after_document"); err != nil {
		return err
	}
	// The SQLite index is derived state, but it is the only lookup/listing
	// path. Keep the committed WAL until the corresponding index entry is
	// durable so a restart can repair an index failure without losing the
	// Session or rebuilding the canonical event log.
	if err := s.upsertSessionIndex(record.Document.Session, resolvedPath); err != nil {
		return committedDocumentWrite(err)
	}
	if err := s.injectTransactionFault("after_index"); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	return nil
}

func (s *Store) appendMissingTransactionEvents(documentPath string, events []*session.Event) error {
	// Normal writes already populated the bounded immutable cache while
	// preparing idempotency. Reuse it here so WAL application does not perform a
	// second full migration/validation pass; crash recovery naturally rebuilds
	// once in a fresh Store.
	existing, err := s.readCachedEventLogContext(context.Background(), documentPath)
	if err != nil {
		return err
	}
	byID := make(map[string]*session.Event, len(existing))
	for _, event := range existing {
		if event != nil && strings.TrimSpace(event.ID) != "" {
			byID[strings.TrimSpace(event.ID)] = event
		}
	}
	missing := make([]*session.Event, 0, len(events))
	for _, event := range persistedEvents(events) {
		id := strings.TrimSpace(event.ID)
		if prior := byID[id]; prior != nil {
			if !sameDurableEvent(prior, event) {
				return &session.EventConflictError{SessionID: event.SessionID, EventID: id}
			}
			continue
		}
		missing = append(missing, event)
	}
	if len(missing) == 0 {
		return nil
	}
	_, err = s.appendEventLogTransaction(documentPath, missing)
	return err
}

func sameDurableEvent(left *session.Event, right *session.Event) bool {
	leftData, leftErr := json.Marshal(session.CloneEvent(left))
	rightData, rightErr := json.Marshal(session.CloneEvent(right))
	return leftErr == nil && rightErr == nil && bytes.Equal(leftData, rightData)
}

func (s *Store) injectTransactionFault(phase string) error {
	if s != nil && s.transactionFault != nil {
		return s.transactionFault(strings.TrimSpace(phase))
	}
	return nil
}
