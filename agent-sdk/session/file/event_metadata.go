package file

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// This disposable index stores provenance and byte locations, never transcript
// payloads. It is built only for a requested Session, catches up incrementally,
// and survives Host restart. The canonical log and its WAL remain authoritative.
const eventMetadataIndexVersion = 1
const maxEventMetadataIndexBytes = 32 << 20
const eventMetadataCheckpointStride = 200

type eventMetadataRecord struct {
	session.EventMetadata
	Offset      int64
	Length      int
	Line        int
	Canonical   bool
	Replay      bool
	RunID       string `json:",omitempty"`
	RunRevision uint64 `json:",omitempty"`
	PauseRunID  string `json:",omitempty"`
}

type eventMetadataIndex struct {
	Version int
	Size    int64
	ModTime int64
	Tail    eventPageCheckpoint
	Records []eventMetadataRecord
	info    os.FileInfo
	bytes   int64
}

type eventMetadataDisk struct {
	Data json.RawMessage
	Hash [sha256.Size]byte
}

// EventMetadataPage reads bounded provenance, leaving content and window policy
// to their existing owners. Cached byte locations also accelerate EventsPage.
func (s *Store) EventMetadataPage(ctx context.Context, req session.EventPageRequest) (session.EventMetadataPage, error) {
	req = session.NormalizeEventPageRequest(req)
	var out session.EventMetadataPage
	err := s.withEventMetadata(ctx, req.SessionRef, func(index *eventMetadataIndex, _ string) error {
		out.NextSeq = req.AfterSeq
		start := sort.Search(len(index.Records), func(n int) bool { return index.Records[n].Seq > req.AfterSeq })
		for _, record := range index.Records[start:] {
			if req.ThroughSeq > 0 && record.Seq > req.ThroughSeq {
				break
			}
			visible := false
			switch req.Visibility {
			case session.EventPageCanonical:
				visible = record.Canonical
			case session.EventPageClientReplay:
				visible = record.Replay
			case session.EventPageAllDurable:
				visible = true
			}
			if visible && len(out.Events) >= req.Limit {
				out.HasMore = true
				break
			}
			out.NextSeq = record.Seq
			if visible {
				out.Events = append(out.Events, record.EventMetadata)
			}
		}
		if !out.HasMore {
			through := index.Tail.Seq
			if req.ThroughSeq > 0 {
				through = min(through, req.ThroughSeq)
			}
			out.NextSeq = max(out.NextSeq, through)
		}
		return nil
	})
	return out, err
}

// RunJournal reads only the selected Run and pause payloads from canonical disk.
func (s *Store) RunJournal(ctx context.Context, ref session.SessionRef, runID string) (session.RunJournalSnapshot, error) {
	runID = strings.TrimSpace(runID)
	var out session.RunJournalSnapshot
	err := s.withEventMetadata(ctx, ref, func(index *eventMetadataIndex, path string) error {
		var selected *eventMetadataRecord
		for n := len(index.Records) - 1; n >= 0; n-- {
			record := &index.Records[n]
			if record.RunID == "" || runID != "" && record.RunID != runID {
				continue
			}
			if runID == "" {
				selected = record
				break
			}
			if selected == nil || record.RunRevision >= selected.RunRevision {
				selected = record
			}
		}
		if selected == nil || selected.RunRevision == 0 {
			return session.ErrSessionNotFound
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		defer file.Close()
		event, err := readIndexedEvent(file, eventAppendRecord{offset: selected.Offset, length: selected.Length})
		if err != nil {
			return err
		}
		if event.Journal == nil || event.Journal.Execution == nil {
			return errors.New("agent-sdk/session/file: invalid indexed Run")
		}
		run := session.NormalizeExecutionRecord(*event.Journal.Execution)
		out.Run = &run
		if run.Status != session.ExecutionWaitingApproval {
			return nil
		}
		for n := len(index.Records) - 1; n >= 0; n-- {
			record := index.Records[n]
			if record.PauseRunID != run.RunID {
				continue
			}
			event, err := readIndexedEvent(file, eventAppendRecord{offset: record.Offset, length: record.Length})
			if err != nil {
				return err
			}
			if event.Journal == nil || event.Journal.PauseToken == nil {
				return errors.New("agent-sdk/session/file: invalid indexed pause")
			}
			pause := session.ClonePauseToken(*event.Journal.PauseToken)
			out.Pause = &pause
			break
		}
		return nil
	})
	return out, err
}

func (s *Store) withEventMetadata(ctx context.Context, ref session.SessionRef, fn func(*eventMetadataIndex, string) error) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	return s.withRootReadLockContext(ctx, func() error {
		doc, err := s.readDocumentForRef(ref)
		if err != nil {
			return err
		}
		path, err := s.resolveWritePath(doc.Session)
		if err != nil {
			return err
		}
		index, err := s.readEventMetadata(ctx, path)
		if err != nil {
			return err
		}
		return fn(index, eventLogPath(path))
	})
}

