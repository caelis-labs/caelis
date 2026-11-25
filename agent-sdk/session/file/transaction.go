package file

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	transactionKind    = "caelis.sdk.session.transaction"
	transactionVersion = 1
	transactionSuffix  = ".txn.json"
)

type persistedTransaction struct {
	Kind     string            `json:"kind"`
	Version  int               `json:"version"`
	Document persistedDocument `json:"document"`
	Events   []*session.Event  `json:"events"`
}

// MarshalJSON uses the same self-contained media encoding in the WAL as in
// JSONL. Recovery never depends on a blob published outside the WAL commit.
func (record persistedTransaction) MarshalJSON() ([]byte, error) {
	type transactionJSON persistedTransaction
	events := make([]json.RawMessage, len(record.Events))
	for i, event := range record.Events {
		raw, err := session.MarshalEventJSON(event)
		if err != nil {
			return nil, err
		}
		events[i] = raw
	}
	return json.Marshal(struct {
		transactionJSON
		Events []json.RawMessage `json:"events"`
	}{transactionJSON: transactionJSON(record), Events: events})
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
	if validate, ok := ctx.Value(idleCommitValidationKey{}).(func() error); ok {
		if err := validate(); err != nil {
			return err
		}
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
	if raw.Kind != transactionKind || raw.Version != transactionVersion {
		return persistedTransaction{}, fmt.Errorf("unsupported transaction %q version %d", raw.Kind, raw.Version)
	}
	record := persistedTransaction{Kind: raw.Kind, Version: raw.Version}
	document, err := decodePersistedDocument(raw.Document)
	record.Document = document
	if err != nil {
		return record, err
	}
	record.Events = make([]*session.Event, 0, len(raw.Events))
	seen := make(map[string]bool, len(raw.Events))
	for index, eventRaw := range raw.Events {
		event, err := session.UnmarshalEventJSON(eventRaw)
		if err != nil {
			return record, fmt.Errorf("decode event %d: %w", index, err)
		}
		if event.SessionID != document.Session.SessionID || event.Seq == 0 || strings.TrimSpace(event.ID) == "" || session.IsTransient(event) || seen[event.ID] {
			return record, fmt.Errorf("invalid transaction event identity at index %d", index)
		}
		if index > 0 && event.Seq != record.Events[index-1].Seq+1 {
			return record, fmt.Errorf("non-consecutive transaction event sequence at index %d", index)
		}
		seen[event.ID] = true
		record.Events = append(record.Events, event)
	}
	return record, nil
}

func (s *Store) applyTransaction(
	ctx context.Context,
	path string,
	record persistedTransaction,
	appendIndex *eventAppendIndex,
) error {
	documentPath := strings.TrimSuffix(path, transactionSuffix)
	if len(record.Events) > 0 {
		if err := s.appendMissingTransactionEvents(documentPath, record.Events, appendIndex); err != nil {
			return err
		}
	}
	if err := s.injectTransactionFault("after_event_log"); err != nil {
		return err
	}
	if err := s.writeDocumentAt(ctx, documentPath, record.Document); err != nil {
		return err
	}
	if err := s.injectTransactionFault("after_document"); err != nil {
		return err
	}
	// The SQLite index is derived state, but it is the only lookup/listing
	// path. Keep the committed WAL until the corresponding index entry is
	// durable so a restart can repair an index failure without losing the
	// Session or rebuilding the canonical event log.
	if err := s.upsertSessionIndex(record.Document.Session, documentPath); err != nil {
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
	return s.appendEventLogTransaction(documentPath, missing)
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
	return s.appendEventLogTransaction(documentPath, missing)
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
	return s.appendEventLogTransaction(documentPath, missing)
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
	var throughSeq uint64
	bySeq := map[uint64]*session.Event{}
	err := walkEventLogTail(ctx, path, func(raw []byte) (bool, error) {
		event, err := decodeEventLogRecord(raw, path, 0)
		if err != nil {
			return false, err
		}
		if throughSeq == 0 {
			throughSeq = event.Seq
		}
		if event.Seq < firstSeq {
			return true, nil
		}
		bySeq[event.Seq] = event
		return event.Seq == firstSeq, nil
	})
	if err != nil {
		return 0, nil, err
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
