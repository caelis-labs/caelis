package viewmodel

import "github.com/OnslaughtSnail/caelis/core/session"

type CommandCatalogView struct {
	Commands []CommandView `json:"commands,omitempty"`
}

type CommandView struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	InputHint   string `json:"input_hint,omitempty"`
}

type CommandExecutionView struct {
	Handled           bool                 `json:"handled,omitempty"`
	Command           string               `json:"command,omitempty"`
	Output            string               `json:"output,omitempty"`
	Events            []session.Event      `json:"events,omitempty"`
	SessionRef        *session.Ref         `json:"session_ref,omitempty"`
	SettingsPanel     *SettingsPanelView   `json:"settings_panel,omitempty"`
	TaskPanel         *TaskPanelView       `json:"task_panel,omitempty"`
	ControllerPanel   *ControllerPanelView `json:"controller_panel,omitempty"`
	ModelConnectPanel *ModelConnectView    `json:"model_connect_panel,omitempty"`
	AgentManagement   *AgentManagementView `json:"agent_management,omitempty"`
}
