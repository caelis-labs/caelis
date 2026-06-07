package acp

import (
	"context"
	"encoding/json"
)

// ─── Session lifecycle request/response types ────────────────────────

// InitializeRequest is sent by the client to initiate an ACP session.
type InitializeRequest struct {
	ProtocolVersion    int             `json:"protocolVersion"`
	ClientCapabilities map[string]any  `json:"clientCapabilities,omitempty"`
	ClientInfo         *Implementation `json:"clientInfo,omitempty"`
}

// InitializeResponse is returned by the agent after initialization.
type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
	AuthMethods       []json.RawMessage `json:"authMethods,omitempty"`
}

// Implementation identifies a client or agent implementation.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

// AgentCapabilities describes what the agent can do.
type AgentCapabilities struct {
	Auth                map[string]any             `json:"auth,omitempty"`
	LoadSession         bool                       `json:"loadSession,omitempty"`
	MCPCapabilities     MCPCapabilities            `json:"mcpCapabilities,omitempty"`
	PromptCapabilities  PromptCapabilities         `json:"promptCapabilities,omitempty"`
	SessionCapabilities map[string]json.RawMessage `json:"sessionCapabilities,omitempty"`
	Tools               []ToolCapability           `json:"tools,omitempty"`
	Streaming           bool                       `json:"streaming,omitempty"`
	MaxConcurrent       int                        `json:"maxConcurrent,omitempty"`
}

// MCPCapabilities describes supported MCP transports.
type MCPCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

// PromptCapabilities describes prompt content capabilities.
type PromptCapabilities struct {
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
	Image           bool `json:"image"`
}

// ToolCapability describes a tool the agent provides.
type ToolCapability struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// NewSessionRequest creates a new ACP session.
type NewSessionRequest struct {
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers,omitempty"`
}

// NewSessionResponse is returned after creating a session.
type NewSessionResponse struct {
	SessionID     string                `json:"sessionId"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
	Models        *SessionModelState    `json:"models,omitempty"`
}

// SessionConfigOption describes a configurable option.
type SessionConfigOption struct {
	Type         string                      `json:"type"`
	ID           string                      `json:"id"`
	Name         string                      `json:"name"`
	Description  string                      `json:"description,omitempty"`
	Category     string                      `json:"category,omitempty"`
	CurrentValue any                         `json:"currentValue"`
	Options      []SessionConfigSelectOption `json:"options,omitempty"`
}

// SessionConfigSelectOption describes a selectable config value.
type SessionConfigSelectOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModeState describes available and current modes.
type SessionModeState struct {
	AvailableModes []SessionMode `json:"availableModes"`
	CurrentModeID  string        `json:"currentModeId"`
}

// SessionMode describes a session mode.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModelState describes available and current models.
type SessionModelState struct {
	CurrentModelID  string      `json:"currentModelId"`
	AvailableModels []ModelInfo `json:"availableModels"`
}

// ModelInfo describes a model.
type ModelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Provider    string `json:"provider,omitempty"`
	MaxTokens   int    `json:"maxTokens,omitempty"`
}

// PromptRequest sends a prompt to the agent.
type PromptRequest struct {
	SessionID string            `json:"sessionId"`
	MessageID *string           `json:"messageId,omitempty"`
	Prompt    []json.RawMessage `json:"prompt"`
}

// PromptResponse is returned after the agent processes a prompt.
type PromptResponse struct {
	StopReason string `json:"stopReason"` // "end_turn" | "cancelled"
}

// LoadSessionRequest loads an existing session.
type LoadSessionRequest struct {
	SessionID  string            `json:"sessionId"`
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers,omitempty"`
}

// LoadSessionResponse is returned after loading a session.
type LoadSessionResponse struct {
	SessionID     string                `json:"sessionId"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
	Models        *SessionModelState    `json:"models,omitempty"`
}

// ResumeSessionRequest resumes an existing session.
type ResumeSessionRequest struct {
	SessionID  string            `json:"sessionId"`
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers,omitempty"`
}

// ResumeSessionResponse is returned after resuming a session.
type ResumeSessionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
	Models        *SessionModelState    `json:"models,omitempty"`
}

// CloseSessionRequest closes a session.
type CloseSessionRequest struct {
	SessionID string `json:"sessionId"`
}

// CloseSessionResponse is the response to CloseSessionRequest.
type CloseSessionResponse struct{}

