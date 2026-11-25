package file

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/caelis-labs/caelis/agent-sdk/placement"
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
	// The root recovery barrier applies every supported WAL before this
	// read. A verified retired WAL remains the owner's durable write barrier,
	// including for another process that already cached the document path.
	if _, err := os.Stat(transactionPath(path)); err == nil {
		return persistedDocument{}, fmt.Errorf("%w: Session %q has an unsupported committed transaction", session.ErrUnsupportedLegacyFormat, sessionID)
	} else if !os.IsNotExist(err) {
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
	doc, err := decodePersistedDocument(data)
	if err != nil {
		return persistedDocument{}, fmt.Errorf("agent-sdk/session/file: decode %s: %w", path, err)
	}
	doc.Session = session.CloneSession(doc.Session)
	if doc.State != nil {
		doc.State = cloneState(doc.State)
	}
	doc.PendingApprovals = clonePendingApprovals(doc.PendingApprovals)
	return doc, nil
}

func decodePersistedDocument(data []byte) (persistedDocument, error) {
	// Capture the retired inline history shape in the same decode as the
	// document. The WAL reader needs the Session identity even when its history
	// cannot be recovered.
	var wire struct {
		persistedDocument
		Events json.RawMessage `json:"events"`
	}
	err := json.Unmarshal(data, &wire)
	doc := wire.persistedDocument
	if doc.Kind == documentKind && ((doc.Version == 0 && err == nil) || (doc.Version == 1 && err != nil)) {
		return doc, fmt.Errorf("%w: unsupported document version %d or field shape", session.ErrUnsupportedLegacyFormat, doc.Version)
	}
	if err != nil {
		return persistedDocument{}, err
	}
	if doc.Kind != documentKind || (doc.Version != 1 && doc.Version != documentVersion) {
		return persistedDocument{}, fmt.Errorf("agent-sdk/session/file: unsupported document %q version %d", doc.Kind, doc.Version)
	}
	if len(wire.Events) != 0 {
		var events []json.RawMessage
		if json.Unmarshal(wire.Events, &events) != nil || len(events) != 0 {
			return doc, fmt.Errorf("%w: session document has legacy embedded events", session.ErrUnsupportedLegacyFormat)
		}
	}
	if doc.Version == 1 {
		// V1 predates creation-bound execution settings, idle compaction and
		// mandatory sealed ACP placement. Never infer an endpoint on resume.
		doc.Version = documentVersion
		doc.Idle = session.IdleCompactionState{}
		doc.Session.ExecutionConfig = nil
		doc.Session.SessionRef = session.NormalizeSessionRef(doc.Session.SessionRef)
		doc.Session.Participants = slices.DeleteFunc(doc.Session.Participants, func(binding session.ParticipantBinding) bool {
			return binding.Kind == session.ParticipantKindACP && placement.ValidateSealed(binding.Placement) != nil
		})
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

	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return err
	}
	if injectFault && s.writeDocumentFault != nil {
		if err := s.writeDocumentFault(); err != nil {
			return err
		}
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
	s.pathCache[sanitizeSessionID(doc.Session.SessionID)] = path
	if updateIndex {
		if err := s.upsertSessionIndex(doc.Session, path); err != nil {
			return committedDocumentWrite(err)
		}
	}
	return nil
}
