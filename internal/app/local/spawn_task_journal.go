package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
	"github.com/OnslaughtSnail/caelis/core/session"
)

const (
	spawnTaskJournalKind    = "caelis.spawn_task"
	spawnTaskJournalVersion = 1
)

type spawnTaskJournal struct {
	root string
	mu   sync.Mutex
}

type spawnTaskJournalRecord struct {
	Kind             string                  `json:"kind"`
	Version          int                     `json:"version"`
	Parent           session.Ref             `json:"parent,omitempty"`
	Workspace        session.Workspace       `json:"workspace,omitempty"`
	TurnID           string                  `json:"turn_id,omitempty"`
	Agent            string                  `json:"agent,omitempty"`
	RemoteSessionID  string                  `json:"remote_session_id,omitempty"`
	Snapshot         sandbox.SessionSnapshot `json:"snapshot"`
	Stdout           string                  `json:"stdout,omitempty"`
	StdoutTotalBytes int64                   `json:"stdout_total_bytes,omitempty"`
	UpdatedAt        time.Time               `json:"updated_at,omitempty"`
}

func newSpawnTaskJournal(root string) *spawnTaskJournal {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	return &spawnTaskJournal{root: filepath.Join(root, "spawn-tasks")}
}

func (j *spawnTaskJournal) write(ctx context.Context, record spawnTaskJournalRecord) error {
	if j == nil {
		return nil
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	record = normalizeSpawnTaskJournalRecord(record)
	if strings.TrimSpace(record.Snapshot.Ref.ID) == "" {
		return errors.New("app/local: SPAWN task journal record id is required")
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

func (j *spawnTaskJournal) open(ctx context.Context, ref sandbox.SessionRef) (sandbox.Session, bool, error) {
	if j == nil {
		return nil, false, nil
	}
	if err := contextErr(ctx); err != nil {
		return nil, false, err
	}
	record, err := j.read(strings.TrimSpace(ref.ID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	record = recoveredSpawnTaskJournalRecord(record)
	if ref.Backend != "" && ref.Backend != record.Snapshot.Ref.Backend {
		return nil, false, fmt.Errorf("app/local: archived SPAWN task backend mismatch: %s", ref.Backend)
	}
	return archivedSpawnTaskSession{record: record}, true, nil
}

func (j *spawnTaskJournal) list(ctx context.Context) ([]sandbox.SessionSnapshot, error) {
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
		out = append(out, recoveredSpawnTaskJournalRecord(record).Snapshot)
	}
	sort.SliceStable(out, func(i, k int) bool {
		return out[i].UpdatedAt.After(out[k].UpdatedAt)
	})
	return out, nil
}

func (j *spawnTaskJournal) readAll() ([]spawnTaskJournalRecord, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	files, err := os.ReadDir(j.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]spawnTaskJournalRecord, 0, len(files))
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		record, err := readSpawnTaskJournalFile(filepath.Join(j.root, file.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

func (j *spawnTaskJournal) read(id string) (spawnTaskJournalRecord, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return spawnTaskJournalRecord{}, errors.New("app/local: SPAWN task id is required")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	return readSpawnTaskJournalFile(j.path(id))
}

func (j *spawnTaskJournal) path(id string) string {
	return filepath.Join(j.root, safeSpawnTaskJournalID(id)+".json")
}

func readSpawnTaskJournalFile(path string) (spawnTaskJournalRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return spawnTaskJournalRecord{}, err
	}
	var record spawnTaskJournalRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return spawnTaskJournalRecord{}, err
	}
	return normalizeSpawnTaskJournalRecord(record), nil
}

func normalizeSpawnTaskJournalRecord(record spawnTaskJournalRecord) spawnTaskJournalRecord {
	record.Kind = spawnTaskJournalKind
	record.Version = spawnTaskJournalVersion
	record.Parent = session.NormalizeRef(record.Parent)
	record.Workspace.Key = strings.TrimSpace(record.Workspace.Key)
	record.Workspace.CWD = strings.TrimSpace(record.Workspace.CWD)
	record.TurnID = strings.TrimSpace(record.TurnID)
	record.Agent = strings.TrimSpace(record.Agent)
	record.RemoteSessionID = strings.TrimSpace(record.RemoteSessionID)
	record.Snapshot.Ref.ID = strings.TrimSpace(record.Snapshot.Ref.ID)
	record.Snapshot.Ref.Backend = sandbox.BackendCustom
	record.Snapshot.Command = strings.TrimSpace(record.Snapshot.Command)
	record.Snapshot.Dir = strings.TrimSpace(record.Snapshot.Dir)
	record.Snapshot.Error = strings.TrimSpace(record.Snapshot.Error)
	record.Snapshot.Terminal.ID = strings.TrimSpace(record.Snapshot.Terminal.ID)
	record.Snapshot.Terminal.SessionID = strings.TrimSpace(record.Snapshot.Terminal.SessionID)
	if record.Snapshot.Terminal.SessionID == "" {
		record.Snapshot.Terminal.SessionID = record.Snapshot.Ref.ID
	}
	if record.Snapshot.Terminal.ID == "" {
		record.Snapshot.Terminal.ID = "spawn-" + record.Snapshot.Ref.ID
	}
	record.Snapshot.Metadata = maps.Clone(record.Snapshot.Metadata)
	if record.Snapshot.Metadata == nil {
		record.Snapshot.Metadata = map[string]any{}
	}
	if record.Agent == "" {
		if agent, ok := record.Snapshot.Metadata["agent"].(string); ok {
			record.Agent = strings.TrimSpace(agent)
		}
	}
	if record.RemoteSessionID == "" {
		if remoteSessionID, ok := record.Snapshot.Metadata["remote_session_id"].(string); ok {
			record.RemoteSessionID = strings.TrimSpace(remoteSessionID)
		}
	}
	record.Snapshot.Metadata["task_kind"] = "subagent"
	record.Snapshot.Metadata["source"] = "spawn"
	if record.Agent != "" {
		record.Snapshot.Metadata["agent"] = record.Agent
	}
	if record.RemoteSessionID != "" {
		record.Snapshot.Metadata["remote_session_id"] = record.RemoteSessionID
	}
	record.Snapshot.Metadata["state"] = string(record.Snapshot.State)
	record.Snapshot.Metadata["running"] = record.Snapshot.Running
	if !record.Snapshot.Running {
		record.Snapshot.Metadata["exit_code"] = record.Snapshot.ExitCode
	}
	if record.Snapshot.UpdatedAt.IsZero() {
		record.Snapshot.UpdatedAt = record.UpdatedAt
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.Snapshot.UpdatedAt
	}
	if record.StdoutTotalBytes <= 0 {
		record.StdoutTotalBytes = int64(len([]byte(record.Stdout)))
	}
	return record
}

func recoveredSpawnTaskJournalRecord(record spawnTaskJournalRecord) spawnTaskJournalRecord {
	record = normalizeSpawnTaskJournalRecord(record)
	record.Snapshot.SupportsInput = false
	if record.Snapshot.Running {
		record.Snapshot.Running = false
		record.Snapshot.State = sandbox.SessionFailed
		if record.Snapshot.ExitCode == 0 {
			record.Snapshot.ExitCode = -1
		}
		if record.Snapshot.Error == "" {
			record.Snapshot.Error = "app/local: SPAWN task recovered without a live controller"
		}
	}
	if record.Snapshot.State == "" {
		if record.Snapshot.ExitCode == 0 && record.Snapshot.Error == "" {
			record.Snapshot.State = sandbox.SessionCompleted
		} else {
			record.Snapshot.State = sandbox.SessionFailed
		}
	}
	record.Snapshot.Metadata = maps.Clone(record.Snapshot.Metadata)
	record.Snapshot.Metadata["state"] = string(record.Snapshot.State)
	record.Snapshot.Metadata["running"] = record.Snapshot.Running
	record.Snapshot.Metadata["supports_input"] = record.Snapshot.SupportsInput
	if !record.Snapshot.Running {
		record.Snapshot.Metadata["exit_code"] = record.Snapshot.ExitCode
	}
	return record
}

func safeSpawnTaskJournalID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "spawn-task"
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
		return "spawn-task"
	}
	return b.String()
}

type archivedSpawnTaskSession struct {
	record spawnTaskJournalRecord
}

func (s archivedSpawnTaskSession) Ref() sandbox.SessionRef {
	return s.record.Snapshot.Ref
}

func (s archivedSpawnTaskSession) Snapshot(ctx context.Context) (sandbox.SessionSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return sandbox.SessionSnapshot{}, err
	}
	return s.record.Snapshot, nil
}

func (s archivedSpawnTaskSession) Read(ctx context.Context, cursor sandbox.OutputCursor) (sandbox.OutputSnapshot, error) {
	if err := contextErr(ctx); err != nil {
		return sandbox.OutputSnapshot{}, err
	}
	stdout, next, dropped := archivedSpawnOutputSince(s.record.Stdout, s.record.StdoutTotalBytes, cursor.Stdout)
	return sandbox.OutputSnapshot{
		Stdout:             string(stdout),
		Cursor:             sandbox.OutputCursor{Stdout: next},
		StdoutDroppedBytes: dropped,
	}, nil
}

func (s archivedSpawnTaskSession) Write(ctx context.Context, _ []byte) error {
	if err := contextErr(ctx); err != nil {
		return err
	}
	return errors.New("app/local: archived SPAWN task is not writable")
}

func (s archivedSpawnTaskSession) Cancel(ctx context.Context) error {
	return contextErr(ctx)
}

func (s archivedSpawnTaskSession) Wait(ctx context.Context) (sandbox.CommandResult, error) {
	if err := contextErr(ctx); err != nil {
		return sandbox.CommandResult{}, err
	}
	return sandbox.CommandResult{
		Stdout:   s.record.Stdout,
		Error:    s.record.Snapshot.Error,
		ExitCode: s.record.Snapshot.ExitCode,
		Route:    sandbox.RouteHost,
		Backend:  sandbox.BackendCustom,
	}, nil
}

func (s archivedSpawnTaskSession) Close() error {
	return nil
}

func (s archivedSpawnTaskSession) TaskMeta() map[string]any {
	return maps.Clone(s.record.Snapshot.Metadata)
}

func archivedSpawnOutputSince(text string, total int64, cursor int64) ([]byte, int64, int64) {
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

var _ sandbox.Session = archivedSpawnTaskSession{}
