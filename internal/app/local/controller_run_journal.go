package local

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OnslaughtSnail/caelis/core/model"
	"github.com/OnslaughtSnail/caelis/core/session"
	"github.com/OnslaughtSnail/caelis/internal/engine/control"
)

const (
	controllerRunJournalKind    = "caelis.controller_run"
	controllerRunJournalVersion = 1
)

type controllerRunJournal struct {
	root string
	mu   sync.Mutex
}

type controllerRunJournalRecord struct {
	Kind                      string                    `json:"kind"`
	Version                   int                       `json:"version"`
	ID                        string                    `json:"id"`
	SessionRef                session.Ref               `json:"session_ref"`
	Workspace                 session.Workspace         `json:"workspace,omitempty"`
	TurnID                    string                    `json:"turn_id,omitempty"`
	Controller                session.ControllerBinding `json:"controller,omitempty"`
	RemoteSessionID           string                    `json:"remote_session_id,omitempty"`
	ControllerModel           string                    `json:"controller_model,omitempty"`
	ControllerReasoningEffort string                    `json:"controller_reasoning_effort,omitempty"`
	ControllerMode            string                    `json:"controller_mode,omitempty"`
	Input                     string                    `json:"input,omitempty"`
	ContentParts              []model.ContentPart       `json:"content_parts,omitempty"`
	ConfigOptions             []control.ConfigOption    `json:"config_options,omitempty"`
	Running                   bool                      `json:"running,omitempty"`
	Error                     string                    `json:"error,omitempty"`
	StartedAt                 time.Time                 `json:"started_at,omitempty"`
	UpdatedAt                 time.Time                 `json:"updated_at,omitempty"`
}

func newControllerRunJournal(root string) *controllerRunJournal {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	return &controllerRunJournal{root: filepath.Join(root, "controller-runs")}
}

func (j *controllerRunJournal) write(ctx context.Context, record controllerRunJournalRecord) error {
	if j == nil {
		return nil
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	record = normalizeControllerRunJournalRecord(record)
	if record.ID == "" {
		return errors.New("app/local: controller run journal record id is required")
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now().UTC()
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = record.StartedAt
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
	path := j.path(record.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (j *controllerRunJournal) delete(ctx context.Context, id string) error {
	if j == nil {
		return nil
	}
	if err := contextErr(ctx); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := os.Remove(j.path(id)); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func (j *controllerRunJournal) readRunning(ctx context.Context) ([]controllerRunJournalRecord, error) {
	if j == nil {
		return nil, nil
	}
	if err := contextErr(ctx); err != nil {
		return nil, err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	files, err := os.ReadDir(j.root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]controllerRunJournalRecord, 0, len(files))
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		record, err := readControllerRunJournalFile(filepath.Join(j.root, file.Name()))
		if err != nil {
			return nil, err
		}
		record = normalizeControllerRunJournalRecord(record)
		if record.Running {
			out = append(out, record)
		}
	}
	return out, nil
}

func (j *controllerRunJournal) path(id string) string {
	return filepath.Join(j.root, safeControllerRunJournalID(id)+".json")
}

func readControllerRunJournalFile(path string) (controllerRunJournalRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return controllerRunJournalRecord{}, err
	}
	var record controllerRunJournalRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return controllerRunJournalRecord{}, err
	}
	return normalizeControllerRunJournalRecord(record), nil
}

func normalizeControllerRunJournalRecord(record controllerRunJournalRecord) controllerRunJournalRecord {
	record.Kind = controllerRunJournalKind
	record.Version = controllerRunJournalVersion
	record.ID = strings.TrimSpace(record.ID)
	record.SessionRef = session.NormalizeRef(record.SessionRef)
	record.Workspace.Key = strings.TrimSpace(record.Workspace.Key)
	record.Workspace.CWD = strings.TrimSpace(record.Workspace.CWD)
	record.TurnID = strings.TrimSpace(record.TurnID)
	record.Controller.ID = strings.TrimSpace(record.Controller.ID)
	record.Controller.AgentName = strings.TrimSpace(record.Controller.AgentName)
	record.Controller.Label = strings.TrimSpace(record.Controller.Label)
	record.Controller.EpochID = strings.TrimSpace(record.Controller.EpochID)
	record.Controller.RemoteSessionID = strings.TrimSpace(record.Controller.RemoteSessionID)
	record.Controller.Source = strings.TrimSpace(record.Controller.Source)
	record.RemoteSessionID = firstNonEmpty(record.RemoteSessionID, record.Controller.RemoteSessionID)
	record.Controller.RemoteSessionID = firstNonEmpty(record.Controller.RemoteSessionID, record.RemoteSessionID)
	record.ControllerModel = strings.TrimSpace(record.ControllerModel)
	record.ControllerReasoningEffort = strings.TrimSpace(record.ControllerReasoningEffort)
	record.ControllerMode = strings.TrimSpace(record.ControllerMode)
	record.Input = strings.TrimSpace(record.Input)
	record.ContentParts = model.CloneContentParts(record.ContentParts)
	record.ConfigOptions = cloneControlConfigOptions(record.ConfigOptions)
	record.Error = strings.TrimSpace(record.Error)
	if record.ID == "" {
		record.ID = controllerRunRecordID(record)
	}
	return record
}

func controllerRunRecordID(record controllerRunJournalRecord) string {
	return firstNonEmpty(
		record.TurnID,
		controllerRunCompositeID(record.SessionRef.SessionID, record.Controller.EpochID),
		controllerRunCompositeID(record.SessionRef.SessionID, firstNonEmpty(record.Controller.ID, record.Controller.AgentName, record.Controller.Label)),
		record.SessionRef.SessionID,
	)
}

func controllerRunCompositeID(prefix string, suffix string) string {
	prefix = strings.TrimSpace(prefix)
	suffix = strings.TrimSpace(suffix)
	if prefix == "" || suffix == "" {
		return ""
	}
	return prefix + "-" + suffix
}

func safeControllerRunJournalID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return "controller-run"
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
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-._")
	if out == "" {
		return fmt.Sprintf("controller-run-%x", []byte(id))
	}
	return out
}