func (s *Store) readEventMetadata(ctx context.Context, documentPath string) (*eventMetadataIndex, error) {
	path := eventLogPath(documentPath)
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return &eventMetadataIndex{Version: eventMetadataIndexVersion}, nil
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	index := s.eventMetadataIndexes[path]
	if index == nil {
		index = loadEventMetadataIndex(path)
	}
	if index != nil {
		valid := index.Version == eventMetadataIndexVersion && index.Size <= info.Size() &&
			(index.Size != info.Size() || index.ModTime == info.ModTime().UnixNano()) &&
			(index.info == nil || os.SameFile(index.info, info))
		if valid && index.Tail.Seq > 0 {
			valid, err = validateEventPageCheckpoint(ctx, file, info.Size(), index.Tail)
		}
		if err != nil {
			return nil, err
		}
		if !valid {
			index = nil
		}
	}
	if index == nil {
		index = &eventMetadataIndex{Version: eventMetadataIndexVersion}
	}
	if index.Size != info.Size() {
		// Publish only a completed scan; cancellation cannot leave a partially
		// advanced in-memory index behind an unchanged source watermark.
		next := *index
		next.Records = append([]eventMetadataRecord(nil), index.Records...)
		if _, err := file.Seek(index.Size, io.SeekStart); err != nil {
			return nil, err
		}
		reader := bufio.NewReader(io.LimitReader(file, info.Size()-index.Size))
		line := index.Tail.LineNo
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			raw, readErr := reader.ReadString('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return nil, readErr
			}
			if !strings.HasSuffix(raw, "\n") {
				break
			}
			line++
			if s.eventMetadataLineRead != nil {
				s.eventMetadataLineRead(path, line, next.Size)
			}
			if strings.TrimSpace(raw) != "" {
				record, err := eventMetadataFromJSON([]byte(raw), path, line)
				if err != nil {
					return nil, err
				}
				if record.Seq <= next.Tail.Seq {
					return nil, errors.New("agent-sdk/session/file: non-increasing metadata sequence")
				}
				record.Offset, record.Length, record.Line = next.Size, len(raw), line
				if next.keep(record) {
					next.Records = append(next.Records, record)
					next.bytes += eventMetadataRecordBytes(record)
				}
				next.Tail = newEventPageCheckpoint(record.Seq, next.Size, next.Size+int64(len(raw)), line, raw)
			}
			next.Size += int64(len(raw))
		}
		endInfo, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if eventPageFileSnapshotChanged(info, endInfo) {
			return nil, errors.New("agent-sdk/session/file: event log changed during metadata read")
		}
		next.ModTime = info.ModTime().UnixNano()
		index = &next
		// Cache persistence is best effort. A missing/corrupt cache is rebuilt
		// on demand and never changes the result or commits durable Session work.
		s.saveEventMetadataIndex(ctx, path, index)
	}
	index.info = info
	if s.eventMetadataIndexes == nil {
		s.eventMetadataIndexes = make(map[string]*eventMetadataIndex)
	}
	delete(s.eventMetadataIndexes, path)
	total := index.bytes
	for _, cached := range s.eventMetadataIndexes {
		total += cached.bytes
	}
	if len(s.eventMetadataIndexes) >= 8 || total > maxEventMetadataIndexBytes {
		clear(s.eventMetadataIndexes)
	}
	if index.bytes <= maxEventMetadataIndexBytes {
		s.eventMetadataIndexes[path] = index
	}
	return index, nil
}

func loadEventMetadataIndex(path string) *eventMetadataIndex {
	file, err := os.Open(path + ".metadata")
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxEventMetadataIndexBytes+1))
	if err != nil || len(data) > maxEventMetadataIndexBytes {
		return nil
	}
	var disk eventMetadataDisk
	if json.Unmarshal(data, &disk) != nil || sha256.Sum256(disk.Data) != disk.Hash {
		return nil
	}
	var index eventMetadataIndex
	if json.Unmarshal(disk.Data, &index) != nil || index.Size < 0 {
		return nil
	}
	for _, record := range index.Records {
		index.bytes += eventMetadataRecordBytes(record)
	}
	return &index
}

func (s *Store) saveEventMetadataIndex(ctx context.Context, path string, index *eventMetadataIndex) {
	data, err := json.Marshal(index)
	if err != nil || len(data) > maxEventMetadataIndexBytes/2 {
		return
	}
	disk, err := json.Marshal(eventMetadataDisk{Data: data, Hash: sha256.Sum256(data)})
	if err != nil {
		return
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".event-metadata-*")
	if err != nil {
		return
	}
	name := file.Name()
	defer os.Remove(name)
	_, err = file.Write(disk)
	closeErr := file.Close()
	if err == nil && closeErr == nil {
		_ = replaceFile(ctx, s.diagnostics, fileOperationReplaceDocument, name, path+".metadata")
	}
}

