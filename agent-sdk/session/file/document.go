package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (s *Store) readDocumentForRef(ref session.SessionRef) (persistedDocument, error) {
	normalized := session.NormalizeSessionRef(ref)
	if normalized.SessionID == "" {
		return persistedDocument{}, session.ErrInvalidSession
	}
	doc, err := s.readDocument(normalized.SessionID, normalized.WorkspaceKey)
	if err != nil {
		return persistedDocument{}, err
	}
	if !matchesRef(doc.Session, normalized) {
		return persistedDocument{}, session.ErrSessionNotFound
	}
	return doc, nil
}

func (s *Store) readDocument(sessionID string, workspaceKey ...string) (persistedDocument, error) {
	path, err := s.resolveDocumentPath(sessionID, firstNonEmpty(workspaceKey...))
	if err != nil {
		return persistedDocument{}, err
	}
	// Any retained WAL is a write barrier, even for processes that cached
	// the path before the transaction was published.
	if err := retainedTransactionError(transactionPath(path)); err != nil {
		return persistedDocument{}, err
	}
	doc, err := s.readDocumentAt(path)
	if err == nil && doc.Session.SessionID != sessionID {
		return persistedDocument{}, fmt.Errorf("%w: document %s has a different Session identity", session.ErrUnreadableSession, path)
	}
	return doc, err
}

func (s *Store) readDocumentAt(path string) (persistedDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return persistedDocument{}, fmt.Errorf("%w: missing indexed document %s", session.ErrUnreadableSession, path)
		}
		return persistedDocument{}, err
	}
	doc, err := decodePersistedDocument(data)
	if err != nil {
		return persistedDocument{}, fmt.Errorf("%w: decode %s: %w", session.ErrUnreadableSession, path, err)
	}
	doc.Session = session.CloneSession(doc.Session)
	if doc.State != nil {
		doc.State = cloneState(doc.State)
	}
	doc.PendingApprovals = clonePendingApprovals(doc.PendingApprovals)
	return doc, nil
}

func decodePersistedDocument(data []byte) (persistedDocument, error) {
	var header struct {
		Kind    string          `json:"kind"`
		Version int             `json:"version"`
		Events  json.RawMessage `json:"events"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return persistedDocument{}, err
	}
	if header.Kind == documentKind && (header.Version == 0 || header.Version == 1) {
		return persistedDocument{}, fmt.Errorf("%w: unsupported document version %d", session.ErrUnsupportedLegacyFormat, header.Version)
	}
	if header.Kind != documentKind || header.Version != documentVersion {
		return persistedDocument{}, fmt.Errorf("unsupported document %q version %d", header.Kind, header.Version)
	}
	if len(header.Events) != 0 {
		var events []json.RawMessage
		if json.Unmarshal(header.Events, &events) != nil || len(events) != 0 {
			return persistedDocument{}, fmt.Errorf("%w: session document has legacy embedded events", session.ErrUnsupportedLegacyFormat)
		}
	}
	var doc persistedDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return persistedDocument{}, err
	}
	if strings.TrimSpace(doc.Session.SessionID) == "" || strings.TrimSpace(doc.Session.AppName) == "" || strings.TrimSpace(doc.Session.UserID) == "" {
		return persistedDocument{}, errors.New("missing document Session identity")
	}
	return doc, nil
}

type committedDocumentWriteError struct {
	err error
}

func (e *committedDocumentWriteError) Error() string {
	return e.err.Error()
}

func (e *committedDocumentWriteError) Unwrap() error {
	return e.err
}

func documentWriteCommitted(err error) bool {
	var committed *committedDocumentWriteError
	return errors.As(err, &committed)
}

func committedDocumentWrite(err error) error {
	if err == nil {
		return nil
	}
	return &committedDocumentWriteError{err: err}
}

func (s *Store) writeDocument(ctx context.Context, doc persistedDocument) error {
	return s.writeDocumentInternal(ctx, doc, true, true)
}

func (s *Store) writeDocumentInternal(ctx context.Context, doc persistedDocument, injectFault bool, updateIndex bool) error {
	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return err
	}
	if injectFault && s.writeDocumentFault != nil {
		if err := s.writeDocumentFault(); err != nil {
			return err
		}
	}
	if err := s.writeDocumentAt(ctx, path, doc); err != nil {
		return err
	}
	if updateIndex {
		if err := s.upsertSessionIndex(doc.Session, path); err != nil {
			return committedDocumentWrite(err)
		}
	}
	return nil
}

func (s *Store) writeDocumentAt(ctx context.Context, path string, doc persistedDocument) error {
	doc.Kind = documentKind
	doc.Version = documentVersion
	doc.Session = session.CloneSession(doc.Session)
	doc.State = cloneState(doc.State)
	doc.PendingApprovals = clonePendingApprovals(doc.PendingApprovals)

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("agent-sdk/session/file: encode session %q: %w", doc.Session.SessionID, err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
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
	if err := s.durability.SyncFile(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFile(ctx, s.diagnostics, fileOperationReplaceDocument, tmpName, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return committedDocumentWrite(err)
	}
	if err := s.durability.SyncDirectory(dir); err != nil {
		return committedDocumentWrite(err)
	}
	s.pathCache[doc.Session.SessionID] = path
	return nil
}
