package viewmodel

import (
	"fmt"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/core/sandbox"
)

type TaskListView struct {
	Supported bool       `json:"supported"`
	Count     int        `json:"count,omitempty"`
	Tasks     []TaskItem `json:"tasks,omitempty"`
}

type TaskItem struct {
	ID              string    `json:"id,omitempty"`
	Kind            string    `json:"kind,omitempty"`
	Source          string    `json:"source,omitempty"`
	Title           string    `json:"title,omitempty"`
	Backend         string    `json:"backend,omitempty"`
	Action          string    `json:"action,omitempty"`
	State           string    `json:"state,omitempty"`
	Running         bool      `json:"running,omitempty"`
	SupportsInput   bool      `json:"supports_input,omitempty"`
	Command         string    `json:"command,omitempty"`
	CWD             string    `json:"cwd,omitempty"`
	TerminalID      string    `json:"terminal_id,omitempty"`
	Agent           string    `json:"agent,omitempty"`
	RemoteSessionID string    `json:"remote_session_id,omitempty"`
	StdoutCursor    int64     `json:"stdout_cursor,omitempty"`
	StderrCursor    int64     `json:"stderr_cursor,omitempty"`
	EventID         string    `json:"event_id,omitempty"`
	TurnID          string    `json:"turn_id,omitempty"`
	ExitCode        int       `json:"exit_code,omitempty"`
	Error           string    `json:"error,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type TaskOutputView struct {
	Task               TaskItem `json:"task"`
	Stdout             string   `json:"stdout,omitempty"`
	Stderr             string   `json:"stderr,omitempty"`
	StdoutCursor       int64    `json:"stdout_cursor,omitempty"`
	StderrCursor       int64    `json:"stderr_cursor,omitempty"`
	StdoutDroppedBytes int64    `json:"stdout_dropped_bytes,omitempty"`
	StderrDroppedBytes int64    `json:"stderr_dropped_bytes,omitempty"`
}

func TaskItemFromSnapshot(snapshot sandbox.SessionSnapshot) TaskItem {
	meta := snapshot.Metadata
	kind := firstTaskSnapshotString(meta, "task_kind", "kind")
	if kind == "" {
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(snapshot.Command)), "SPAWN") {
			kind = "subagent"
		} else {
			kind = "command"
		}
	}
	source := firstTaskSnapshotString(meta, "source")
	if source == "" {
		source = "live"
	}
	title := firstTaskSnapshotString(meta, "title")
	if title == "" {
		title = strings.TrimSpace(snapshot.Command)
	}
	return TaskItem{
		ID:              strings.TrimSpace(snapshot.Ref.ID),
		Kind:            kind,
		Source:          source,
		Title:           title,
		Backend:         strings.TrimSpace(string(snapshot.Ref.Backend)),
		Action:          firstTaskSnapshotString(meta, "action"),
		State:           strings.TrimSpace(string(snapshot.State)),
		Running:         snapshot.Running,
		SupportsInput:   snapshot.SupportsInput,
		Command:         strings.TrimSpace(snapshot.Command),
		CWD:             strings.TrimSpace(snapshot.Dir),
		TerminalID:      strings.TrimSpace(snapshot.Terminal.ID),
		Agent:           firstTaskSnapshotString(meta, "agent"),
		RemoteSessionID: firstTaskSnapshotString(meta, "remote_session_id"),
		StdoutCursor:    firstTaskSnapshotInt64(meta, "stdout_cursor"),
		StderrCursor:    firstTaskSnapshotInt64(meta, "stderr_cursor"),
		ExitCode:        snapshot.ExitCode,
		Error:           strings.TrimSpace(snapshot.Error),
		StartedAt:       snapshot.StartedAt,
		UpdatedAt:       snapshot.UpdatedAt,
	}
}

func TaskOutputFromSnapshot(snapshot sandbox.SessionSnapshot, output sandbox.OutputSnapshot) TaskOutputView {
	return TaskOutputView{
		Task:               TaskItemFromSnapshot(snapshot),
		Stdout:             output.Stdout,
		Stderr:             output.Stderr,
		StdoutCursor:       output.Cursor.Stdout,
		StderrCursor:       output.Cursor.Stderr,
		StdoutDroppedBytes: output.StdoutDroppedBytes,
		StderrDroppedBytes: output.StderrDroppedBytes,
	}
}

func firstTaskSnapshotString(meta map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := meta[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			if out := strings.TrimSpace(typed); out != "" {
				return out
			}
		case fmt.Stringer:
			if out := strings.TrimSpace(typed.String()); out != "" {
				return out
			}
		}
	}
	return ""
}

func firstTaskSnapshotInt64(meta map[string]any, keys ...string) int64 {
	for _, key := range keys {
		value, ok := meta[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case int:
			if typed != 0 {
				return int64(typed)
			}
		case int64:
			if typed != 0 {
				return typed
			}
		case float64:
			if typed != 0 {
				return int64(typed)
			}
		}
	}
	return 0
}
