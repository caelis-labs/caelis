package file

import (
	"bytes"
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

func (s *Store) writeTransaction(path string, record persistedTransaction) error {
	record.Kind = transactionKind
	record.Version = transactionVersion
	record.Document.Session = session.CloneSession(record.Document.Session)
	record.Document.State = cloneState(record.Document.State)
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
	if err := os.Rename(tmpName, path); err != nil {
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
		record, err := decodePersistedTransaction(data)
		if err != nil {
			return fmt.Errorf("agent-sdk/session/file: decode committed transaction %s: %w", path, err)
		}
		if err := s.applyTransaction(path, record); err != nil {
			return err
		}
	}
	return nil
}

func decodePersistedTransaction(data []byte) (persistedTransaction, error) {
	var raw struct {
		Kind     string            `json:"kind"`
		Version  int               `json:"version"`
		Document json.RawMessage   `json:"document"`
		Events   []json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return persistedTransaction{}, err
	}
	record := persistedTransaction{Kind: raw.Kind, Version: raw.Version}
	if err := json.Unmarshal(raw.Document, &record.Document); err != nil {
		return persistedTransaction{}, err
	}
	record.Events = make([]*session.Event, 0, len(raw.Events))
	for index, eventRaw := range raw.Events {
		migrated, err := session.MigrateEventJSON(eventRaw)
		if err != nil {
			return persistedTransaction{}, fmt.Errorf("migrate event %d: %w", index, err)
		}
		var event session.Event
		if err := json.Unmarshal(migrated, &event); err != nil {
			return persistedTransaction{}, fmt.Errorf("decode event %d: %w", index, err)
		}
		if err := session.ValidateDurableCoreEvent(&event); err != nil {
			return persistedTransaction{}, fmt.Errorf("validate event %d: %w", index, err)
		}
		record.Events = append(record.Events, &event)
	}
	return record, nil
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
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	if err := s.upsertSessionIndex(record.Document.Session, resolvedPath); err != nil {
		return committedDocumentWrite(err)
	}
	return nil
}

func (s *Store) appendMissingTransactionEvents(documentPath string, events []*session.Event) error {
	existing, err := s.readEventLog(documentPath)
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