// Common current-schema records only decode provenance. Special/legacy event
// families use the canonical decoder so projection eligibility has one owner.
func eventMetadataFromJSON(raw []byte, path string, line int) (eventMetadataRecord, error) {
	var header struct {
		Schema      int                       `json:"schema"`
		Seq         uint64                    `json:"seq"`
		Type        session.EventType         `json:"type"`
		Visibility  session.Visibility        `json:"visibility"`
		Scope       *session.EventScope       `json:"scope"`
		ChildOrigin *session.EventChildOrigin `json:"child_origin"`
		Notice      json.RawMessage           `json:"notice"`
		Journal     *struct {
			Kind       session.JournalKind      `json:"kind"`
			Execution  *session.ExecutionRecord `json:"execution"`
			PauseToken *session.PauseToken      `json:"pause_token"`
		} `json:"journal"`
	}
	if err := json.Unmarshal(raw, &header); err != nil {
		return eventMetadataRecord{}, err
	}
	event := &session.Event{Schema: header.Schema, Seq: header.Seq, Type: header.Type, Visibility: header.Visibility, Scope: header.Scope, ChildOrigin: header.ChildOrigin}
	if header.Journal != nil {
		event.Journal = &session.ExecutionJournalEntry{Kind: header.Journal.Kind, Execution: header.Journal.Execution, PauseToken: header.Journal.PauseToken}
	}
	if header.Schema != session.EventSchemaVersion || header.Type == "" || header.Type == session.EventTypeContext || len(header.Notice) > 0 {
		var err error
		event, err = decodeIndexedEvent(raw, path, line)
		if err != nil {
			return eventMetadataRecord{}, err
		}
	}
	event = session.CloneEvent(event)
	out := eventMetadataRecord{EventMetadata: session.EventMetadata{Seq: event.Seq, HasScope: event.Scope != nil, Child: event.ChildOrigin != nil, ReviewedApproval: session.ResolvedApprovalReview(event) != nil}, Canonical: session.IsCanonicalHistoryEvent(event), Replay: session.IsClientReplayEvent(event)}
	if event.Scope != nil {
		out.TurnID = event.Scope.TurnID
	}
	if event.Journal != nil {
		if event.Journal.Execution != nil {
			run := session.NormalizeExecutionRecord(*event.Journal.Execution)
			if run.Kind == session.JournalKindRun {
				out.RunID, out.RunRevision = run.RunID, run.Revision
			}
		}
		if event.Journal.PauseToken != nil {
			out.PauseRunID = event.Journal.PauseToken.RunID
		}
	}
	return out, nil
}

func (s *Store) metadataEventPageCheckpoint(ctx context.Context, file *os.File, path string, info os.FileInfo, after uint64) (eventPageCheckpoint, error) {
	index := s.eventMetadataIndexes[path]
	if index == nil || index.info == nil || !os.SameFile(index.info, info) || info.Size() < index.Size || info.Size() == index.Size && info.ModTime().UnixNano() != index.ModTime {
		return eventPageCheckpoint{}, nil
	}
	n := sort.Search(len(index.Records), func(n int) bool { return index.Records[n].Seq > after })
	if n == 0 {
		return eventPageCheckpoint{}, nil
	}
	if index.Tail.Seq > 0 {
		valid, err := validateEventPageCheckpoint(ctx, file, info.Size(), index.Tail)
		if err != nil || !valid {
			return eventPageCheckpoint{}, err
		}
	}
	record := index.Records[n-1]
	raw := make([]byte, record.Length)
	if _, err := file.ReadAt(raw, record.Offset); err != nil {
		return eventPageCheckpoint{}, err
	}
	return newEventPageCheckpoint(record.Seq, record.Offset, record.Offset+int64(record.Length), record.Line, string(raw)), nil
}

func eventMetadataRecordBytes(record eventMetadataRecord) int64 {
	return int64(256 + len(record.TurnID) + len(record.RunID) + len(record.PauseRunID))
}

func (i *eventMetadataIndex) keep(record eventMetadataRecord) bool {
	if len(i.Records) == 0 || record.RunID != "" || record.PauseRunID != "" || record.ReviewedApproval {
		return true
	}
	previous := i.Records[len(i.Records)-1]
	return record.Seq-previous.Seq >= eventMetadataCheckpointStride || record.TurnID != previous.TurnID || record.HasScope != previous.HasScope || record.Child != previous.Child || record.Canonical != previous.Canonical || record.Replay != previous.Replay
}