// CancelNotification cancels the current agent operation.
type CancelNotification struct {
	SessionID string `json:"sessionId"`
}

// AuthenticateRequest selects an authentication method.
type AuthenticateRequest struct {
	MethodID string `json:"methodId"`
}

// AuthenticateResponse is returned after authentication.
type AuthenticateResponse struct{}

// SessionListRequest lists available sessions.
type SessionListRequest struct {
	Cursor string `json:"cursor,omitempty"`
	CWD    string `json:"cwd,omitempty"`
}

// SessionListResponse is the response to SessionListRequest.
type SessionListResponse struct {
	Sessions   []SessionSummary `json:"sessions"`
	NextCursor string           `json:"nextCursor,omitempty"`
}

// SessionSummary describes a session for listing.
type SessionSummary struct {
	SessionID string `json:"sessionId"`
	CWD       string `json:"cwd,omitempty"`
	Title     string `json:"title,omitempty"`
	UpdatedAt string `json:"updatedAt,omitempty"`
}

// SetSessionModeRequest changes the current session mode.
type SetSessionModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

// SetSessionModeResponse is returned after changing mode.
type SetSessionModeResponse struct{}

// SetSessionModelRequest changes the current model.
type SetSessionModelRequest struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

// SetSessionModelResponse is returned after changing model.
type SetSessionModelResponse struct{}

// SetSessionConfigOptionRequest changes one config option.
type SetSessionConfigOptionRequest struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Type      string `json:"type,omitempty"`
	Value     any    `json:"value"`
}

// SetSessionConfigOptionResponse is returned after changing config.
type SetSessionConfigOptionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}

// ─── Mode/Config/Model update types ─────────────────────────────────

// CurrentModeUpdate notifies the client of a mode change.
type CurrentModeUpdate struct {
	SessionUpdate UpdateKind `json:"sessionUpdate"`
	CurrentModeID string     `json:"currentModeId"`
}

func (c CurrentModeUpdate) SessionUpdateType() UpdateKind { return c.SessionUpdate }

// ConfigOptionUpdate notifies the client of a config option change.
type ConfigOptionUpdate struct {
	SessionUpdate UpdateKind            `json:"sessionUpdate"`
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}

func (c ConfigOptionUpdate) SessionUpdateType() UpdateKind { return c.SessionUpdate }

// SessionInfoUpdate notifies the client of session info changes.
type SessionInfoUpdate struct {
	SessionUpdate UpdateKind       `json:"sessionUpdate"`
	Title         *string          `json:"title,omitempty"`
	UpdatedAt     *string          `json:"updatedAt,omitempty"`
	Handoff       *HandoffInfo     `json:"handoff,omitempty"`
	Participant   *ParticipantInfo `json:"participant,omitempty"`
	Meta          map[string]any   `json:"_meta,omitempty"`
}

func (s SessionInfoUpdate) SessionUpdateType() UpdateKind { return s.SessionUpdate }

// HandoffInfo describes a control handoff between agents.
type HandoffInfo struct {
	FromAgent string `json:"fromAgent"`
	ToAgent   string `json:"toAgent"`
	Reason    string `json:"reason,omitempty"`
}

// ParticipantInfo describes a participant lifecycle update.
type ParticipantInfo struct {
	ParticipantID string            `json:"participantId"`
	Role          string            `json:"role,omitempty"`
	State         string            `json:"state,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}

// AvailableCommandInput describes the expected command input.
type AvailableCommandInput struct {
	Hint string `json:"hint,omitempty"`
}

// AvailableCommand describes a slash command exposed by the agent.
type AvailableCommand struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description,omitempty"`
	Input       *AvailableCommandInput `json:"input"`
}

// AvailableCommandsUpdate notifies clients of available slash commands.
type AvailableCommandsUpdate struct {
	SessionUpdate     UpdateKind         `json:"sessionUpdate"`
	AvailableCommands []AvailableCommand `json:"availableCommands"`
}

func (a AvailableCommandsUpdate) SessionUpdateType() UpdateKind { return a.SessionUpdate }

// ─── Filesystem RPC types ────────────────────────────────────────────

// ReadTextFileRequest reads a text file from the workspace.
type ReadTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Line      *int   `json:"line,omitempty"`
	Limit     *int   `json:"limit,omitempty"`
}

