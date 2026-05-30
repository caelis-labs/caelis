package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	return archivedCommandSession{record: recoveredSessionJournalRecord(record)}, nil
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
		out = append(out, recoveredSessionJournalRecord(record).Snapshot)
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
	if record.Snapshot.UpdatedAt.IsZero() {
		record.Snapshot.UpdatedAt = record.UpdatedAt
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.Snapshot.UpdatedAt
	}
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

var _ sandbox.Session = archivedCommandSession{}
