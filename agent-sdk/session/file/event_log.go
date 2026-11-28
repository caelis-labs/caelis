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

// Long tool results commonly exceed bufio's 4 KiB default. A scan holds this
// read buffer plus scratch space for its largest record.
const eventLogReadBufferSize = 64 << 10

// readEventLogLine borrows the reader buffer for ordinary records and reuses
// scratch space for larger records. The returned bytes are valid until the
// next read.
func readEventLogLine(reader *bufio.Reader, overflow *[]byte) ([]byte, error) {
	line, err := reader.ReadSlice('\n')
	if !errors.Is(err, bufio.ErrBufferFull) {
		return line, err
	}
	*overflow = append((*overflow)[:0], line...)
	for errors.Is(err, bufio.ErrBufferFull) {
		line, err = reader.ReadSlice('\n')
		*overflow = append(*overflow, line...)
	}
	return *overflow, err
}

func (s *Store) eventsForDocumentContext(ctx context.Context, doc persistedDocument) ([]*session.Event, error) {
	path, err := s.resolveWritePath(doc.Session)
	if err != nil {
		return nil, err
	}
	return s.readCachedEventLogContext(ctx, path)
}

// readEventHistory captures the document and its committed log together. Large
// logs cannot use the decoded cache, so scan their complete append-only prefix
// after releasing the Store and root locks that metadata and Control need.
func (s *Store) readEventHistory(ctx context.Context, ref session.SessionRef, limit int, includeTransient bool) (persistedDocument, []*session.Event, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return persistedDocument{}, nil, err
	}
	var doc persistedDocument
	var events []*session.Event
	var log *os.File
	var snapshotSize int64
	defer func() {
		if log != nil {
			_ = log.Close()
		}
	}()
	err := s.withRootLockContext(ctx, func() error {
		var err error
		doc, err = s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		documentPath, err := s.resolveWritePath(doc.Session)
		if err != nil {
			return err
		}
		path := filepath.Clean(eventLogPath(documentPath))
		log, err = os.Open(path)
		if errors.Is(err, os.ErrNotExist) {
			s.removeEventLogCache(path)
			return nil
		}
		if err != nil {
			return err
		}
		info, err := log.Stat()
		if err != nil {
			return err
		}
		if info.Size() > s.eventLogCacheLimitBytes() {
			complete, err := completeEventLogFile(log, info.Size())
			if err != nil {
				return err
			}
			if complete {
				s.removeEventLogCache(path)
				snapshotSize = info.Size()
				return nil
			}
			// An append may truncate a partial tail. Read that log under
			// the recovery barrier so no partial write enters a snapshot.
		}
		events, err = s.readCachedEventLog(ctx, log, path, info)
		return err
	})
	s.mu.Unlock()
	if err != nil {
		return persistedDocument{}, nil, err
	}
	if snapshotSize > 0 {
		if err := s.eventHistoryReadMu.LockContext(ctx); err != nil {
			return persistedDocument{}, nil, err
		}
		defer s.eventHistoryReadMu.Unlock()
		prefix := io.NewSectionReader(log, 0, snapshotSize)
		var offset int64
		events, offset, _, _, err = s.readEventLogTailContext(ctx, prefix, log.Name(), 0, 0, eventPageCheckpoint{})
		if err != nil {
			return persistedDocument{}, nil, err
		}
		if offset != snapshotSize {
			return persistedDocument{}, nil, fmt.Errorf("agent-sdk/session/file: event log %s changed during history read", log.Name())
		}
	}
	return doc, session.FilterEvents(events, limit, includeTransient), nil
}

func (s *Store) appendEventLogTransaction(documentPath string, events []*session.Event) error {
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
	if err := truncatePartialEventLogTail(s.durability, path); err != nil {
		return err
	}
	var buf bytes.Buffer
	for _, event := range events {
		raw, err := session.MarshalEventJSON(event)
		if err != nil {
			return err
		}
		buf.Write(raw)
		buf.WriteByte('\n')
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
		return errors.Join(err, rollbackOpenEventLogAppend(s.durability, file, offset))
	}
	if syncErr := s.durability.SyncFile(file); syncErr != nil {
		return errors.Join(syncErr, rollbackOpenEventLogAppend(s.durability, file, offset))
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return errors.Join(err, rollbackEventLogAppend(s.durability, path, offset))
	}
	if err := s.durability.SyncDirectory(dir); err != nil {
		return errors.Join(err, rollbackEventLogAppend(s.durability, path, offset))
	}
	return nil
}

func rollbackOpenEventLogAppend(durability durabilityOps, file *os.File, offset int64) error {
	// Windows append handles do not reliably grant the access required by
	// Truncate. Close first, then reopen without O_APPEND so rollback has the
	// same durable path on every supported platform.
	path := file.Name()
	closeErr := file.Close()
	return errors.Join(closeErr, rollbackEventLogAppend(durability, path, offset))
}