// ReadTextFileResponse is the response to ReadTextFileRequest.
type ReadTextFileResponse struct {
	Content   string `json:"content"`
	TotalSize int    `json:"totalSize,omitempty"`
}

// WriteTextFileRequest writes a text file to the workspace.
type WriteTextFileRequest struct {
	SessionID string `json:"sessionId"`
	Path      string `json:"path"`
	Content   string `json:"content"`
}

// WriteTextFileResponse is the response to WriteTextFileRequest.
type WriteTextFileResponse struct{}

// ─── ACP Agent interface ─────────────────────────────────────────────

// Agent is the contract for an ACP-compatible agent implementation.
// External ACP agents implement this interface to integrate with
// the Caelis runtime.
type Agent interface {
	// Initialize handles the ACP initialize handshake.
	Initialize(ctx context.Context, req InitializeRequest) (InitializeResponse, error)

	// NewSession creates a new session.
	NewSession(ctx context.Context, req NewSessionRequest) (NewSessionResponse, error)

	// Prompt processes a user prompt and streams updates.
	Prompt(ctx context.Context, req PromptRequest, callbacks PromptCallbacks) (PromptResponse, error)

	// Cancel cancels the current operation.
	Cancel(ctx context.Context, sessionID string) error

	// CloseSession closes a session.
	CloseSession(ctx context.Context, sessionID string) error
}

// Authenticator is implemented by agents that support authenticate.
type Authenticator interface {
	Authenticate(ctx context.Context, req AuthenticateRequest) (AuthenticateResponse, error)
}

// SessionLister is implemented by agents that can list sessions.
type SessionLister interface {
	ListSessions(ctx context.Context, req SessionListRequest) (SessionListResponse, error)
}

// SessionLoader is implemented by agents that can load sessions.
type SessionLoader interface {
	LoadSession(ctx context.Context, req LoadSessionRequest, callbacks PromptCallbacks) (LoadSessionResponse, error)
}

// SessionResumer is implemented by agents that can resume sessions.
type SessionResumer interface {
	ResumeSession(ctx context.Context, req ResumeSessionRequest) (ResumeSessionResponse, error)
}

// SessionModeSetter is implemented by agents that can change modes.
type SessionModeSetter interface {
	SetSessionMode(ctx context.Context, req SetSessionModeRequest) (SetSessionModeResponse, error)
}

// SessionConfigSetter is implemented by agents that can change config options.
type SessionConfigSetter interface {
	SetSessionConfigOption(ctx context.Context, req SetSessionConfigOptionRequest) (SetSessionConfigOptionResponse, error)
}

// SessionModelSetter is implemented by agents that can change models.
type SessionModelSetter interface {
	SetSessionModel(ctx context.Context, req SetSessionModelRequest) (SetSessionModelResponse, error)
}

// TerminalProvider is implemented by agents that serve terminal/* requests.
type TerminalProvider interface {
	CreateTerminal(context.Context, CreateTerminalRequest) (CreateTerminalResponse, error)
	TerminalOutput(context.Context, TerminalOutputRequest) (TerminalOutputResponse, error)
	TerminalWaitForExit(context.Context, TerminalWaitForExitRequest) (TerminalWaitForExitResponse, error)
	TerminalKill(context.Context, TerminalKillRequest) error
	TerminalRelease(context.Context, TerminalReleaseRequest) error
}

// FileSystemProvider is implemented by agents that serve fs/* requests.
type FileSystemProvider interface {
	ReadTextFile(context.Context, ReadTextFileRequest) (ReadTextFileResponse, error)
	WriteTextFile(context.Context, WriteTextFileRequest) (WriteTextFileResponse, error)
}

// PromptCallbacks provides callbacks for streaming updates during prompt processing.
type PromptCallbacks interface {
	// OnUpdate sends a session/update notification to the client.
	OnUpdate(SessionNotification)

	// OnPermissionRequest sends a request_permission and waits for response.
	OnPermissionRequest(RequestPermissionRequest) (RequestPermissionResponse, error)
}

// TerminalClientCallbacks is optionally implemented by PromptCallbacks that
// can ask the ACP client to manage terminals.
type TerminalClientCallbacks interface {
	TerminalProvider
}

// FileSystemClientCallbacks is optionally implemented by PromptCallbacks that
// can ask the ACP client to read and write text files.
type FileSystemClientCallbacks interface {
	FileSystemProvider
}
