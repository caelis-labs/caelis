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
	Handled    bool         `json:"handled,omitempty"`
	Command    string       `json:"command,omitempty"`
	Output     string       `json:"output,omitempty"`
	SessionRef *session.Ref `json:"session_ref,omitempty"`
}
