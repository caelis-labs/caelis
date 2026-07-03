package file

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/caelis-labs/caelis/ports/session"
)

func (s *Store) eventsForDocument(doc persistedDocument) ([]*session.Event, error) {
	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return nil, err
	}
	return s.readEventLog(path)
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
			var event session.Event
			if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return nil, fmt.Errorf("impl/session/file: decode event log %s: %w", path, err)
			}
			event = normalizeLegacyEventLogEvent(event)
			if err := session.ValidateDurableCoreEvent(&event); err != nil {
				return nil, fmt.Errorf("impl/session/file: invalid event log %s line %d: %w", path, lineNo, err)
			}
			events = append(events, session.CloneEvent(&event))
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return events, nil
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
			return fmt.Errorf("impl/session/file: %w: event log %s line %d contains legacy semantic field %q", session.ErrUnsupportedLegacyFormat, path, lineNo, key)
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

func normalizeLegacyEventLogEvent(event session.Event) session.Event {
	if session.EventTypeOf(&event) != session.EventTypeCustom || event.Message == nil {
		return event
	}
	if !legacyPluginContextEvent(&event) {
		return event
	}
	event.Type = session.EventTypeContext
	return event
}

func legacyPluginContextEvent(event *session.Event) bool {
	if event == nil {
		return false
	}
	source := strings.TrimSpace(fmt.Sprint(event.Meta["source"]))
	return strings.EqualFold(source, "plugin_hook")
}

func eventLogPath(documentPath string) string {
	return strings.TrimSuffix(documentPath, ".json") + ".events.jsonl"
}
