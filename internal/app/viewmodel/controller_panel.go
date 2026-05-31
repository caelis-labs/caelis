package viewmodel

import "time"

type ControllerPanelView struct {
	Active      bool                        `json:"active"`
	Status      *ControllerStatus           `json:"status,omitempty"`
	Summary     ControllerPanelSummary      `json:"summary,omitempty"`
	Sections    []ControllerPanelSection    `json:"sections,omitempty"`
	Actions     []ControllerPanelAction     `json:"actions,omitempty"`
	Diagnostics []ControllerPanelDiagnostic `json:"diagnostics,omitempty"`
}

type ControllerPanelSummary struct {
	Agent           string    `json:"agent,omitempty"`
	RemoteSessionID string    `json:"remote_session_id,omitempty"`
	Model           string    `json:"model,omitempty"`
	ReasoningEffort string    `json:"reasoning_effort,omitempty"`
	Mode            string    `json:"mode,omitempty"`
	Phase           string    `json:"phase,omitempty"`
	Running         bool      `json:"running,omitempty"`
	Recovering      bool      `json:"recovering,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type ControllerPanelSection struct {
	ID      string                  `json:"id,omitempty"`
	Title   string                  `json:"title,omitempty"`
	Fields  []ControllerPanelField  `json:"fields,omitempty"`
	Actions []ControllerPanelAction `json:"actions,omitempty"`
}

type ControllerPanelField struct {
	ID       string                   `json:"id,omitempty"`
	Label    string                   `json:"label,omitempty"`
	Kind     string                   `json:"kind,omitempty"`
	Value    string                   `json:"value,omitempty"`
	Command  string                   `json:"command,omitempty"`
	Editable bool                     `json:"editable,omitempty"`
	Options  []ControllerConfigChoice `json:"options,omitempty"`
}

type ControllerPanelAction struct {
	ID            string `json:"id,omitempty"`
	Kind          string `json:"kind,omitempty"`
	Label         string `json:"label,omitempty"`
	Command       string `json:"command,omitempty"`
	Enabled       bool   `json:"enabled,omitempty"`
	RequiresInput bool   `json:"requires_input,omitempty"`
	Destructive   bool   `json:"destructive,omitempty"`
}

type ControllerPanelDiagnostic struct {
	Severity string            `json:"severity,omitempty"`
	Kind     string            `json:"kind,omitempty"`
	Message  string            `json:"message,omitempty"`
	Meta     map[string]string `json:"meta,omitempty"`
}
