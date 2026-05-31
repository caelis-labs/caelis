package viewmodel

type SettingsView struct {
	Runtime    RuntimeSettings    `json:"runtime"`
	Store      StoreSettings      `json:"store"`
	Sandbox    SandboxSettings    `json:"sandbox"`
	Compaction CompactionSettings `json:"compaction,omitempty"`
	Skills     SkillSettings      `json:"skills,omitempty"`
}

type SettingsPanelView struct {
	Configured    bool                      `json:"configured"`
	Settings      SettingsView              `json:"settings"`
	Runtime       RuntimeStatus             `json:"runtime"`
	Model         ModelStatus               `json:"model"`
	Agents        AgentStatus               `json:"agents"`
	Sandbox       SandboxPanel              `json:"sandbox"`
	Resources     ResourceStatus            `json:"resources"`
	Sections      []SettingsPanelSection    `json:"sections,omitempty"`
	ConfigOptions []SettingsConfigOption    `json:"config_options,omitempty"`
	Diagnostics   []SettingsPanelDiagnostic `json:"diagnostics,omitempty"`
	Actions       []SettingsPanelAction     `json:"actions,omitempty"`
}

type SandboxPanel struct {
	Status  SandboxPanelStatus    `json:"status"`
	Actions []SettingsPanelAction `json:"actions,omitempty"`
}

type SandboxPanelStatus struct {
	RequestedBackend         string `json:"requested_backend,omitempty"`
	ResolvedBackend          string `json:"resolved_backend,omitempty"`
	Route                    string `json:"route,omitempty"`
	Isolation                string `json:"isolation,omitempty"`
	DefaultPermission        string `json:"default_permission,omitempty"`
	Network                  string `json:"network,omitempty"`
	DefaultNetwork           string `json:"default_network,omitempty"`
	NetworkControl           bool   `json:"network_control,omitempty"`
	PathPolicy               bool   `json:"path_policy,omitempty"`
	ReadableRootCount        int    `json:"readable_root_count,omitempty"`
	WritableRootCount        int    `json:"writable_root_count,omitempty"`
	FallbackToHost           bool   `json:"fallback_to_host,omitempty"`
	FallbackReason           string `json:"fallback_reason,omitempty"`
	FallbackInstallHint      string `json:"fallback_install_hint,omitempty"`
	SetupRequired            bool   `json:"setup_required,omitempty"`
	SetupError               string `json:"setup_error,omitempty"`
	SetupMarkerCurrent       bool   `json:"setup_marker_current,omitempty"`
	SetupMarkerReason        string `json:"setup_marker_reason,omitempty"`
	SandboxRuntimeConfigured bool   `json:"sandbox_runtime_configured,omitempty"`
}

type SettingsPanelSection struct {
	ID          string                `json:"id,omitempty"`
	Title       string                `json:"title,omitempty"`
	Description string                `json:"description,omitempty"`
	Fields      []SettingsPanelField  `json:"fields,omitempty"`
	Actions     []SettingsPanelAction `json:"actions,omitempty"`
	Meta        map[string]string     `json:"meta,omitempty"`
}

type SettingsPanelField struct {
	ID          string                     `json:"id,omitempty"`
	ConfigID    string                     `json:"config_id,omitempty"`
	Label       string                     `json:"label,omitempty"`
	Kind        string                     `json:"kind,omitempty"`
	Category    string                     `json:"category,omitempty"`
	Description string                     `json:"description,omitempty"`
	Value       string                     `json:"value,omitempty"`
	Detail      string                     `json:"detail,omitempty"`
	Editable    bool                       `json:"editable,omitempty"`
	Sensitive   bool                       `json:"sensitive,omitempty"`
	Options     []SettingsPanelFieldOption `json:"options,omitempty"`
}

type SettingsPanelFieldOption struct {
	Value       string `json:"value,omitempty"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
}

type SettingsConfigOption struct {
	Type         string                       `json:"type,omitempty"`
	ID           string                       `json:"id,omitempty"`
	FieldID      string                       `json:"field_id,omitempty"`
	Name         string                       `json:"name,omitempty"`
	Description  string                       `json:"description,omitempty"`
	Category     string                       `json:"category,omitempty"`
	CurrentValue any                          `json:"current_value,omitempty"`
	Options      []SettingsConfigOptionChoice `json:"options,omitempty"`
}

type SettingsConfigOptionChoice struct {
	Value       string `json:"value,omitempty"`
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
}

type SettingsPanelAction struct {
	ID                   string `json:"id,omitempty"`
	Label                string `json:"label,omitempty"`
	Description          string `json:"description,omitempty"`
	Target               string `json:"target,omitempty"`
	Kind                 string `json:"kind,omitempty"`
	Enabled              bool   `json:"enabled"`
	Destructive          bool   `json:"destructive,omitempty"`
	RequiresConfirmation bool   `json:"requires_confirmation,omitempty"`
}

type SettingsPanelDiagnostic struct {
	Severity  string            `json:"severity,omitempty"`
	Source    string            `json:"source,omitempty"`
	Kind      string            `json:"kind,omitempty"`
	ID        string            `json:"id,omitempty"`
	Path      string            `json:"path,omitempty"`
	Message   string            `json:"message,omitempty"`
	ActionIDs []string          `json:"action_ids,omitempty"`
	Meta      map[string]string `json:"meta,omitempty"`
}

type RuntimeSettings struct {
	AppName      string `json:"app_name,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	WorkspaceKey string `json:"workspace_key,omitempty"`
	WorkspaceCWD string `json:"workspace_cwd,omitempty"`
	Model        string `json:"model,omitempty"`
}

type StoreSettings struct {
	Backend string `json:"backend,omitempty"`
	URI     string `json:"uri,omitempty"`
}

type SandboxSettings struct {
	Backend       string   `json:"backend,omitempty"`
	ReadableRoots []string `json:"readable_roots,omitempty"`
	WritableRoots []string `json:"writable_roots,omitempty"`
	Network       string   `json:"network,omitempty"`
	HelperPath    string   `json:"helper_path,omitempty"`
}

type CompactionSettings struct {
	Prompt               string  `json:"prompt,omitempty"`
	MaxSourceChars       int     `json:"max_source_chars,omitempty"`
	AutoMode             string  `json:"auto_mode,omitempty"`
	AutoWatermarkRatio   float64 `json:"auto_watermark_ratio,omitempty"`
	TaskIndexLimit       int     `json:"task_index_limit,omitempty"`
	ControllerIndexLimit int     `json:"controller_index_limit,omitempty"`
}

type SkillSettings struct {
	LoadingMode       string `json:"loading_mode,omitempty"`
	MaxExpansionChars int    `json:"max_expansion_chars,omitempty"`
}
