package file

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
func (s *Store) writeRecoverableDocumentTransaction(
	ctx context.Context,
	doc persistedDocument,
	events []*session.Event,
	prewarm *eventAppendPrewarm,
) error {
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
	if err := s.writeTransaction(ctx, txnPath, record); err != nil {
		return err
	}
	if err := s.injectTransactionFault("after_commit"); err != nil {
		return committedDocumentWrite(err)
	}
	appendIndex := prewarm.indexFor(path)
	if err := s.applyTransaction(ctx, txnPath, record, appendIndex); err != nil {
		return committedDocumentWrite(err)
	}
	if err := s.clearTransactionRecoveryMarker(ctx); err != nil {
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
	if err := s.durability.SyncFile(file); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return s.durability.SyncDirectory(root)
}

func (s *Store) clearTransactionRecoveryMarker(ctx context.Context) error {
	path := s.transactionRecoveryMarkerPath()
	if err := removeFile(ctx, s.diagnostics, fileOperationRemoveRecoveryMarker, path); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return s.durability.SyncDirectory(filepath.Dir(path))
}

func (s *Store) writeTransaction(ctx context.Context, path string, record persistedTransaction) error {
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
	if err := s.durability.SyncFile(tmp); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := replaceFile(ctx, s.diagnostics, fileOperationReplaceTransaction, tmpName, path); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return committedDocumentWrite(err)
	}
	if err := s.durability.SyncDirectory(dir); err != nil {
		return committedDocumentWrite(err)
	}
	return nil
}

func (s *Store) recoverTransactions(ctx context.Context) error {
	if s != nil && s.transactionRecoveryScan != nil {
		s.transactionRecoveryScan()
	}
	paths, err := transactionPaths(s.normalizedRootDir())
	if err != nil {
		return err
	}
	prewarm := eventAppendPrewarmFromContext(ctx)
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
		documentPath := strings.TrimSuffix(path, transactionSuffix)
		if err := s.applyTransaction(ctx, path, record, prewarm.indexFor(documentPath)); err != nil {
			return err
		}
	}
	return s.clearTransactionRecoveryMarker(ctx)
}

