package file

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/OnslaughtSnail/caelis/ports/session"
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
	return s.readDocumentAt(path)
}

func (s *Store) readDocumentAt(path string) (persistedDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return persistedDocument{}, session.ErrSessionNotFound
		}
		return persistedDocument{}, err
	}
	var doc persistedDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return persistedDocument{}, fmt.Errorf("impl/session/file: decode %s: %w", path, err)
	}
	if doc.Kind != documentKind || doc.Version != documentVersion {
		return persistedDocument{}, fmt.Errorf(
			"impl/session/file: unsupported document %q version %d",
			doc.Kind,
			doc.Version,
		)
	}
	doc.Session = session.CloneSession(doc.Session)
	doc.Events = session.CloneEvents(doc.Events)
	if doc.State != nil {
		doc.State = cloneState(doc.State)
	}
	return doc, nil
}

func (s *Store) writeDocument(doc persistedDocument) error {
	doc.Kind = documentKind
	doc.Version = documentVersion
	doc.Session = session.CloneSession(doc.Session)
	if err := s.migrateDocumentEventsToLog(&doc); err != nil {
		return err
	}
	doc.Events = nil
	doc.State = cloneState(doc.State)

	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("impl/session/file: encode session %q: %w", doc.Session.SessionID, err)
	}
	data = append(data, '\n')

	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return err
	}
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
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	s.pathCache[pathCacheKey(doc.Session.SessionID, doc.Session.WorkspaceKey)] = path
	if err := s.upsertSessionIndex(doc.Session, path); err != nil {
		return err
	}
	return nil
}
