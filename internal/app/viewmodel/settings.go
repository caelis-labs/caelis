package viewmodel

type SettingsView struct {
	Runtime    RuntimeSettings    `json:"runtime"`
	Store      StoreSettings      `json:"store"`
	Sandbox    SandboxSettings    `json:"sandbox"`
	Compaction CompactionSettings `json:"compaction,omitempty"`
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
	Prompt             string  `json:"prompt,omitempty"`
	MaxSourceChars     int     `json:"max_source_chars,omitempty"`
	AutoMode           string  `json:"auto_mode,omitempty"`
	AutoWatermarkRatio float64 `json:"auto_watermark_ratio,omitempty"`
}
