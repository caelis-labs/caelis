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

	"github.com/OnslaughtSnail/caelis/ports/session"
)

func (s *Store) eventsForDocument(doc persistedDocument) ([]*session.Event, error) {
	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return nil, err
	}
	events := persistedEvents(doc.Events)
	logEvents, err := s.readEventLog(path)
	if err != nil {
		return nil, err
	}
	events = append(events, logEvents...)
	return session.CloneEvents(events), nil
}

func (s *Store) migrateDocumentEventsToLog(doc *persistedDocument) error {
	if doc == nil || len(doc.Events) == 0 {
		return nil
	}
	events := persistedEvents(doc.Events)
	doc.Events = nil
	if len(events) == 0 {
		return nil
	}
	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return err
	}
	existing, err := s.readEventLogIDs(path)
	if err != nil {
		return err
	}
	missing := make([]*session.Event, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		id := strings.TrimSpace(event.ID)
		if id != "" && existing[id] {
			continue
		}
		missing = append(missing, event)
	}
	if len(missing) == 0 {
		return nil
	}
	return s.appendEventLog(path, missing)
}

func (s *Store) appendEventLog(documentPath string, events []*session.Event) error {
	events = persistedEvents(events)
	if len(events) == 0 {
		return nil
	}
	path := eventLogPath(documentPath)
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	if err := truncatePartialEventLogTail(path); err != nil {
		return err
	}
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	for _, event := range events {
		if err := encoder.Encode(session.CloneEvent(event)); err != nil {
			return err
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	offset, err := file.Seek(0, io.SeekEnd)
	if err != nil {
		file.Close()
		return err
	}
	written, err := file.Write(buf.Bytes())
	if err != nil || written != buf.Len() {
		if err == nil {
			err = io.ErrShortWrite
		}
		_ = file.Truncate(offset)
		_ = file.Sync()
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Truncate(offset)
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	return syncDir(dir)
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
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, readErr
		}
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			var event session.Event
			if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
				if errors.Is(readErr, io.EOF) {
					break
				}
				return nil, fmt.Errorf("impl/session/file: decode event log %s: %w", path, err)
			}
			events = append(events, session.CloneEvent(&event))
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
	}
	return events, nil
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
