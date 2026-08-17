package file

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

const (
	maxEventAppendIndexes    = 8
	maxEventAppendIndexBytes = 32 << 20
	eventAppendRecordBytes   = 32
)

// eventAppendIndex retains only event identity and file locations. Large event
// payloads therefore remain on disk while append preparation can still prove
// idempotency and allocate the next sequence without rescanning the full log.
type eventAppendIndex struct {
	info     os.FileInfo
	size     int64
	modTime  time.Time
	offset   int64
	lineNo   int
	tail     eventPageCheckpoint
	byID     map[string]eventAppendRecord
	byKey    map[string]eventAppendRecord
	lastSeq  uint64
	bytes    int64
	lastUsed uint64
}

type eventAppendRecord struct {
	offset int64
	length int
}

type eventAppendPreparation struct {
	existingEvents []*session.Event
	existingIDs    map[string]struct{}
	lastSeq        uint64
}

// eventAppendPrewarm carries immutable append-index snapshots from the
// lock-free preparation phase into the authoritative write and WAL-recovery
// transactions.
type eventAppendPrewarm struct {
	indexes map[string]*eventAppendIndex
}

type eventAppendPrewarmContextKey struct{}

func newEventAppendPrewarm() *eventAppendPrewarm {
	return &eventAppendPrewarm{indexes: map[string]*eventAppendIndex{}}
}

func (p *eventAppendPrewarm) add(documentPath string, index *eventAppendIndex) {
	if p == nil || index == nil {
		return
	}
	if p.indexes == nil {
		p.indexes = map[string]*eventAppendIndex{}
	}
	p.indexes[filepath.Clean(documentPath)] = index
}

func (p *eventAppendPrewarm) indexFor(documentPath string) *eventAppendIndex {
	if p == nil {
		return nil
	}
	return p.indexes[filepath.Clean(documentPath)]
}

func contextWithEventAppendPrewarm(ctx context.Context, prewarm *eventAppendPrewarm) context.Context {
	if prewarm == nil {
		return ctx
	}
	return context.WithValue(ctx, eventAppendPrewarmContextKey{}, prewarm)
}

func eventAppendPrewarmFromContext(ctx context.Context) *eventAppendPrewarm {
	if ctx == nil {
		return nil
	}
	prewarm, _ := ctx.Value(eventAppendPrewarmContextKey{}).(*eventAppendPrewarm)
	return prewarm
}

