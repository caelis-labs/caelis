package file

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	if err := rejectUnsupportedLegacyDocument(data, path); err != nil {
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
	if doc.State != nil {
		doc.State = cloneState(doc.State)
	}
	return doc, nil
}

func rejectUnsupportedLegacyDocument(data []byte, path string) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil
	}
	raw, ok := root["events"]
	if !ok {
		return nil
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "null" {
		return nil
	}
	var events []json.RawMessage
	if err := json.Unmarshal(raw, &events); err != nil {
		return fmt.Errorf("impl/session/file: %w: session document %s has legacy embedded events", session.ErrUnsupportedLegacyFormat, path)
	}
	if len(events) == 0 {
		return nil
	}
	return fmt.Errorf("impl/session/file: %w: session document %s has legacy embedded events", session.ErrUnsupportedLegacyFormat, path)
}

func (s *Store) writeDocument(doc persistedDocument) error {
	doc.Kind = documentKind
	doc.Version = documentVersion
	doc.Session = session.CloneSession(doc.Session)
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
