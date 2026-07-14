package file

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

func (s *Store) eventsForDocument(doc persistedDocument) ([]*session.Event, error) {
	return s.eventsForDocumentContext(context.Background(), doc)
}

func (s *Store) eventsForDocumentContext(ctx context.Context, doc persistedDocument) ([]*session.Event, error) {
	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return nil, err
	}
	return s.readCachedEventLogContext(ctx, path)
}

func (s *Store) appendEventLog(documentPath string, events []*session.Event) error {
	_, err := s.appendEventLogTransaction(documentPath, events)
	return err
}

func (s *Store) appendEventLogTransaction(documentPath string, events []*session.Event) (func() error, error) {
	events = persistedEvents(events)
	if len(events) == 0 {
		return func() error { return nil }, nil
	}
	path := eventLogPath(documentPath)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	if err := truncatePartialEventLogTail(path); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, event := range events {
		if err := encoder.Encode(session.CloneEvent(event)); err != nil {
			return nil, err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		file.Close()
		return nil, err
	}
	written, err := file.Write(buf.Bytes())
	if err != nil || written != buf.Len() {
		if err == nil {
			err = io.ErrShortWrite
		}
		_ = file.Truncate(offset)
		_ = file.Sync()
		file.Close()
		return nil, err
	}
	if err := file.Sync(); err != nil {
		_ = file.Truncate(offset)
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = rollbackEventLogAppend(path, offset)
		return nil, err
	}
	if err := syncDir(dir); err != nil {
		_ = rollbackEventLogAppend(path, offset)
		return nil, err
	}
	return func() error {
		return rollbackEventLogAppend(path, offset)
	}, nil
}

func rollbackEventLogAppend(path string, offset int64) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) && offset == 0 {
			return nil
		}
		return err
	}
	if err := file.Truncate(offset); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}

func (s *Store) readEventLog(documentPath string) ([]*session.Event, error) {
	return s.readEventLogContext(context.Background(), documentPath)
}

func (s *Store) readEventLogContext(ctx context.Context, documentPath string) ([]*session.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	path := eventLogPath(documentPath)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	events := make([]*session.Event, 0)
	lineNo := 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line, readErr := reader.ReadString('\n')
		lineNo++
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			if err := rejectUnsupportedLegacyEventLogLine([]byte(trimmed), path, lineNo); err != nil {
				return nil, err
			}
			migratedRaw, err := session.MigrateEventJSON(json.RawMessage(trimmed))
			if err != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return nil, fmt.Errorf("agent-sdk/session/file: migrate event log %s line %d: %w", path, lineNo, err)
			}
			var event session.Event
			if err := json.Unmarshal(migratedRaw, &event); err != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return nil, fmt.Errorf("agent-sdk/session/file: decode event log %s: %w", path, err)
			}
			if err := session.ValidateDurableCoreEvent(&event); err != nil {
				return nil, fmt.Errorf("agent-sdk/session/file: invalid event log %s line %d: %w", path, lineNo, err)
			}
			events = append(events, session.CloneEvent(&event))
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return events, nil
}

func (s *Store) readEventLogPage(ctx context.Context, documentPath string, req session.EventPageRequest) (session.EventPage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req = session.NormalizeEventPageRequest(req)
	path := eventLogPath(documentPath)
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			s.invalidateEventPageIndex(path)
			return session.EventPage{NextSeq: req.AfterSeq}, nil
		}
		return session.EventPage{}, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return session.EventPage{}, err
	}
	checkpoint, err := s.eventPageStartCheckpoint(ctx, file, path, info, req.AfterSeq)
	if err != nil {
		return session.EventPage{}, err
	}
	if checkpoint.Offset > 0 {
		if _, err := file.Seek(checkpoint.Offset, io.SeekStart); err != nil {
			s.invalidateEventPageIndex(path)
			return session.EventPage{}, err
		}
	}

	out := session.EventPage{NextSeq: req.AfterSeq}
	reader := bufio.NewReader(file)
	lineNo := checkpoint.LineNo
	offset := checkpoint.Offset
	lastConsumed := checkpoint
	for {
		if err := ctx.Err(); err != nil {
			return session.EventPage{}, err
		}
		lineStart := offset
		line, readErr := reader.ReadString('\n')
		offset += int64(len(line))
		lineNo++
		if len(line) > 0 && s.eventPageLineRead != nil {
			s.eventPageLineRead(path, lineNo, lineStart)
		}
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return session.EventPage{}, readErr
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			var header struct {
				Seq uint64 `json:"seq"`
			}
			if err := json.Unmarshal([]byte(trimmed), &header); err != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return session.EventPage{}, fmt.Errorf("agent-sdk/session/file: decode event log %s line %d header: %w", path, lineNo, err)
			}
			// The cursor proves earlier records were already consumed. Parse only
			// their sequence header instead of repeatedly migrating, validating,
			// and cloning every payload on each forward page.
			lineCheckpoint := newEventPageCheckpoint(header.Seq, lineStart, offset, lineNo, line)
			if header.Seq > 0 && header.Seq <= req.AfterSeq {
				lastConsumed = lineCheckpoint
				if errors.Is(readErr, io.EOF) {
					break
				}
				continue
			}
			if err := rejectUnsupportedLegacyEventLogLine([]byte(trimmed), path, lineNo); err != nil {
				return session.EventPage{}, err
			}
			migratedRaw, err := session.MigrateEventJSON(json.RawMessage(trimmed))
			if err != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return session.EventPage{}, fmt.Errorf("agent-sdk/session/file: migrate event log %s line %d: %w", path, lineNo, err)
			}
			var event session.Event
			if err := json.Unmarshal(migratedRaw, &event); err != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return session.EventPage{}, fmt.Errorf("agent-sdk/session/file: decode event log %s line %d: %w", path, lineNo, err)
			}
			if err := session.ValidateDurableCoreEvent(&event); err != nil {
				return session.EventPage{}, fmt.Errorf("agent-sdk/session/file: invalid event log %s line %d: %w", path, lineNo, err)
			}
			if event.Seq > req.AfterSeq && (req.ThroughSeq == 0 || event.Seq <= req.ThroughSeq) {
				if session.EventMatchesPageVisibility(&event, req.Visibility) && len(out.Events) >= req.Limit {
					out.HasMore = true
					break
				}
				out.NextSeq = event.Seq
				lastConsumed = lineCheckpoint
				if session.EventMatchesPageVisibility(&event, req.Visibility) {
					out.Events = append(out.Events, session.CanonicalizeEvent(&event))
				}
			}
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	endInfo, err := os.Stat(path)
	if err != nil {
		s.invalidateEventPageIndex(path)
		return session.EventPage{}, err
	}
	if eventPageFileSnapshotChanged(info, endInfo) {
		s.invalidateEventPageIndex(path)
		return session.EventPage{}, fmt.Errorf("agent-sdk/session/file: event log %s changed during page read", path)
	}
	s.recordEventPageCheckpoint(path, lastConsumed)
	return out, nil
}

