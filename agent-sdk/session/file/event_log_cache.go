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
	maxEventLogCaches     = 8
	maxEventLogCacheBytes = 32 << 20
)

// eventLogCache retains immutable decoded history for a small active-session
// working set. Size accounts retained source bytes and keeps the cache strictly
// bounded; callers still build request-local idempotency maps from Events.
type eventLogCache struct {
	info     os.FileInfo
	size     int64
	modTime  time.Time
	offset   int64
	lineNo   int
	tail     eventPageCheckpoint
	events   []*session.Event
	ids      map[string]struct{}
	byID     map[string]*session.Event
	byKey    map[string]*session.Event
	lastSeq  uint64
	lastUsed uint64
}

func (s *Store) readCachedEventLogContext(ctx context.Context, documentPath string) ([]*session.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path := filepath.Clean(eventLogPath(documentPath))
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.removeEventLogCache(path)
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}

	cache, usable, err := s.usableEventLogCache(ctx, file, path, info)
	if err != nil {
		return nil, err
	}
	if usable && info.Size() == cache.size && info.ModTime().Equal(cache.modTime) {
		s.touchEventLogCache(cache)
		return cache.events, nil
	}

	startOffset := int64(0)
	startLineNo := 0
	lastCheckpoint := eventPageCheckpoint{}
	var prefix []*session.Event
	if usable {
		startOffset = cache.offset
		startLineNo = cache.lineNo
		lastCheckpoint = cache.tail
		prefix = cache.events
	}
	tail, endOffset, endLineNo, lastCheckpoint, err := s.readEventLogTailContext(
		ctx, file, path, startOffset, startLineNo, lastCheckpoint,
	)
	if err != nil {
		return nil, err
	}
	endInfo, err := os.Stat(path)
	if err != nil {
		s.removeEventLogCache(path)
		s.invalidateEventPageIndex(path)
		return nil, err
	}
	if eventPageFileSnapshotChanged(info, endInfo) {
		s.removeEventLogCache(path)
		s.invalidateEventPageIndex(path)
		return nil, fmt.Errorf("agent-sdk/session/file: event log %s changed during cached read", path)
	}

	events := make([]*session.Event, 0, len(prefix)+len(tail))
	events = append(events, prefix...)
	events = append(events, tail...)
	complete, err := completeEventLogFile(file, endInfo.Size())
	if err != nil {
		return nil, err
	}
	if complete {
		nextCache := &eventLogCache{
			info: info, size: endInfo.Size(), modTime: endInfo.ModTime(),
			offset: endOffset, lineNo: endLineNo, tail: lastCheckpoint, events: events,
		}
		if usable {
			nextCache.ids = cache.ids
			nextCache.byID = cache.byID
			nextCache.byKey = cache.byKey
			nextCache.lastSeq = cache.lastSeq
			indexEventLogCacheTail(nextCache, tail)
		} else {
			indexEventLogCacheTail(nextCache, events)
		}
		s.storeEventLogCache(path, nextCache)
	} else {
		s.removeEventLogCache(path)
	}
	return events, nil
}

func indexEventLogCacheTail(cache *eventLogCache, events []*session.Event) {
	if cache.ids == nil {
		cache.ids = map[string]struct{}{}
	}
	if cache.byID == nil {
		cache.byID = map[string]*session.Event{}
	}
	if cache.byKey == nil {
		cache.byKey = map[string]*session.Event{}
	}
	for _, event := range events {
		if event == nil {
			continue
		}
		if id := strings.TrimSpace(event.ID); id != "" {
			cache.ids[id] = struct{}{}
			cache.byID[id] = event
		}
		if key := strings.TrimSpace(event.IdempotencyKey); key != "" {
			cache.byKey[key] = event
		}
		if event.Seq > cache.lastSeq {
			cache.lastSeq = event.Seq
		}
	}
}

func (s *Store) usableEventLogCache(
	ctx context.Context,
	file *os.File,
	path string,
	info os.FileInfo,
) (*eventLogCache, bool, error) {
	cache := s.eventLogCaches[path]
	if cache == nil {
		return nil, false, nil
	}
	if cache.info == nil || !os.SameFile(cache.info, info) || info.Size() < cache.size ||
		(info.Size() == cache.size && !info.ModTime().Equal(cache.modTime)) ||
		cache.offset != cache.size || cache.offset > info.Size() {
		s.removeEventLogCache(path)
		return nil, false, nil
	}
	if cache.tail.Seq > 0 {
		valid, err := validateEventPageCheckpoint(ctx, file, info.Size(), cache.tail)
		if err != nil {
			return nil, false, err
		}
		if !valid {
			s.removeEventLogCache(path)
			return nil, false, nil
		}
	}
	return cache, true, nil
}

