package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
)

const (
	StopReasonEndTurn   = "end_turn"
	StopReasonCancelled = "cancelled"
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
	ProtocolVersion   int                        `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities          `json:"agentCapabilities"`
	AgentInfo         *Implementation            `json:"agentInfo,omitempty"`
	AuthMethods       []json.RawMessage          `json:"authMethods,omitempty"`
	Meta              map[string]json.RawMessage `json:"_meta,omitempty"`
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
	Meta       map[string]any    `json:"_meta,omitempty"`
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

// SessionSteeringOutcome is the Agent-selected disposition of one steering
// request. Unknown values are retained so newer peer outcomes can cross the
// wire without requiring a schema update.
type SessionSteeringOutcome string

// SessionSteeringIdleBehavior controls how an Agent reports steering input
// received without a running Turn. It does not authorize the Agent to start a
// replacement Turn.
type SessionSteeringIdleBehavior string

const (
	SessionSteeringMetaKey = "steering"

	SessionSteeringInjected       SessionSteeringOutcome = "injected"
	SessionSteeringStartedNewTurn SessionSteeringOutcome = "startedNewTurn"
	SessionSteeringPromptRequired SessionSteeringOutcome = "promptRequired"
	SessionSteeringFailed         SessionSteeringOutcome = "failed"

	SessionSteeringIdlePromptRequired SessionSteeringIdleBehavior = "promptRequired"
)

// SessionSteeringCapability is advertised at initialize response
// _meta.steering when the Agent accepts _session/steering requests.
type SessionSteeringCapability struct {
	Supported bool `json:"supported"`
}

// DecodeSessionSteeringCapability validates the recognized initialize
// response _meta.steering capability. Missing fields mean unsupported; a
// present supported field must be a JSON boolean.
func DecodeSessionSteeringCapability(meta map[string]json.RawMessage) (SessionSteeringCapability, error) {
	raw, ok := meta[SessionSteeringMetaKey]
	if !ok {
		return SessionSteeringCapability{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err == nil {
			err = fmt.Errorf("must be an object")
		}
		return SessionSteeringCapability{}, fmt.Errorf("_meta.%s: %w", SessionSteeringMetaKey, err)
	}
	supportedRaw, ok := fields["supported"]
	if !ok {
		return SessionSteeringCapability{}, nil
	}
	if bytes.Equal(bytes.TrimSpace(supportedRaw), []byte("null")) {
		return SessionSteeringCapability{}, fmt.Errorf("_meta.%s.supported must be a boolean", SessionSteeringMetaKey)
	}
	var supported bool
	if err := json.Unmarshal(supportedRaw, &supported); err != nil {
		return SessionSteeringCapability{}, fmt.Errorf("_meta.%s.supported: %w", SessionSteeringMetaKey, err)
	}
	return SessionSteeringCapability{Supported: supported}, nil
}

// SessionSteeringOptions is the recognized _meta.steering request vocabulary.
// Unknown sibling fields remain available to newer peers through the original
// request Meta value.
type SessionSteeringOptions struct {
	IdleBehavior SessionSteeringIdleBehavior `json:"idleBehavior,omitempty"`
}

// SessionSteeringRequest carries one ACP prompt to a Session without assigning
// a prompt or Turn lifecycle to the request itself.
type SessionSteeringRequest struct {
	SessionID string                     `json:"sessionId"`
	Prompt    []json.RawMessage          `json:"prompt"`
	Meta      map[string]json.RawMessage `json:"_meta,omitempty"`
}

// SessionSteeringResponse reports how the Agent accepted a steering request.
// Reason is optional extension detail, including noRunningTurn for the
// promptRequired outcome.
type SessionSteeringResponse struct {
	Outcome SessionSteeringOutcome `json:"outcome"`
	Reason  string                 `json:"reason,omitempty"`
}

// DecodeSessionSteeringOptions validates and returns the steering options
// owned by this protocol version while ignoring unknown extension fields.
func DecodeSessionSteeringOptions(meta map[string]json.RawMessage) (SessionSteeringOptions, error) {
	raw, ok := meta[SessionSteeringMetaKey]
	if !ok {
		return SessionSteeringOptions{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		if err == nil {
			err = fmt.Errorf("must be an object")
		}
		return SessionSteeringOptions{}, fmt.Errorf("_meta.%s: %w", SessionSteeringMetaKey, err)
	}
	var options SessionSteeringOptions
	if idleRaw, ok := fields["idleBehavior"]; ok {
		if err := json.Unmarshal(idleRaw, &options.IdleBehavior); err != nil {
			return SessionSteeringOptions{}, fmt.Errorf("_meta.%s.idleBehavior: %w", SessionSteeringMetaKey, err)
		}
		if options.IdleBehavior != SessionSteeringIdlePromptRequired {
			return SessionSteeringOptions{}, fmt.Errorf(
				"_meta.%s.idleBehavior %q is unsupported",
				SessionSteeringMetaKey,
				options.IdleBehavior,
			)
		}
	}
	return options, nil
}