func transactionPaths(root string) ([]string, error) {
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
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
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

func (s *Store) applyTransaction(
	ctx context.Context,
	path string,
	record persistedTransaction,
	appendIndex *eventAppendIndex,
) error {
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
	if len(record.Events) > 0 {
		if err := s.appendMissingTransactionEvents(documentPath, record.Events, appendIndex); err != nil {
			return err
		}
	}
	if err := s.injectTransactionFault("after_event_log"); err != nil {
		return err
	}
	if err := s.writeDocumentInternal(ctx, record.Document, false, false); err != nil {
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
	if err := removeFile(ctx, s.diagnostics, fileOperationRemoveTransaction, path); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := s.durability.SyncDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return nil
}

func (s *Store) appendMissingTransactionEvents(
	documentPath string,
	events []*session.Event,
	appendIndex *eventAppendIndex,
) error {
	if appendIndex == nil {
		return s.appendMissingTransactionEventsFromTail(documentPath, events)
	}
	if info, err := os.Stat(eventLogPath(documentPath)); err == nil && info.Size() > s.eventLogCacheLimitBytes() {
		return s.appendMissingTransactionEventsIndexed(documentPath, events, appendIndex)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
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

func (s *Store) appendMissingTransactionEventsIndexed(
	documentPath string,
	events []*session.Event,
	appendIndex *eventAppendIndex,
) error {
	index, err := s.readEventAppendIndexContext(context.Background(), documentPath, appendIndex)
	if err != nil {
		return err
	}
	file, err := os.Open(eventLogPath(documentPath))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if file != nil {
		defer file.Close()
	}
	missing := make([]*session.Event, 0, len(events))
	for _, event := range persistedEvents(events) {
		id := strings.TrimSpace(event.ID)
		record, exists := index.byID[id]
		if !exists {
			missing = append(missing, event)
			continue
		}
		prior, err := readIndexedEvent(file, record)
		if err != nil {
			return err
		}
		if !sameDurableEvent(prior, event) {
			return &session.EventConflictError{SessionID: event.SessionID, EventID: id}
		}
	}
	if len(missing) == 0 {
		return nil
	}
	_, err = s.appendEventLogTransaction(documentPath, missing)
	return err
}

func (s *Store) appendMissingTransactionEventsFromTail(documentPath string, events []*session.Event) error {
	events = persistedEvents(events)
	if len(events) == 0 {
		return nil
	}
	firstSeq := events[0].Seq
	throughSeq, bySeq, err := readEventLogTransactionTail(
		context.Background(), eventLogPath(documentPath), firstSeq,
	)
	if err != nil {
		return err
	}
	missing := make([]*session.Event, 0, len(events))
	for _, event := range events {
		prior := bySeq[event.Seq]
		if prior != nil {
			if !sameDurableEvent(prior, event) {
				return &session.EventConflictError{SessionID: event.SessionID, EventID: event.ID}
			}
			continue
		}
		if event.Seq <= throughSeq {
			return &session.EventConflictError{SessionID: event.SessionID, EventID: event.ID}
		}
		missing = append(missing, event)
	}
	if len(missing) == 0 {
		return nil
	}
	if missing[0].Seq != throughSeq+1 {
		return &session.EventConflictError{SessionID: missing[0].SessionID, EventID: missing[0].ID}
	}
	_, err = s.appendEventLogTransaction(documentPath, missing)
	return err
}

// readEventLogTransactionTail reads only the complete suffix that can overlap
// one committed WAL batch. Prepared transaction events have consecutive
// sequence numbers, so recovery can prove presence or absence without
// rebuilding an index over the entire Session history.
func readEventLogTransactionTail(
	ctx context.Context,
	path string,
	firstSeq uint64,
) (uint64, map[uint64]*session.Event, error) {
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, map[uint64]*session.Event{}, nil
		}
		return 0, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, nil, err
	}
	if info.Size() == 0 {
		return 0, map[uint64]*session.Event{}, nil
	}

	const chunkSize = 64 << 10
	buf := make([]byte, chunkSize)
	var suffix []byte
	position := info.Size()
	var lastByte [1]byte
	if _, err := file.ReadAt(lastByte[:], info.Size()-1); err != nil {
		return 0, nil, err
	}
	tailComplete := lastByte[0] == '\n'
	firstRecord := true
	var throughSeq uint64
	bySeq := map[uint64]*session.Event{}

	consume := func(raw []byte) (bool, error) {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			return false, nil
		}
		if firstRecord && !tailComplete {
			firstRecord = false
			return false, nil
		}
		event, err := decodeIndexedEvent(trimmed, path, 0)
		if err != nil {
			return false, err
		}
		firstRecord = false
		if throughSeq == 0 {
			throughSeq = event.Seq
		}
		if event.Seq < firstSeq {
			return true, nil
		}
		bySeq[event.Seq] = event
		return event.Seq == firstSeq, nil
	}

	for position > 0 {
		if err := ctx.Err(); err != nil {
			return 0, nil, err
		}
		readSize := int64(len(buf))
		if position < readSize {
			readSize = position
		}
		position -= readSize
		chunk := buf[:readSize]
		if _, err := file.ReadAt(chunk, position); err != nil && !errors.Is(err, io.EOF) {
			return 0, nil, err
		}
		data := make([]byte, 0, len(chunk)+len(suffix))
		data = append(data, chunk...)
		data = append(data, suffix...)
		end := len(data)
		for index := len(data) - 1; index >= 0; index-- {
			if data[index] != '\n' {
				continue
			}
			stop, err := consume(data[index+1 : end])
			if err != nil {
				return 0, nil, err
			}
			if stop {
				return throughSeq, bySeq, nil
			}
			end = index
		}
		suffix = append(suffix[:0], data[:end]...)
	}
	if len(bytes.TrimSpace(suffix)) > 0 {
		if _, err := consume(suffix); err != nil {
			return 0, nil, err
		}
	}
	return throughSeq, bySeq, nil
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
