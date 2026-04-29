package file

import (
	"bufio"
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

	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdktask "github.com/OnslaughtSnail/caelis/sdk/task"
)

const (
	indexKind    = "caelis.sdk.task_index"
	indexVersion = 1
	blobKind     = "caelis.sdk.task_blob"
)

// Config defines one durable task file store.
type Config struct {
	RootDir string
	Clock   func() time.Time
}

// Store persists session-scoped task indexes and finalized task output blobs.
type Store struct {
	mu      sync.Mutex
	rootDir string
	clock   func() time.Time
}

type indexDocument struct {
	Kind      string                `json:"kind"`
	Version   int                   `json:"version"`
	Session   sdksession.SessionRef `json:"session"`
	UpdatedAt time.Time             `json:"updated_at"`
	Tasks     []*sdktask.Entry      `json:"tasks"`
	Metadata  map[string]any        `json:"metadata,omitempty"`
}

type blobRecord struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	TaskID    string    `json:"task_id"`
	Stream    string    `json:"stream"`
	Text      string    `json:"text"`
	CreatedAt time.Time `json:"created_at"`
}

// NewStore constructs one file-backed task store.
func NewStore(cfg Config) *Store {
	rootDir := strings.TrimSpace(cfg.RootDir)
	if rootDir == "" {
		rootDir = filepath.Join(os.TempDir(), "caelis-sdk-tasks")
	}
	clock := cfg.Clock
	if clock == nil {
		clock = time.Now
	}
	return &Store{rootDir: rootDir, clock: clock}
}

func (s *Store) Upsert(_ context.Context, entry *sdktask.Entry) error {
	entry = sdktask.CloneEntry(entry)
	if entry == nil {
		return nil
	}
	if strings.TrimSpace(entry.TaskID) == "" || strings.TrimSpace(entry.Session.SessionID) == "" {
		return fmt.Errorf("sdk/task/file: task_id and session_id are required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readIndex(entry.Session)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if errors.Is(err, os.ErrNotExist) {
		doc = indexDocument{
			Kind:      indexKind,
			Version:   indexVersion,
			Session:   sdksession.NormalizeSessionRef(entry.Session),
			UpdatedAt: s.now(),
			Tasks:     nil,
		}
	}

	blobIDs, err := s.writeFinalBlobs(entry)
	if err != nil {
		return err
	}
	if len(blobIDs) != 0 {
		if entry.Result == nil {
			entry.Result = map[string]any{}
		}
		for key, value := range blobIDs {
			entry.Result[key] = value
		}
		delete(entry.Result, "stdout")
		delete(entry.Result, "stderr")
	}

	replaced := false
	for i, item := range doc.Tasks {
		if item != nil && strings.TrimSpace(item.TaskID) == entry.TaskID {
			doc.Tasks[i] = entry
			replaced = true
			break
		}
	}
	if !replaced {
		doc.Tasks = append(doc.Tasks, entry)
	}
	sort.Slice(doc.Tasks, func(i, j int) bool {
		if doc.Tasks[i] == nil || doc.Tasks[j] == nil {
			return i < j
		}
		return doc.Tasks[i].UpdatedAt.After(doc.Tasks[j].UpdatedAt)
	})
	doc.UpdatedAt = s.now()
	return s.writeIndex(doc)
}

func (s *Store) Get(_ context.Context, taskID string) (*sdktask.Entry, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, fmt.Errorf("sdk/task/file: task_id is required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	files, err := os.ReadDir(s.rootDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("sdk/task/file: task %q not found", taskID)
		}
		return nil, err
	}
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".index.json") {
			continue
		}
		doc, err := s.readIndexByPath(filepath.Join(s.rootDir, file.Name()))
		if err != nil {
			return nil, err
		}
		for _, item := range doc.Tasks {
			if item == nil || strings.TrimSpace(item.TaskID) != taskID {
				continue
			}
			return s.hydrateEntry(doc.Session, item)
		}
	}
	return nil, fmt.Errorf("sdk/task/file: task %q not found", taskID)
}

func (s *Store) ListSession(_ context.Context, ref sdksession.SessionRef) ([]*sdktask.Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.readIndex(ref)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []*sdktask.Entry{}, nil
		}
		return nil, err
	}
	out := make([]*sdktask.Entry, 0, len(doc.Tasks))
	for _, item := range doc.Tasks {
		if item == nil {
			continue
		}
		out = append(out, sdktask.CloneEntry(item))
	}
	return out, nil
}

