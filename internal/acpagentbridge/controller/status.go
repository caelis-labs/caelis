package controller

import (
	"context"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
)

// ControllerCommand is one slash command declared by a remote ACP controller.
type ControllerCommand struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ControllerConfigChoice is one selectable remote config value.
type ControllerConfigChoice struct {
	Value       string `json:"value,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ControllerConfigOption is the product UI view of a remote config option.
type ControllerConfigOption struct {
	ID           string                   `json:"id,omitempty"`
	Name         string                   `json:"name,omitempty"`
	Type         string                   `json:"type,omitempty"`
	Category     string                   `json:"category,omitempty"`
	Description  string                   `json:"description,omitempty"`
	CurrentValue string                   `json:"current_value,omitempty"`
	Options      []ControllerConfigChoice `json:"options,omitempty"`
}

// ControllerMode is one remote mode exposed to product configuration UI.
type ControllerMode struct {
	ID          string `json:"id,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

// ControllerStatus summarizes live remote state for Caelis product surfaces.
type ControllerStatus struct {
	SessionRef           session.SessionRef                  `json:"session_ref,omitempty"`
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

// SetControllerModelRequest changes product-selected remote model config.
type SetControllerModelRequest struct {
	SessionRef      session.SessionRef `json:"session_ref,omitempty"`
	Model           string             `json:"model,omitempty"`
	ReasoningEffort string             `json:"reasoning_effort,omitempty"`
}

// SetControllerModeRequest changes product-selected remote session mode.
type SetControllerModeRequest struct {
	SessionRef session.SessionRef `json:"session_ref,omitempty"`
	Mode       string             `json:"mode,omitempty"`
}

// StatusProvider is the product-facing remote controller status capability.
type StatusProvider interface {
	ControllerStatus(context.Context, session.SessionRef) (ControllerStatus, bool, error)
}

// Configurator is the product-facing remote controller config capability.
type Configurator interface {
	SetControllerModel(context.Context, SetControllerModelRequest) (ControllerStatus, error)
	SetControllerMode(context.Context, SetControllerModeRequest) (ControllerStatus, error)
}