func rejectUnsupportedLegacyEventLogLine(data []byte, path string, lineNo int) error {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil
	}
	for _, key := range []string{
		"user_message",
		"assistant_message",
		"system_context",
		"tool_call",
		"tool_result",
	} {
		if raw, ok := root[key]; ok && len(raw) > 0 && strings.TrimSpace(string(raw)) != "null" {
			return fmt.Errorf("agent-sdk/session/file: %w: event log %s line %d contains legacy semantic field %q", session.ErrUnsupportedLegacyFormat, path, lineNo, key)
		}
	}
	return nil
}

func truncatePartialEventLogTail(path string) error {
	file, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	var last [1]byte
	if _, err := file.ReadAt(last[:], size-1); err != nil {
		return err
	}
	if last[0] == '\n' {
		return nil
	}
	const chunkSize = 4096
	buf := make([]byte, chunkSize)
	offset := size
	for offset > 0 {
		n := int64(len(buf))
		if offset < n {
			n = offset
		}
		offset -= n
		chunk := buf[:n]
		if _, err := file.ReadAt(chunk, offset); err != nil && !errors.Is(err, io.EOF) {
			return err
		}
		for i := len(chunk) - 1; i >= 0; i-- {
			if chunk[i] == '\n' {
				if err := file.Truncate(offset + int64(i) + 1); err != nil {
					return err
				}
				return file.Sync()
			}
		}
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	return file.Sync()
}

func (s *Store) readEventLogIDs(documentPath string) (map[string]bool, error) {
	events, err := s.readEventLog(documentPath)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if id := strings.TrimSpace(event.ID); id != "" {
			ids[id] = true
		}
	}
	return ids, nil
}

func eventLogPath(documentPath string) string {
	return strings.TrimSuffix(documentPath, ".json") + ".events.jsonl"
}

// readEventLogCheckpoint scans complete JSONL records from the tail. Memory is
// bounded by one chunk plus the largest event line; ordinary checkpoints read
// only the final durable record and the nearest client-replay record.
func readEventLogCheckpoint(ctx context.Context, path string) (uint64, *session.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil, nil
		}
		return 0, nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, nil, err
	}
	if info.Size() == 0 {
		return 0, nil, nil
	}

	const chunkSize = 64 << 10
	buf := make([]byte, chunkSize)
	var suffix []byte
	position := info.Size()
	tailComplete := false
	var lastByte [1]byte
	if _, err := file.ReadAt(lastByte[:], info.Size()-1); err != nil {
		return 0, nil, err
	}
	tailComplete = lastByte[0] == '\n'
	firstRecord := true
	var throughSeq uint64
	var lastReplay *session.Event

	consume := func(raw []byte) (bool, error) {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			return false, nil
		}
		migrated, err := session.MigrateEventJSON(json.RawMessage(trimmed))
		if err != nil {
			if firstRecord && !tailComplete {
				firstRecord = false
				return false, nil
			}
			return false, fmt.Errorf("agent-sdk/session/file: migrate event checkpoint %s: %w", path, err)
		}
		var event session.Event
		if err := json.Unmarshal(migrated, &event); err != nil {
			if firstRecord && !tailComplete {
				firstRecord = false
				return false, nil
			}
			return false, fmt.Errorf("agent-sdk/session/file: decode event checkpoint %s: %w", path, err)
		}
		firstRecord = false
		if err := session.ValidateDurableCoreEvent(&event); err != nil {
			return false, fmt.Errorf("agent-sdk/session/file: invalid event checkpoint %s: %w", path, err)
		}
		if throughSeq == 0 {
			throughSeq = event.Seq
		}
		if lastReplay == nil && session.IsClientReplayEvent(&event) {
			lastReplay = session.CloneEvent(&event)
		}
		return throughSeq > 0 && lastReplay != nil, nil
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
				return throughSeq, lastReplay, nil
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
	return throughSeq, lastReplay, nil
}