func (s *Store) writeFinalBlobs(entry *sdktask.Entry) (map[string]string, error) {
	if entry == nil || entry.Result == nil {
		return map[string]string{}, nil
	}
	if entry.Running {
		return map[string]string{}, nil
	}
	stdout, _ := entry.Result["stdout"].(string)
	stderr, _ := entry.Result["stderr"].(string)
	if strings.TrimSpace(stdout) == "" && strings.TrimSpace(stderr) == "" {
		return map[string]string{}, nil
	}
	records, err := s.readBlobs(entry.Session)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if records == nil {
		records = map[string]blobRecord{}
	}
	upsertBlob := func(stream string, text string) string {
		if strings.TrimSpace(text) == "" {
			return ""
		}
		id := fmt.Sprintf("blob-%s-%s-final", strings.TrimSpace(entry.TaskID), stream)
		records[id] = blobRecord{
			ID:        id,
			Kind:      blobKind,
			TaskID:    strings.TrimSpace(entry.TaskID),
			Stream:    stream,
			Text:      text,
			CreatedAt: s.now(),
		}
		return id
	}
	stdoutID := upsertBlob("stdout", stdout)
	stderrID := upsertBlob("stderr", stderr)
	if err := s.writeBlobs(entry.Session, records); err != nil {
		return nil, err
	}
	out := map[string]string{}
	if stdoutID != "" {
		out["stdout_blob"] = stdoutID
	}
	if stderrID != "" {
		out["stderr_blob"] = stderrID
	}
	return out, nil
}

func (s *Store) hydrateEntry(session sdksession.SessionRef, entry *sdktask.Entry) (*sdktask.Entry, error) {
	entry = sdktask.CloneEntry(entry)
	if entry == nil {
		return nil, fmt.Errorf("sdk/task/file: entry is required")
	}
	stdoutBlob, _ := entry.Result["stdout_blob"].(string)
	stderrBlob, _ := entry.Result["stderr_blob"].(string)
	if stdoutBlob == "" && stderrBlob == "" {
		return entry, nil
	}
	records, err := s.readBlobs(session)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if entry.Result == nil {
		entry.Result = map[string]any{}
	}
	if stdoutBlob != "" {
		if record, ok := records[stdoutBlob]; ok {
			entry.Result["stdout"] = record.Text
		}
	}
	if stderrBlob != "" {
		if record, ok := records[stderrBlob]; ok {
			entry.Result["stderr"] = record.Text
		}
	}
	return entry, nil
}

func (s *Store) readIndex(ref sdksession.SessionRef) (indexDocument, error) {
	return s.readIndexByPath(s.indexPath(ref.SessionID))
}

func (s *Store) readIndexByPath(path string) (indexDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return indexDocument{}, err
	}
	var doc indexDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return indexDocument{}, err
	}
	doc.Session = sdksession.NormalizeSessionRef(doc.Session)
	for i, entry := range doc.Tasks {
		doc.Tasks[i] = sdktask.CloneEntry(entry)
	}
	return doc, nil
}

func (s *Store) writeIndex(doc indexDocument) error {
	if err := os.MkdirAll(s.rootDir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	path := s.indexPath(doc.Session.SessionID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) readBlobs(ref sdksession.SessionRef) (map[string]blobRecord, error) {
	path := s.blobPath(ref.SessionID)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	records := map[string]blobRecord{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record blobRecord
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			return nil, err
		}
		records[strings.TrimSpace(record.ID)] = record
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func (s *Store) writeBlobs(ref sdksession.SessionRef, records map[string]blobRecord) error {
	if err := os.MkdirAll(s.rootDir, 0o755); err != nil {
		return err
	}
	ids := make([]string, 0, len(records))
	for id := range records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var lines []string
	for _, id := range ids {
		raw, err := json.Marshal(records[id])
		if err != nil {
			return err
		}
		lines = append(lines, string(raw))
	}
	path := s.blobPath(ref.SessionID)
	tmp := path + ".tmp"
	content := strings.Join(lines, "\n")
	if content != "" {
		content += "\n"
	}
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (s *Store) indexPath(sessionID string) string {
	return filepath.Join(s.rootDir, strings.TrimSpace(sessionID)+".index.json")
}

func (s *Store) blobPath(sessionID string) string {
	return filepath.Join(s.rootDir, strings.TrimSpace(sessionID)+".blobs.jsonl")
}

func (s *Store) now() time.Time {
	return s.clock()
}
