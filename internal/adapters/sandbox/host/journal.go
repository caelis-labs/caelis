package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
)

const (
	sessionJournalKind    = "caelis.host_sandbox.session"
	sessionJournalVersion = 1
)

type sessionJournal struct {
	root string
	mu   sync.Mutex
}

type sessionJournalRecord struct {
	Kind               string                  `json:"kind"`
	Version            int                     `json:"version"`
	Snapshot           sandbox.SessionSnapshot `json:"snapshot"`
	PID                int                     `json:"pid,omitempty"`
	Stdout             string                  `json:"stdout,omitempty"`
	Stderr             string                  `json:"stderr,omitempty"`
	StdoutTotalBytes   int64                   `json:"stdout_total_bytes,omitempty"`
	StderrTotalBytes   int64                   `json:"stderr_total_bytes,omitempty"`
	StdoutDroppedBytes int64                   `json:"stdout_dropped_bytes,omitempty"`
	StderrDroppedBytes int64                   `json:"stderr_dropped_bytes,omitempty"`
	UpdatedAt          time.Time               `json:"updated_at,omitempty"`
}

func newSessionJournal(root string) *sessionJournal {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	return &sessionJournal{root: filepath.Join(root, "host-sessions")}
}

type sessionStreamFiles struct {
	stdoutPath   string
	stderrPath   string
	stdoutWriter *os.File
	stderrWriter *os.File
}

func (f sessionStreamFiles) enabled() bool {
	return strings.TrimSpace(f.stdoutPath) != "" || strings.TrimSpace(f.stderrPath) != ""
}

func (j *sessionJournal) createStreamFiles(id string) (sessionStreamFiles, []io.Closer, error) {
	if j == nil {
		return sessionStreamFiles{}, nil, nil
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return sessionStreamFiles{}, nil, errors.New("sandbox/host: session id is required")
	}
	if err := os.MkdirAll(j.root, 0o755); err != nil {
		return sessionStreamFiles{}, nil, err
	}
	stdoutPath := j.streamPath(id, "stdout")
	stderrPath := j.streamPath(id, "stderr")
	stdout, err := os.OpenFile(stdoutPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return sessionStreamFiles{}, nil, err
	}
	stderr, err := os.OpenFile(stderrPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		_ = stdout.Close()
		return sessionStreamFiles{}, nil, err
	}
	return sessionStreamFiles{
		stdoutPath:   stdoutPath,
		stderrPath:   stderrPath,
		stdoutWriter: stdout,
		stderrWriter: stderr,
	}, []io.Closer{stdout, stderr}, nil
}

