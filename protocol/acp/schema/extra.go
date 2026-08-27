package schema

const (
	MethodSessionList   = "session/list"
	UpdateAvailableCmds = "available_commands_update"
	UpdateCurrentMode   = "current_mode_update"
	UpdateConfigOption  = "config_option_update"
	UpdateSessionInfo   = "session_info_update"
)

type ImageContent struct {
	Type     string `json:"type"`
	MimeType string `json:"mimeType,omitempty"`
	Data     string `json:"data,omitempty"`
	Name     string `json:"name,omitempty"`
	URI      string `json:"uri,omitempty"`
}

type SessionSummary struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd,omitempty"`
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

type SessionListRequest struct {
	Cursor string `json:"cursor,omitempty"`
	CWD    string `json:"cwd,omitempty"`
}

type SessionListResponse struct {
	Sessions   []SessionSummary `json:"sessions"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

type CurrentModeUpdate struct {
	SessionUpdate string `json:"sessionUpdate"`
	CurrentModeID string `json:"currentModeId"`
}

func (u CurrentModeUpdate) SessionUpdateType() string { return u.SessionUpdate }

type ConfigOptionUpdate struct {
	SessionUpdate string                `json:"sessionUpdate"`
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}

func (u ConfigOptionUpdate) SessionUpdateType() string { return u.SessionUpdate }

type SessionInfoUpdate struct {
	SessionUpdate string  `json:"sessionUpdate"`
	Title         *string `json:"title,omitempty"`
	UpdatedAt     *string `json:"updatedAt,omitempty"`
}

func (u SessionInfoUpdate) SessionUpdateType() string { return u.SessionUpdate }

type AvailableCommandInput struct {
	Hint string `json:"hint,omitempty"`
}

type AvailableCommand struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Input       *AvailableCommandInput `json:"input,omitempty"`
}

type AvailableCommandsUpdate struct {
	SessionUpdate     string             `json:"sessionUpdate"`
	AvailableCommands []AvailableCommand `json:"availableCommands"`
}

func (u AvailableCommandsUpdate) SessionUpdateType() string { return u.SessionUpdate }