func rollbackEventLogAppend(durability durabilityOps, path string, offset int64) error {
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
	if err := durability.SyncFile(file); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return durability.SyncDirectory(filepath.Dir(path))
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
	reader := bufio.NewReaderSize(file, eventLogReadBufferSize)
	lineNo := checkpoint.LineNo
	offset := checkpoint.Offset
	lastConsumed := checkpoint
	var overflow []byte
	for {
		if err := ctx.Err(); err != nil {
			return session.EventPage{}, err
		}
		lineStart := offset
		line, readErr := readEventLogLine(reader, &overflow)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return session.EventPage{}, readErr
		}
		offset += int64(len(line))
		lineNo++
		if len(line) > 0 && s.eventPageLineRead != nil {
			s.eventPageLineRead(path, lineNo, lineStart)
		}
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) != 0 {
			var header struct {
				Seq uint64 `json:"seq"`
			}
			if err := json.Unmarshal(trimmed, &header); err != nil {
				return session.EventPage{}, fmt.Errorf("%w: decode event log %s line %d header: %w", session.ErrUnreadableSession, path, lineNo, err)
			}
			// A finite read owns only its durable cut. In particular, history
			// provenance lookups must not decode the entire remaining Session
			// for each individual approval record.
			if req.ThroughSeq > 0 && header.Seq > req.ThroughSeq {
				break
			}
			// The cursor proves earlier records were already consumed. Parse only
			// their sequence header instead of repeatedly migrating, validating,
			// and cloning every payload on each forward page.
			lineCheckpoint := newEventPageCheckpoint(header.Seq, lineStart, offset, lineNo, line)
			if header.Seq > 0 && header.Seq <= req.AfterSeq {
				lastConsumed = lineCheckpoint
				continue
			}
			event, err := decodeEventLogRecord(trimmed, path, lineNo)
			if err != nil {
				return session.EventPage{}, err
			}
			if event.Seq > req.AfterSeq && (req.ThroughSeq == 0 || event.Seq <= req.ThroughSeq) {
				visible := session.EventMatchesPageVisibility(event, req.Visibility)
				if visible && len(out.Events) >= req.Limit {
					out.HasMore = true
					break
				}
				out.NextSeq = event.Seq
				lastConsumed = lineCheckpoint
				if visible {
					out.Events = append(out.Events, session.CanonicalizeEvent(event))
				}
			}
			if req.ThroughSeq > 0 && header.Seq == req.ThroughSeq {
				break
			}
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

func decodeEventLogRecord(data []byte, path string, lineNo int) (*session.Event, error) {
	label := path
	if lineNo > 0 {
		label = fmt.Sprintf("%s line %d", path, lineNo)
	}
	event, err := session.UnmarshalEventJSON(data)
	if err != nil {
		return nil, fmt.Errorf("%w: decode event log %s: %w", session.ErrUnreadableSession, label, err)
	}
	return event, nil
}

func truncatePartialEventLogTail(durability durabilityOps, path string) error {
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
				return durability.SyncFile(file)
			}
		}
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	return durability.SyncFile(file)
}

func eventLogPath(documentPath string) string {
	return strings.TrimSuffix(documentPath, ".json") + ".events.jsonl"
}

// readEventLogCheckpoint reads the durable high-water mark and nearest
// client-replay event from complete records at the tail.
func readEventLogCheckpoint(ctx context.Context, path string) (uint64, *session.Event, error) {
	var throughSeq uint64
	var lastReplay *session.Event
	err := walkEventLogTail(ctx, path, func(raw []byte) (bool, error) {
		event, err := decodeEventLogRecord(raw, path, 0)
		if err != nil {
			return false, err
		}
		if throughSeq == 0 {
			throughSeq = event.Seq
		}
		if lastReplay == nil && session.IsClientReplayEvent(event) {
			lastReplay = session.CloneEvent(event)
		}
		return throughSeq > 0 && lastReplay != nil, nil
	})
	if err != nil {
		return 0, nil, err
	}
	return throughSeq, lastReplay, nil
}

// walkEventLogTail passes complete, nonblank records in reverse order. It holds
// only one read chunk and the largest record, and stops at the visitor's cut.
func walkEventLogTail(ctx context.Context, path string, visit func([]byte) (bool, error)) error {
	if ctx == nil {
		ctx = context.Background()
	}
	file, err := os.Open(path)
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
	if info.Size() == 0 {
		return nil
	}

	const chunkSize = 64 << 10
	buf := make([]byte, chunkSize)
	var suffix []byte
	position := info.Size()
	var lastByte [1]byte
	if _, err := file.ReadAt(lastByte[:], info.Size()-1); err != nil {
		return err
	}
	skipTail := lastByte[0] != '\n'

	consume := func(raw []byte) (bool, error) {
		if skipTail {
			skipTail = false
			return false, nil
		}
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			return false, nil
		}
		return visit(trimmed)
	}

	for position > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		readSize := int64(len(buf))
		if position < readSize {
			readSize = position
		}
		position -= readSize
		chunk := buf[:readSize]
		if _, err := file.ReadAt(chunk, position); err != nil && !errors.Is(err, io.EOF) {
			return err
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
				return err
			}
			if stop {
				return nil
			}
			end = index
		}
		suffix = append(suffix[:0], data[:end]...)
	}
	if len(bytes.TrimSpace(suffix)) > 0 {
		if _, err := consume(suffix); err != nil {
			return err
		}
	}
	return nil
}