func (s *Store) prewarmEventAppendIndexContext(
	ctx context.Context,
	ref session.SessionRef,
) (*eventAppendPrewarm, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ref = session.NormalizeSessionRef(ref)
	if ref.SessionID == "" {
		return nil, nil
	}
	prewarm := newEventAppendPrewarm()
	// Path discovery runs before the Store mutex and root write lock so a cold
	// index scan cannot starve lease heartbeats. The concurrent cache avoids a
	// namespace walk on every append; the locked transaction verifies the path.
	pathKey := pathCacheKey(ref.SessionID, ref.WorkspaceKey)
	cachedPath, _ := s.eventAppendPaths.Load(pathKey)
	documentPath, _ := cachedPath.(string)
	if strings.TrimSpace(documentPath) == "" {
		resolved, err := s.findDocumentPath(ref.SessionID, ref.WorkspaceKey)
		if err == nil {
			documentPath = filepath.Clean(resolved)
			s.eventAppendPaths.Store(pathKey, documentPath)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(documentPath) != "" {
		info, err := os.Stat(eventLogPath(documentPath))
		switch {
		case err == nil && info.Size() > s.eventLogCacheLimitBytes():
			index, err := s.readEventAppendIndexContext(ctx, documentPath, nil)
			if err != nil {
				return nil, err
			}
			prewarm.add(documentPath, index)
		case os.IsNotExist(err):
			s.eventAppendPaths.Delete(pathKey)
		case err != nil:
			return nil, err
		}
	}
	if len(prewarm.indexes) == 0 {
		return nil, nil
	}
	return prewarm, nil
}

func (s *Store) eventAppendPreparationContext(
	ctx context.Context,
	doc persistedDocument,
	incoming []*session.Event,
	prewarm *eventAppendPrewarm,
) (eventAppendPreparation, error) {
	documentPath, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return eventAppendPreparation{}, err
	}
	s.eventAppendPaths.Store(pathCacheKey(doc.Session.SessionID, doc.Session.WorkspaceKey), filepath.Clean(documentPath))
	logInfo, statErr := os.Stat(eventLogPath(documentPath))
	if statErr == nil && logInfo.Size() <= s.eventLogCacheLimitBytes() {
		existingEvents, err := s.readCachedEventLogContext(ctx, documentPath)
		if err != nil {
			return eventAppendPreparation{}, err
		}
		if relevant, existingIDs, lastSeq, ok := s.cachedAppendPreparationInputs(doc, existingEvents, incoming); ok {
			return eventAppendPreparation{
				existingEvents: relevant,
				existingIDs:    existingIDs,
				lastSeq:        lastSeq,
			}, nil
		}
	}
	if statErr != nil && !os.IsNotExist(statErr) {
		return eventAppendPreparation{}, statErr
	}
	seed := prewarm.indexFor(documentPath)
	index, err := s.readEventAppendIndexContext(ctx, documentPath, seed)
	if err != nil {
		return eventAppendPreparation{}, err
	}
	prewarm.add(documentPath, index)
	existingIDs := make(map[string]struct{}, len(index.byID))
	for id := range index.byID {
		existingIDs[id] = struct{}{}
	}

	records := make(map[eventAppendRecord]struct{}, len(incoming))
	for _, event := range incoming {
		if event == nil {
			continue
		}
		if id := strings.TrimSpace(event.ID); id != "" {
			if record, ok := index.byID[id]; ok {
				records[record] = struct{}{}
				continue
			}
		}
		if key := strings.TrimSpace(event.IdempotencyKey); key != "" {
			if record, ok := index.byKey[key]; ok {
				records[record] = struct{}{}
			}
		}
	}

	existingEvents := make([]*session.Event, 0, len(records))
	if len(records) > 0 {
		file, err := os.Open(eventLogPath(documentPath))
		if err != nil {
			return eventAppendPreparation{}, err
		}
		defer file.Close()
		for record := range records {
			event, err := readIndexedEvent(file, record)
			if err != nil {
				return eventAppendPreparation{}, err
			}
			existingEvents = append(existingEvents, event)
		}
	}
	return eventAppendPreparation{
		existingEvents: existingEvents,
		existingIDs:    existingIDs,
		lastSeq:        index.lastSeq,
	}, nil
}

func (s *Store) readEventAppendIndexContext(
	ctx context.Context,
	documentPath string,
	seed *eventAppendIndex,
) (*eventAppendIndex, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.eventAppendIndexMu.LockContext(ctx); err != nil {
		return nil, err
	}
	defer s.eventAppendIndexMu.Unlock()
	return s.readEventAppendIndexLocked(ctx, documentPath, seed)
}

func (s *Store) readEventAppendIndexLocked(
	ctx context.Context,
	documentPath string,
	seed *eventAppendIndex,
) (*eventAppendIndex, error) {
	path := filepath.Clean(eventLogPath(documentPath))
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.removeEventAppendIndex(path)
			return newEventAppendIndex(), nil
		}
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	index, usable, err := s.usableEventAppendIndex(ctx, file, path, info, seed)
	if err != nil {
		return nil, err
	}
	if usable && info.Size() == index.size && info.ModTime().Equal(index.modTime) {
		if s.eventAppendIndexes[path] == index {
			s.touchEventAppendIndex(index)
		} else {
			s.storeEventAppendIndex(path, index)
		}
		return index, nil
	}
	if !usable {
		index = newEventAppendIndex()
	} else {
		// Cached and prewarmed snapshots remain immutable for callers that carry
		// them between the lock-free preparation and locked commit phases.
		index = cloneEventAppendIndex(index)
	}
	if _, err := file.Seek(index.offset, io.SeekStart); err != nil {
		s.removeEventAppendIndex(path)
		return nil, err
	}

	snapshotSize := info.Size()
	reader := bufio.NewReader(io.LimitReader(file, snapshotSize-index.offset))
	for {
		if err := ctx.Err(); err != nil {
			s.removeEventAppendIndex(path)
			return nil, err
		}
		lineStart := index.offset
		line, readErr := reader.ReadString('\n')
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		if errors.Is(readErr, io.EOF) && !strings.HasSuffix(line, "\n") {
			// The snapshot may catch an in-flight or crash-truncated append. Retain
			// only its complete prefix; the locked catch-up will observe the final
			// record or the append path will truncate it.
			break
		}
		nextOffset := index.offset + int64(len(line))
		nextLineNo := index.lineNo + 1
		if s.eventLogLineRead != nil {
			s.eventLogLineRead(path, nextLineNo, lineStart)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			s.removeEventAppendIndex(path)
			return nil, readErr
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			event, err := decodeIndexedEvent([]byte(trimmed), path, nextLineNo)
			if err != nil {
				s.removeEventAppendIndex(path)
				return nil, err
			}
			record := eventAppendRecord{offset: lineStart, length: len(line)}
			index.add(event, record)
			if event.Seq > 0 {
				index.tail = newEventPageCheckpoint(event.Seq, lineStart, nextOffset, nextLineNo, line)
			}
		}
		index.offset = nextOffset
		index.lineNo = nextLineNo
		if errors.Is(readErr, io.EOF) {
			break
		}
	}

	endInfo, err := os.Stat(path)
	if err != nil {
		s.removeEventAppendIndex(path)
		return nil, err
	}
	if !os.SameFile(info, endInfo) || endInfo.Size() < index.offset {
		s.removeEventAppendIndex(path)
		return nil, fmt.Errorf("agent-sdk/session/file: event log %s changed during append index read", path)
	}
	if index.tail.Seq > 0 {
		valid, err := validateEventPageCheckpoint(ctx, file, endInfo.Size(), index.tail)
		if err != nil {
			return nil, err
		}
		if !valid {
			s.removeEventAppendIndex(path)
			return nil, fmt.Errorf("agent-sdk/session/file: event log %s prefix changed during append index read", path)
		}
	}
	if index.offset == 0 && snapshotSize > 0 {
		s.removeEventAppendIndex(path)
		return index, nil
	}
	index.info = info
	index.size = index.offset
	index.modTime = info.ModTime()
	s.storeEventAppendIndex(path, index)
	return index, nil
}