func (s *Store) readEventLogTailContext(
	ctx context.Context,
	file *os.File,
	path string,
	offset int64,
	lineNo int,
	lastCheckpoint eventPageCheckpoint,
) ([]*session.Event, int64, int, eventPageCheckpoint, error) {
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, lineNo, lastCheckpoint, err
	}
	reader := bufio.NewReader(file)
	events := make([]*session.Event, 0)
	for {
		if err := ctx.Err(); err != nil {
			return nil, offset, lineNo, lastCheckpoint, err
		}
		lineStart := offset
		line, readErr := reader.ReadString('\n')
		if len(line) == 0 && errors.Is(readErr, io.EOF) {
			break
		}
		offset += int64(len(line))
		lineNo++
		if s.eventLogLineRead != nil {
			s.eventLogLineRead(path, lineNo, lineStart)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, offset, lineNo, lastCheckpoint, readErr
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if err := rejectUnsupportedLegacyEventLogLine([]byte(trimmed), path, lineNo); err != nil {
				return nil, offset, lineNo, lastCheckpoint, err
			}
			migratedRaw, err := session.MigrateEventJSON(json.RawMessage(trimmed))
			if err != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return nil, offset, lineNo, lastCheckpoint, fmt.Errorf("agent-sdk/session/file: migrate event log %s line %d: %w", path, lineNo, err)
			}
			var event session.Event
			if err := json.Unmarshal(migratedRaw, &event); err != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return nil, offset, lineNo, lastCheckpoint, fmt.Errorf("agent-sdk/session/file: decode event log %s line %d: %w", path, lineNo, err)
			}
			if err := session.ValidateDurableCoreEvent(&event); err != nil {
				return nil, offset, lineNo, lastCheckpoint, fmt.Errorf("agent-sdk/session/file: invalid event log %s line %d: %w", path, lineNo, err)
			}
			events = append(events, session.CloneEvent(&event))
			if event.Seq > 0 {
				lastCheckpoint = newEventPageCheckpoint(event.Seq, lineStart, offset, lineNo, line)
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return events, offset, lineNo, lastCheckpoint, nil
}

func completeEventLogFile(file *os.File, size int64) (bool, error) {
	if size == 0 {
		return true, nil
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], size-1); err != nil {
		return false, err
	}
	return last[0] == '\n', nil
}

func (s *Store) storeEventLogCache(path string, cache *eventLogCache) {
	path = filepath.Clean(path)
	if s.eventLogCaches == nil {
		s.eventLogCaches = map[string]*eventLogCache{}
	}
	s.removeEventLogCache(path)
	if cache == nil || cache.size < 0 || cache.size > maxEventLogCacheBytes {
		return
	}
	for len(s.eventLogCaches) >= maxEventLogCaches || s.eventLogCacheBytes+cache.size > maxEventLogCacheBytes {
		if !s.evictOldestEventLogCache() {
			return
		}
	}
	s.eventLogCacheClock++
	cache.lastUsed = s.eventLogCacheClock
	s.eventLogCaches[path] = cache
	s.eventLogCacheBytes += cache.size
}

func (s *Store) touchEventLogCache(cache *eventLogCache) {
	s.eventLogCacheClock++
	cache.lastUsed = s.eventLogCacheClock
}

func (s *Store) removeEventLogCache(path string) {
	path = filepath.Clean(path)
	cache := s.eventLogCaches[path]
	if cache == nil {
		return
	}
	delete(s.eventLogCaches, path)
	s.eventLogCacheBytes -= cache.size
	if s.eventLogCacheBytes < 0 {
		s.eventLogCacheBytes = 0
	}
}

func (s *Store) evictOldestEventLogCache() bool {
	var oldestPath string
	var oldestClock uint64
	for path, cache := range s.eventLogCaches {
		if oldestPath == "" || cache.lastUsed < oldestClock {
			oldestPath = path
			oldestClock = cache.lastUsed
		}
	}
	if oldestPath == "" {
		return false
	}
	s.removeEventLogCache(oldestPath)
	return true
}

func (s *Store) cachedAppendPreparationInputs(
	doc persistedDocument,
	existing []*session.Event,
	incoming []*session.Event,
) ([]*session.Event, map[string]struct{}, uint64, bool) {
	documentPath, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return nil, nil, 0, false
	}
	cache := s.eventLogCaches[filepath.Clean(eventLogPath(documentPath))]
	if cache == nil || !sameCachedEventSlice(cache.events, existing) {
		return nil, nil, 0, false
	}
	relevant := make([]*session.Event, 0, len(incoming)*2)
	seen := map[*session.Event]struct{}{}
	add := func(event *session.Event) {
		if event == nil {
			return
		}
		if _, ok := seen[event]; ok {
			return
		}
		seen[event] = struct{}{}
		relevant = append(relevant, event)
	}
	for _, event := range incoming {
		if event == nil {
			continue
		}
		if id := strings.TrimSpace(event.ID); id != "" {
			add(cache.byID[id])
		}
		if key := strings.TrimSpace(event.IdempotencyKey); key != "" {
			add(cache.byKey[key])
		}
	}
	return relevant, cache.ids, cache.lastSeq, true
}

func sameCachedEventSlice(cached []*session.Event, existing []*session.Event) bool {
	if len(cached) != len(existing) {
		return false
	}
	if len(cached) == 0 {
		return true
	}
	return cached[0] == existing[0] && cached[len(cached)-1] == existing[len(existing)-1]
}
