package schema

import "encoding/json"

const (
	JSONRPCVersion         = "2.0"
	CurrentProtocolVersion = 1

	MethodInitialize       = "initialize"
	MethodAuthenticate     = "authenticate"
	MethodSessionNew       = "session/new"
	MethodSessionLoad      = "session/load"
	MethodSessionResume    = "session/resume"
	MethodSessionClose     = "session/close"
	MethodSessionSetMode   = "session/set_mode"
	MethodSessionSetConfig = "session/set_config_option"
	MethodSessionSetModel  = "session/set_model"
	MethodSessionPrompt    = "session/prompt"
	MethodSessionCancel    = "session/cancel"
	// MethodSessionMessage is the Caelis ACP v1 extension for bidirectional
	// mid-turn Agent messages. Support is negotiated through _meta.
	MethodSessionMessage = "_caelis.dev/session/message"

	StopReasonEndTurn   = "end_turn"
	StopReasonCancelled = "cancelled"

	ErrorCodeAuthRequired = -32000
)

type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
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
	Meta                map[string]json.RawMessage `json:"_meta,omitempty"`
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

// AuthMethod is the normalized v1 authentication descriptor advertised during
// initialize. A missing type is the stable agent-managed authenticate flow.
// Terminal is the ACP Preview out-of-band flow.
type AuthMethod struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Description string            `json:"description,omitempty"`
	Type        string            `json:"type,omitempty"`
	Args        []string          `json:"args,omitempty"`
	Env         map[string]string `json:"env,omitempty"`
}

type NewSessionRequest struct {
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
	Meta       map[string]any    `json:"_meta,omitempty"`
}

type NewSessionResponse struct {
	SessionID     string                `json:"sessionId"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
	Models        *SessionModelState    `json:"models,omitempty"`
}

type LoadSessionRequest struct {
	SessionID  string            `json:"sessionId"`
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
}

type LoadSessionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
	Models        *SessionModelState    `json:"models,omitempty"`
}

type ResumeSessionRequest struct {
	SessionID  string            `json:"sessionId"`
	CWD        string            `json:"cwd"`
	MCPServers []json.RawMessage `json:"mcpServers"`
	Meta       map[string]any    `json:"_meta,omitempty"`
}

type ResumeSessionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
	Models        *SessionModelState    `json:"models,omitempty"`
}

type CloseSessionRequest struct {
	SessionID string `json:"sessionId"`
}

type CloseSessionResponse struct{}

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

type ModelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type SessionModelState struct {
	CurrentModelID  string      `json:"currentModelId"`
	AvailableModels []ModelInfo `json:"availableModels"`
}

type SetSessionModelRequest struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

type SetSessionModelResponse struct{}

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

type SessionMessageRequest struct {
	SessionID string `json:"sessionId"`
	MessageID string `json:"messageId"`
	To        string `json:"to"`
	From      string `json:"from,omitempty"`
	Message   string `json:"message"`
}

type SessionMessageResponse struct {
	MessageID   string `json:"messageId"`
	Accepted    bool   `json:"accepted"`
	State       string `json:"state,omitempty"`
	TurnID      string `json:"turnId,omitempty"`
	StartedTurn bool   `json:"startedTurn,omitempty"`
}

type CancelNotification struct {
	SessionID string `json:"sessionId"`
}
