package schema

import "encoding/json"

const (
	JSONRPCVersion         = "2.0"
	CurrentProtocolVersion = 1

	MethodInitialize       = "initialize"
	MethodAuthenticate     = "authenticate"
	MethodSessionNew       = "session/new"
	MethodSessionLoad      = "session/load"
	MethodSessionSetMode   = "session/set_mode"
	MethodSessionSetConfig = "session/set_config_option"
	MethodSessionPrompt    = "session/prompt"
	MethodSessionCancel    = "session/cancel"

	StopReasonEndTurn   = "end_turn"
	StopReasonCancelled = "cancelled"
)

type Implementation struct {
	Name    string `json:"name,omitempty"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version,omitempty"`
}

type InitializeRequest struct {
	ProtocolVersion    int             `json:"protocolVersion"`
	ClientCapabilities map[string]any  `json:"clientCapabilities,omitempty"`
	ClientInfo         *Implementation `json:"clientInfo,omitempty"`
}

type AgentCapabilities struct {
	Auth                map[string]any             `json:"auth,omitempty"`
	LoadSession         bool                       `json:"loadSession,omitempty"`
	MCPCapabilities     MCPCapabilities            `json:"mcpCapabilities,omitempty"`
	PromptCapabilities  PromptCapabilities         `json:"promptCapabilities,omitempty"`
	SessionCapabilities map[string]json.RawMessage `json:"sessionCapabilities,omitempty"`
}

type MCPCapabilities struct {
	HTTP bool `json:"http"`
	SSE  bool `json:"sse"`
}

type PromptCapabilities struct {
	Audio           bool `json:"audio"`
	EmbeddedContext bool `json:"embeddedContext"`
	Image           bool `json:"image"`
}

type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities `json:"agentCapabilities"`
	AgentInfo         *Implementation   `json:"agentInfo,omitempty"`
	AuthMethods       []json.RawMessage `json:"authMethods,omitempty"`
}

type AuthenticateRequest struct {
	MethodID string `json:"methodId"`
}

type AuthenticateResponse struct{}

type NewSessionRequest struct {
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

type NewSessionResponse struct {
	SessionID     string                `json:"sessionId"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
}

type LoadSessionRequest struct {
	SessionID  string            `json:"sessionId"`
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

type LoadSessionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
}

type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SessionModeState struct {
	AvailableModes []SessionMode `json:"availableModes"`
	CurrentModeID  string        `json:"currentModeId"`
}

type SetSessionModeRequest struct {
	SessionID string `json:"sessionId"`
	ModeID    string `json:"modeId"`
}

type SetSessionModeResponse struct{}

type SessionConfigSelectOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SessionConfigOption struct {
	Type         string                      `json:"type"`
	ID           string                      `json:"id"`
	Name         string                      `json:"name"`
	Description  string                      `json:"description,omitempty"`
	Category     string                      `json:"category,omitempty"`
	CurrentValue any                         `json:"currentValue"`
	Options      []SessionConfigSelectOption `json:"options,omitempty"`
}

type SetSessionConfigOptionRequest struct {
	SessionID string `json:"sessionId"`
	ConfigID  string `json:"configId"`
	Type      string `json:"type,omitempty"`
	Value     any    `json:"value"`
}

type SetSessionConfigOptionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}

type PromptRequest struct {
	SessionID string            `json:"sessionId"`
	MessageID *string           `json:"messageId,omitempty"`
	Prompt    []json.RawMessage `json:"prompt"`
}

type PromptResponse struct {
	StopReason string `json:"stopReason"`
}

type CancelNotification struct {
	SessionID string `json:"sessionId"`
}
