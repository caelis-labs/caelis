package viewmodel

type HomeView struct {
	AppName        string           `json:"app_name,omitempty"`
	Version        string           `json:"version,omitempty"`
	VersionLabel   string           `json:"version_label,omitempty"`
	Workspace      string           `json:"workspace,omitempty"`
	WorkspaceLabel string           `json:"workspace_label,omitempty"`
	ModelAlias     string           `json:"model_alias,omitempty"`
	Mode           string           `json:"mode,omitempty"`
	Status         StatusView       `json:"status,omitempty"`
	Diagnostics    []HomeDiagnostic `json:"diagnostics,omitempty"`
	Actions        []HomeAction     `json:"actions,omitempty"`
	Commands       []CommandView    `json:"commands,omitempty"`
}

type HomeDiagnostic struct {
	Severity  string            `json:"severity,omitempty"`
	Source    string            `json:"source,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	Message   string            `json:"message,omitempty"`
	ActionIDs []string          `json:"action_ids,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

type HomeAction struct {
	ID          string `json:"id,omitempty"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Command     string `json:"command,omitempty"`
	Enabled     bool   `json:"enabled"`
}