func (j *sessionJournal) write(ctx context.Context, record sessionJournalRecord) error {
	if j == nil {
		return nil
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	record = normalizeSessionJournalRecord(record)
	if strings.TrimSpace(record.Snapshot.Ref.ID) == "" {
		return errors.New("sandbox/host: session journal record id is required")
	}
	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.MkdirAll(j.root, 0o755); err != nil {
		return err
	}
	path := j.path(record.Snapshot.Ref.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (j *sessionJournal) open(ctx context.Context, ref sandbox.SessionRef) (sandbox.Session, error) {
	if j == nil {
		return nil, os.ErrNotExist
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	record, err := j.read(strings.TrimSpace(ref.ID))
	if err != nil {
		return nil, err
	}
	if ref.Backend != "" && ref.Backend != record.Snapshot.Ref.Backend {
		return nil, fmt.Errorf("sandbox/host: archived session backend mismatch: %s", ref.Backend)
	}
	if live := j.recoveredLive(record); live != nil {
		return live, nil
	}
	return archivedCommandSession{record: j.recoveredRecord(ctx, record)}, nil
}

func (j *sessionJournal) list(ctx context.Context) ([]sandbox.SessionSnapshot, error) {
	if j == nil {
		return nil, nil
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	records, err := j.readAll()
	if err != nil {
		return nil, err
	}
	out := make([]sandbox.SessionSnapshot, 0, len(records))
	for _, record := range records {
		out = append(out, j.recoveredRecord(ctx, record).Snapshot)
	}
	sort.SliceStable(out, func(i int, k int) bool {
		return out[i].UpdatedAt.After(out[k].UpdatedAt)
	})
	return out, nil
}

func (j *sessionJournal) readAll() ([]sessionJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	files, err := os.ReadDir(j.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]sessionJournalRecord, 0, len(files))
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		record, err := readSessionJournalFile(filepath.Join(j.root, file.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

func (j *sessionJournal) read(id string) (sessionJournalRecord, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return sessionJournalRecord{}, errors.New("sandbox/host: session id is required")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return readSessionJournalFile(j.path(id))
}

func (j *sessionJournal) path(id string) string {
	return filepath.Join(j.root, safeSessionJournalID(id)+".json")
}

func (j *sessionJournal) streamPath(id string, stream string) string {
	return filepath.Join(j.root, safeSessionJournalID(id)+"."+stream+".log")
}

func readSessionJournalFile(path string) (sessionJournalRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return sessionJournalRecord{}, err
	}
	var record sessionJournalRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return sessionJournalRecord{}, err
	}
	return normalizeSessionJournalRecord(record), nil
}

func normalizeSessionJournalRecord(record sessionJournalRecord) sessionJournalRecord {
	record.Kind = sessionJournalKind
	record.Version = sessionJournalVersion
	if record.PID <= 0 {
		if pid, ok := numericMetadata(record.Snapshot.Metadata, "pid"); ok {
			record.PID = pid
		}
	}
	record.Snapshot.Ref.ID = strings.TrimSpace(record.Snapshot.Ref.ID)
	if record.Snapshot.Ref.Backend == "" {
		record.Snapshot.Ref.Backend = sandbox.BackendHost
	}
	record.Snapshot.Command = strings.TrimSpace(record.Snapshot.Command)
	record.Snapshot.Dir = strings.TrimSpace(record.Snapshot.Dir)
	record.Snapshot.Error = strings.TrimSpace(record.Snapshot.Error)
	record.Snapshot.Terminal.ID = strings.TrimSpace(record.Snapshot.Terminal.ID)
	record.Snapshot.Terminal.SessionID = strings.TrimSpace(record.Snapshot.Terminal.SessionID)
	if record.Snapshot.Terminal.SessionID == "" {
		record.Snapshot.Terminal.SessionID = record.Snapshot.Ref.ID
	}
	if record.Snapshot.Terminal.ID == "" {
		record.Snapshot.Terminal.ID = record.Snapshot.Ref.ID
	}
	record.Snapshot.Metadata = maps.Clone(record.Snapshot.Metadata)
	if record.Snapshot.Metadata == nil {
		record.Snapshot.Metadata = map[string]any{}
	}
	if record.PID > 0 {
		record.Snapshot.Metadata["pid"] = record.PID
	}
	if record.Snapshot.UpdatedAt.IsZero() {
		record.Snapshot.UpdatedAt = record.UpdatedAt
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.Snapshot.UpdatedAt
	}
	return record
}

func (j *sessionJournal) recoveredLive(record sessionJournalRecord) sandbox.Session {
	record = normalizeSessionJournalRecord(record)
	if !record.Snapshot.Running || record.PID <= 0 || !processAlive(record.PID) {
		return nil
	}
	record.Snapshot.SupportsInput = false
	record.Snapshot.Metadata = maps.Clone(record.Snapshot.Metadata)
	if record.Snapshot.Metadata == nil {
		record.Snapshot.Metadata = map[string]any{}
	}
	record.Snapshot.Metadata["recovered_live_process"] = true
	record.Snapshot.Metadata["supports_input"] = false
	record.Snapshot.Metadata["durable_output"] = true
	return newRecoveredCommandSession(j, record)
}

func (j *sessionJournal) recoveredRecord(ctx context.Context, record sessionJournalRecord) sessionJournalRecord {
	record = normalizeSessionJournalRecord(record)
	if record.Snapshot.Running && record.PID > 0 && processAlive(record.PID) {
		record.Snapshot.SupportsInput = false
		record.Snapshot.Metadata = maps.Clone(record.Snapshot.Metadata)
		record.Snapshot.Metadata["recovered_live_process"] = true
		record.Snapshot.Metadata["supports_input"] = false
		return j.withFileOutput(record)
	}
	if record.Snapshot.Running && record.PID > 0 {
		record = j.completeUnknown(ctx, record)
	}
	return recoveredSessionJournalRecord(j.withFileOutput(record))
}

func (j *sessionJournal) completeUnknown(ctx context.Context, record sessionJournalRecord) sessionJournalRecord {
	record = j.withFileOutput(normalizeSessionJournalRecord(record))
	record.Snapshot.Running = false
	record.Snapshot.SupportsInput = false
	record.Snapshot.State = sandbox.SessionCompleted
	record.Snapshot.ExitCode = 0
	record.Snapshot.Error = ""
	record.Snapshot.Metadata = maps.Clone(record.Snapshot.Metadata)
	if record.Snapshot.Metadata == nil {
		record.Snapshot.Metadata = map[string]any{}
	}
	record.Snapshot.Metadata["exit_status_unknown"] = true
	record.Snapshot.Metadata["recovered_after_restart"] = true
	record.UpdatedAt = time.Now().UTC()
	record.Snapshot.UpdatedAt = record.UpdatedAt
	_ = j.write(ctx, record)
	return record
}

func (j *sessionJournal) withFileOutput(record sessionJournalRecord) sessionJournalRecord {
	if j == nil {
		return record
	}
	id := record.Snapshot.Ref.ID
	stdout, stdoutTotal, stdoutDropped := readTailFile(j.streamPath(id, "stdout"), sessionOutputBufferCap)
	stderr, stderrTotal, stderrDropped := readTailFile(j.streamPath(id, "stderr"), sessionOutputBufferCap)
	if stdoutTotal > 0 || len(stdout) > 0 {
		record.Stdout = string(stdout)
		record.StdoutTotalBytes = stdoutTotal
		record.StdoutDroppedBytes = stdoutDropped
	}
	if stderrTotal > 0 || len(stderr) > 0 {
		record.Stderr = string(stderr)
		record.StderrTotalBytes = stderrTotal
		record.StderrDroppedBytes = stderrDropped
	}
	record.Snapshot.OutputPreview = outputPreviewFromJournalRecord(record, sessionOutputPreviewCap)
	return record
}

func recoveredSessionJournalRecord(record sessionJournalRecord) sessionJournalRecord {
	record = normalizeSessionJournalRecord(record)
	if record.Snapshot.Running {
		record.Snapshot.Running = false
		record.Snapshot.SupportsInput = false
		record.Snapshot.State = sandbox.SessionFailed
		if record.Snapshot.ExitCode == 0 {
			record.Snapshot.ExitCode = -1
		}
		if record.Snapshot.Error == "" {
			record.Snapshot.Error = "sandbox/host: session recovered without a live process"
		}
	}
	if record.Snapshot.State == "" {
		if record.Snapshot.ExitCode == 0 && record.Snapshot.Error == "" {
			record.Snapshot.State = sandbox.SessionCompleted
		} else {
			record.Snapshot.State = sandbox.SessionFailed
		}
	}
	return record
}

func numericMetadata(meta map[string]any, key string) (int, bool) {
	if len(meta) == 0 {
		return 0, false
	}
	switch value := meta[key].(type) {
	case int:
		return value, value > 0
	case int64:
		return int(value), value > 0
	case float64:
		return int(value), value > 0
	case json.Number:
		parsed, err := value.Int64()
		return int(parsed), err == nil && parsed > 0
	default:
		return 0, false
	}
}

func readTailFile(path string, limit int) ([]byte, int64, int64) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, 0, 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, 0
	}
	total := int64(len(data))
	if limit <= 0 || len(data) <= limit {
		return data, total, 0
	}
	dropped := len(data) - limit
	return append([]byte(nil), data[dropped:]...), total, int64(dropped)
}

func safeSessionJournalID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "session"
	}
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "session"
	}
	return b.String()
}

