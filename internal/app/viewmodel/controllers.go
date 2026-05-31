package viewmodel

import (
	"time"

	"github.com/OnslaughtSnail/caelis/core/session"
)

// ControllerCommand is one slash command declared by a remote ACP controller.
type ControllerCommand struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ControllerConfigChoice is one selectable remote ACP controller config value.
type ControllerConfigChoice struct {
	Value       string `json:"value,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ControllerMode is one remote ACP session mode declared by the controller.
type ControllerMode struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ControllerStatus summarizes the active remote ACP controller for shared
// surface status, slash completion, and model/mode selection.
type ControllerStatus struct {
	SessionRef           session.Ref                         `json:"session_ref,omitempty"`
	Agent                string                              `json:"agent,omitempty"`
	RemoteSessionID      string                              `json:"remote_session_id,omitempty"`
	RemoteTitle          string                              `json:"remote_title,omitempty"`
	Model                string                              `json:"model,omitempty"`
	ModelOptions         []ControllerConfigChoice            `json:"model_options,omitempty"`
	ReasoningEffort      string                              `json:"reasoning_effort,omitempty"`
	EffortOptions        []ControllerConfigChoice            `json:"effort_options,omitempty"`
	EffortOptionsByModel map[string][]ControllerConfigChoice `json:"effort_options_by_model,omitempty"`
	Commands             []ControllerCommand                 `json:"commands,omitempty"`
	ConfigOptions        []ControllerConfigOption            `json:"config_options,omitempty"`
	Mode                 string                              `json:"mode,omitempty"`
	ModeOptions          []ControllerMode                    `json:"mode_options,omitempty"`
	UpdatedAt            time.Time                           `json:"updated_at,omitempty"`
}

// ControllerConfigOption is a normalized view of one remote ACP session config
// option. It is intentionally ACP-schema-free for surface consumers.
type ControllerConfigOption struct {
	ID           string                   `json:"id,omitempty"`
	Name         string                   `json:"name,omitempty"`
	Type         string                   `json:"type,omitempty"`
	Category     string                   `json:"category,omitempty"`
	Description  string                   `json:"description,omitempty"`
	CurrentValue string                   `json:"current_value,omitempty"`
	Options      []ControllerConfigChoice `json:"options,omitempty"`
}