func newEventAppendIndex() *eventAppendIndex {
	return &eventAppendIndex{
		byID:  map[string]eventAppendRecord{},
		byKey: map[string]eventAppendRecord{},
	}
}

func cloneEventAppendIndex(index *eventAppendIndex) *eventAppendIndex {
	if index == nil {
		return newEventAppendIndex()
	}
	clone := *index
	clone.byID = make(map[string]eventAppendRecord, len(index.byID))
	for id, record := range index.byID {
		clone.byID[id] = record
	}
	clone.byKey = make(map[string]eventAppendRecord, len(index.byKey))
	for key, record := range index.byKey {
		clone.byKey[key] = record
	}
	return &clone
}

func (i *eventAppendIndex) add(event *session.Event, record eventAppendRecord) {
	if i == nil || event == nil {
		return
	}
	if id := strings.TrimSpace(event.ID); id != "" {
		if _, exists := i.byID[id]; !exists {
			i.bytes += int64(len(id) + eventAppendRecordBytes)
		}
		i.byID[id] = record
	}
	if key := strings.TrimSpace(event.IdempotencyKey); key != "" {
		if _, exists := i.byKey[key]; !exists {
			i.bytes += int64(len(key) + eventAppendRecordBytes)
		}
		i.byKey[key] = record
	}
	if event.Seq > i.lastSeq {
		i.lastSeq = event.Seq
	}
}