type archivedCommandSession struct {
	record sessionJournalRecord
}

func (s archivedCommandSession) Ref() sandbox.SessionRef {
	return s.record.Snapshot.Ref
}

func (s archivedCommandSession) Snapshot(ctx context.Context) (sandbox.SessionSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return sandbox.SessionSnapshot{}, err
	}
	return s.record.Snapshot, nil
}

func (s archivedCommandSession) Read(ctx context.Context, cursor sandbox.OutputCursor) (sandbox.OutputSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return sandbox.OutputSnapshot{}, err
	}
	stdout, stdoutCursor, stdoutDropped := archivedOutputSince(s.record.Stdout, s.record.StdoutTotalBytes, cursor.Stdout)
	stderr, stderrCursor, stderrDropped := archivedOutputSince(s.record.Stderr, s.record.StderrTotalBytes, cursor.Stderr)
	return sandbox.OutputSnapshot{
		Stdout: string(stdout),
		Stderr: string(stderr),
		Cursor: sandbox.OutputCursor{
			Stdout: stdoutCursor,
			Stderr: stderrCursor,
		},
		StdoutDroppedBytes: stdoutDropped,
		StderrDroppedBytes: stderrDropped,
	}, nil
}

func (s archivedCommandSession) Write(ctx context.Context, _ []byte) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	return errors.New("sandbox/host: archived session is not writable")
}

