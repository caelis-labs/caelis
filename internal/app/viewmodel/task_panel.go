package viewmodel

import "time"

type TaskPanelView struct {
	Supported   bool                  `json:"supported"`
	Summary     TaskPanelSummary      `json:"summary,omitempty"`
	Tasks       []TaskItem            `json:"tasks,omitempty"`
	Sections    []TaskPanelSection    `json:"sections,omitempty"`
	Actions     []TaskPanelAction     `json:"actions,omitempty"`
	Diagnostics []TaskPanelDiagnostic `json:"diagnostics,omitempty"`
}

type TaskPanelSummary struct {
	Total     int       `json:"total,omitempty"`
	Running   int       `json:"running,omitempty"`
	Waiting   int       `json:"waiting,omitempty"`
	Completed int       `json:"completed,omitempty"`
	Failed    int       `json:"failed,omitempty"`
	Cancelled int       `json:"cancelled,omitempty"`
	Commands  int       `json:"commands,omitempty"`
	Subagents int       `json:"subagents,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

type TaskPanelSection struct {
	ID      string   `json:"id,omitempty"`
	Title   string   `json:"title,omitempty"`
	Count   int      `json:"count,omitempty"`
	TaskIDs []string `json:"task_ids,omitempty"`
}

type TaskPanelAction struct {
	ID            string `json:"id,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Label         string `json:"label,omitempty"`
	Command       string `json:"command,omitempty"`
	TaskID        string `json:"task_id,omitempty"`
	Enabled       bool   `json:"enabled,omitempty"`
	Destructive   bool   `json:"destructive,omitempty"`
	RequiresInput bool   `json:"requires_input,omitempty"`
}

type TaskPanelDiagnostic struct {
	Severity string `json:"severity,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Message  string `json:"message,omitempty"`
	TaskID   string `json:"task_id,omitempty"`
}