func (s *Store) usableEventAppendIndex(
	ctx context.Context,
	file *os.File,
	path string,
	info os.FileInfo,
	seed *eventAppendIndex,
) (*eventAppendIndex, bool, error) {
	index := seed
	if index == nil {
		index = s.eventAppendIndexes[path]
	}
	if index == nil {
		return nil, false, nil
	}
	if index.info == nil || !os.SameFile(index.info, info) || info.Size() < index.size ||
		(info.Size() == index.size && !info.ModTime().Equal(index.modTime)) ||
		index.offset != index.size || index.offset > info.Size() {
		s.removeEventAppendIndex(path)
		return nil, false, nil
	}
	if index.tail.Seq > 0 {
		valid, err := validateEventPageCheckpoint(ctx, file, info.Size(), index.tail)
		if err != nil {
			return nil, false, err
		}
		if !valid {
			s.removeEventAppendIndex(path)
			return nil, false, nil
		}
	}
	return index, true, nil
}

func (s *Store) storeEventAppendIndex(path string, index *eventAppendIndex) {
	path = filepath.Clean(path)
	if s.eventAppendIndexes == nil {
		s.eventAppendIndexes = map[string]*eventAppendIndex{}
	}
	s.removeEventAppendIndex(path)
	if index == nil || index.bytes < 0 || index.bytes > maxEventAppendIndexBytes {
		return
	}
	for len(s.eventAppendIndexes) >= maxEventAppendIndexes || s.eventAppendIndexBytes+index.bytes > maxEventAppendIndexBytes {
		if !s.evictOldestEventAppendIndex() {
			return
		}
	}
	s.eventAppendIndexClock++
	index.lastUsed = s.eventAppendIndexClock
	s.eventAppendIndexes[path] = index
	s.eventAppendIndexBytes += index.bytes
}

func (s *Store) touchEventAppendIndex(index *eventAppendIndex) {
	s.eventAppendIndexClock++
	index.lastUsed = s.eventAppendIndexClock
}

func (s *Store) removeEventAppendIndex(path string) {
	path = filepath.Clean(path)
	index := s.eventAppendIndexes[path]
	if index == nil {
		return
	}
	delete(s.eventAppendIndexes, path)
	s.eventAppendIndexBytes -= index.bytes
	if s.eventAppendIndexBytes < 0 {
		s.eventAppendIndexBytes = 0
	}
}

func (s *Store) evictOldestEventAppendIndex() bool {
	var oldestPath string
	var oldestClock uint64
	for path, index := range s.eventAppendIndexes {
		if oldestPath == "" || index.lastUsed < oldestClock {
			oldestPath = path
			oldestClock = index.lastUsed
		}
	}
	if oldestPath == "" {
		return false
	}
	s.removeEventAppendIndex(oldestPath)
	return true
}

func readIndexedEvent(file *os.File, record eventAppendRecord) (*session.Event, error) {
	if file == nil || record.offset < 0 || record.length <= 0 {
		return nil, fmt.Errorf("agent-sdk/session/file: invalid event append index record")
	}
	data := make([]byte, record.length)
	n, err := file.ReadAt(data, record.offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n != len(data) {
		return nil, io.ErrUnexpectedEOF
	}
	return decodeIndexedEvent([]byte(strings.TrimSpace(string(data))), file.Name(), 0)
}

func decodeIndexedEvent(data []byte, path string, lineNo int) (*session.Event, error) {
	if err := rejectUnsupportedLegacyEventLogLine(data, path, lineNo); err != nil {
		return nil, err
	}
	migratedRaw, err := session.MigrateEventJSON(json.RawMessage(data))
	if err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: migrate event log %s line %d: %w", path, lineNo, err)
	}
	var event session.Event
	if err := json.Unmarshal(migratedRaw, &event); err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: decode event log %s line %d: %w", path, lineNo, err)
	}
	if err := session.ValidateDurableCoreEvent(&event); err != nil {
		return nil, fmt.Errorf("agent-sdk/session/file: invalid event log %s line %d: %w", path, lineNo, err)
	}
	return session.CloneEvent(&event), nil
}