func (s archivedCommandSession) Cancel(ctx context.Context) error {
	return contextErr(ctx)
}

func (s archivedCommandSession) Wait(ctx context.Context) (sandbox.CommandResult, error) {
	if err := contextErr(ctx); err != nil {
		return sandbox.CommandResult{}, err
	}
	return sandbox.CommandResult{
		Stdout:   s.record.Stdout,
		Stderr:   s.record.Stderr,
		Error:    s.record.Snapshot.Error,
		ExitCode: s.record.Snapshot.ExitCode,
		Route:    sandbox.RouteHost,
		Backend:  sandbox.BackendHost,
	}, nil
}

func (s archivedCommandSession) Close() error {
	return nil
}

type recoveredCommandSession struct {
	journal *sessionJournal
	ref     sandbox.SessionRef
	pid     int
	done    chan struct{}
	once    sync.Once
}

func newRecoveredCommandSession(journal *sessionJournal, record sessionJournalRecord) *recoveredCommandSession {
	record = normalizeSessionJournalRecord(record)
	s := &recoveredCommandSession{
		journal: journal,
		ref:     record.Snapshot.Ref,
		pid:     record.PID,
		done:    make(chan struct{}),
	}
	go s.watch()
	return s
}

func (s *recoveredCommandSession) Ref() sandbox.SessionRef {
	return s.ref
}

func (s *recoveredCommandSession) Snapshot(ctx context.Context) (sandbox.SessionSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return sandbox.SessionSnapshot{}, err
	}
	record, err := s.currentRecord(ctx)
	if err != nil {
		return sandbox.SessionSnapshot{}, err
	}
	return record.Snapshot, nil
}

func (s *recoveredCommandSession) Read(ctx context.Context, cursor sandbox.OutputCursor) (sandbox.OutputSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return sandbox.OutputSnapshot{}, err
	}
	if s == nil || s.journal == nil {
		return sandbox.OutputSnapshot{}, os.ErrNotExist
	}
	stdout, stdoutCursor, stdoutDropped := archivedFileOutputSince(s.journal.streamPath(s.ref.ID, "stdout"), cursor.Stdout)
	stderr, stderrCursor, stderrDropped := archivedFileOutputSince(s.journal.streamPath(s.ref.ID, "stderr"), cursor.Stderr)
	if stdoutCursor == 0 && stderrCursor == 0 {
		record, err := s.currentRecord(ctx)
		if err != nil {
			return sandbox.OutputSnapshot{}, err
		}
		return archivedCommandSession{record: record}.Read(ctx, cursor)
	}
	return sandbox.OutputSnapshot{
		Stdout: string(stdout),
		Stderr: string(stderr),
		Cursor: sandbox.OutputCursor{
			Stdout: stdoutCursor,
			Stderr: stderrCursor,
		},
		StdoutDroppedBytes: stdoutDropped,
		StderrDroppedBytes: stderrDropped,
	}, nil
}

