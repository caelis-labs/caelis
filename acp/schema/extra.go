package schema

const (
	MethodSessionList    = "session/list"
	MethodReadTextFile   = "fs/read_text_file"
	MethodWriteTextFile  = "fs/write_text_file"
	MethodTerminalCreate = "terminal/create"
	UpdateAvailableCmds  = "available_commands_update"
	UpdateCurrentMode    = "current_mode_update"
	UpdateConfigOption   = "config_option_update"
	UpdateSessionInfo    = "session_info_update"
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

type ConfigOptionUpdate struct {
	SessionUpdate string                `json:"sessionUpdate"`
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}

type SessionInfoUpdate struct {
	SessionUpdate string  `json:"sessionUpdate"`
	Title         *string `json:"title,omitempty"`
	UpdatedAt     *string `json:"updatedAt,omitempty"`
}

type EnvVariable struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type CreateTerminalRequest struct {
	SessionID       string        `json:"sessionId"`
	Command         string        `json:"command"`
	Args            []string      `json:"args,omitempty"`
	CWD             string        `json:"cwd,omitempty"`
	Env             []EnvVariable `json:"env,omitempty"`
	OutputByteLimit *int          `json:"outputByteLimit,omitempty"`
}

type CreateTerminalResponse struct {
	TerminalID string `json:"terminalId"`
}

type ReadTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      *int   `json:"line,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

type ReadTextFileResponse struct {
	Content string `json:"content"`
}

type WriteTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

type WriteTextFileResponse struct{}