func (s *recoveredCommandSession) Write(ctx context.Context, _ []byte) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	return errors.New("sandbox/host: recovered session stdin is unavailable")
}

func (s *recoveredCommandSession) Cancel(ctx context.Context) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	if s == nil || s.journal == nil {
		return nil
	}
	if s.pid > 0 {
		_ = killProcessID(s.pid)
	}
	record, err := s.journal.read(s.ref.ID)
	if err != nil {
		return err
	}
	record = s.journal.withFileOutput(normalizeSessionJournalRecord(record))
	record.Snapshot.Running = false
	record.Snapshot.SupportsInput = false
	record.Snapshot.State = sandbox.SessionCancelled
	record.Snapshot.ExitCode = -1
	record.Snapshot.Error = "cancelled"
	record.Snapshot.UpdatedAt = time.Now().UTC()
	record.UpdatedAt = record.Snapshot.UpdatedAt
	if err := s.journal.write(ctx, record); err != nil {
		return err
	}
	s.closeDone()
	return nil
}

func (s *recoveredCommandSession) Wait(ctx context.Context) (sandbox.CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-s.done:
	case <-ctx.Done():
		return sandbox.CommandResult{}, ctx.Err()
	}
	record, err := s.currentRecord(ctx)
	if err != nil {
		return sandbox.CommandResult{}, err
	}
	return archivedCommandSession{record: record}.Wait(ctx)
}

func (s *recoveredCommandSession) Close() error {
	return nil
}

func (s *recoveredCommandSession) currentRecord(ctx context.Context) (sessionJournalRecord, error) {
	if s == nil || s.journal == nil {
		return sessionJournalRecord{}, os.ErrNotExist
	}
	record, err := s.journal.read(s.ref.ID)
	if err != nil {
		return sessionJournalRecord{}, err
	}
	return s.journal.recoveredRecord(ctx, record), nil
}

func (s *recoveredCommandSession) watch() {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if s == nil || s.pid <= 0 || !processAlive(s.pid) {
			if s != nil && s.journal != nil {
				record, err := s.journal.read(s.ref.ID)
				if err == nil && record.Snapshot.Running {
					_ = s.journal.completeUnknown(context.Background(), record)
				}
			}
			s.closeDone()
			return
		}
		<-ticker.C
	}
}

func (s *recoveredCommandSession) closeDone() {
	if s == nil {
		return
	}
	s.once.Do(func() {
		close(s.done)
	})
}

func archivedOutputSince(text string, total int64, cursor int64) ([]byte, int64, int64) {
	data := []byte(text)
	if total <= 0 {
		total = int64(len(data))
	}
	if cursor < 0 {
		cursor = 0
	}
	earliest := total - int64(len(data))
	dropped := int64(0)
	if cursor < earliest {
		dropped = earliest - cursor
		cursor = earliest
	}
	if cursor >= total {
		return nil, total, dropped
	}
	start := int(cursor - earliest)
	if start < 0 {
		start = 0
	}
	if start > len(data) {
		start = len(data)
	}
	return append([]byte(nil), data[start:]...), total, dropped
}

func archivedFileOutputSince(path string, cursor int64) ([]byte, int64, int64) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, 0
	}
	return archivedOutputSince(string(data), int64(len(data)), cursor)
}

var _ sandbox.Session = archivedCommandSession{}
var _ sandbox.Session = (*recoveredCommandSession)(nil)
