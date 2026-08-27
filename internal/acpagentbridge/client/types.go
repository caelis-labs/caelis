// Package client adapts the product-neutral ACP SDK to Caelis's external-Agent
// process and ingress policy. Transport and request lifecycle remain SDK-owned.
package client

import (
	"encoding/json"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const (
	ErrorCodeAuthRequired = -32000

	MethodInitialize           = acpsdk.AgentMethodInitialize
	MethodAuthenticate         = acpsdk.AgentMethodAuthenticate
	MethodSessionNew           = acpsdk.AgentMethodSessionNew
	MethodSessionList          = acpsdk.AgentMethodSessionList
	MethodSessionLoad          = acpsdk.AgentMethodSessionLoad
	MethodSessionResume        = acpsdk.AgentMethodSessionResume
	MethodSessionClose         = acpsdk.AgentMethodSessionClose
	MethodSessionSetMode       = acpsdk.AgentMethodSessionSetMode
	MethodSessionSetConfig     = acpsdk.AgentMethodSessionSetConfigOption
	MethodSessionSetModel      = "session/set_model"
	MethodSessionPrompt        = acpsdk.AgentMethodSessionPrompt
	MethodSessionCancel        = acpsdk.AgentMethodSessionCancel
	MethodSessionSteering      = "_session/steering"
	MethodSessionUpdate        = acpsdk.ClientMethodSessionUpdate
	MethodSessionReqPermission = acpsdk.ClientMethodSessionRequestPermission
)

const (
	UpdateUserMessage   = schema.UpdateUserMessage
	UpdateAgentMessage  = schema.UpdateAgentMessage
	UpdateAgentThought  = schema.UpdateAgentThought
	UpdateToolCall      = schema.UpdateToolCall
	UpdateToolCallState = schema.UpdateToolCallInfo
	UpdateAvailableCmds = schema.UpdateAvailableCmds
	UpdatePlan          = schema.UpdatePlan
	UpdateUsage         = schema.UpdateUsage
	UpdateCurrentMode   = schema.UpdateCurrentMode
	UpdateConfigOption  = schema.UpdateConfigOption
	UpdateSessionInfo   = schema.UpdateSessionInfo
)

// Implementation is the tolerant external-Agent implementation descriptor.
// The Host-private client keeps the compatibility representation separate
// from the strict SDK type used by the product's agent-side Surface.
type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

// AgentCapabilities retains open external-Agent capability sections that the
// Host must inspect without rejecting newer peers.
type AgentCapabilities struct {
	Auth                map[string]any             `json:"auth,omitempty"`
	LoadSession         bool                       `json:"loadSession,omitempty"`
	McpCapabilities     acpsdk.McpCapabilities     `json:"mcpCapabilities,omitempty"`
	PromptCapabilities  acpsdk.PromptCapabilities  `json:"promptCapabilities,omitempty"`
	SessionCapabilities map[string]json.RawMessage `json:"sessionCapabilities,omitempty"`
	Meta                map[string]json.RawMessage `json:"_meta,omitempty"`
}

// InitializeResponse intentionally retains raw authentication methods and an
// open session-capability map. External Agent ingress must remain tolerant of
// newer descriptors while the Surface-owned agent side uses acp-go-sdk types.
type InitializeResponse struct {
	ProtocolVersion   int                        `json:"protocolVersion"`
	AgentCapabilities AgentCapabilities          `json:"agentCapabilities"`
	AgentInfo         *Implementation            `json:"agentInfo,omitempty"`
	AuthMethods       []json.RawMessage          `json:"authMethods,omitempty"`
	Meta              map[string]json.RawMessage `json:"_meta,omitempty"`
}
type AuthenticateRequest = acpsdk.AuthenticateRequest
type AuthenticateResponse = acpsdk.AuthenticateResponse
type NewSessionRequest = schema.NewSessionRequest
type LoadSessionRequest = schema.LoadSessionRequest
type ResumeSessionRequest = schema.ResumeSessionRequest

// NewSessionResponse retains the legacy models field only for external Agents
// that do not expose the standard model session config option.
type NewSessionResponse struct {
	SessionID     string                `json:"sessionId"`
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
	Models        *SessionModelState    `json:"models,omitempty"`
}

// LoadSessionResponse is the external-Agent load response compatibility DTO.
type LoadSessionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
	Models        *SessionModelState    `json:"models,omitempty"`
}

// ResumeSessionResponse is the external-Agent resume response compatibility DTO.
type ResumeSessionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions,omitempty"`
	Modes         *SessionModeState     `json:"modes,omitempty"`
	Models        *SessionModelState    `json:"models,omitempty"`
}

// ModelInfo is one legacy external-Agent model descriptor.
type ModelInfo struct {
	ModelID     string `json:"modelId"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModelState is the retired ACP model channel retained as a fallback
// for external Agents that advertise no standard model config option.
type SessionModelState struct {
	CurrentModelID  string      `json:"currentModelId"`
	AvailableModels []ModelInfo `json:"availableModels"`
}

type CloseSessionRequest = acpsdk.CloseSessionRequest
type CloseSessionResponse = acpsdk.CloseSessionResponse
type SetSessionModeRequest = acpsdk.SetSessionModeRequest
type SetSessionModeResponse = acpsdk.SetSessionModeResponse

// SetSessionModelRequest is the legacy external-Agent model mutation request.
type SetSessionModelRequest struct {
	SessionID string `json:"sessionId"`
	ModelID   string `json:"modelId"`
}

// SetSessionModelResponse is the empty legacy model mutation response.
type SetSessionModelResponse struct{}
type SetSessionConfigOptionRequest = schema.SetSessionConfigOptionRequest
type SetSessionConfigOptionResponse = schema.SetSessionConfigOptionResponse
type PromptRequest = schema.PromptRequest
type PromptResponse = schema.PromptResponse
type SessionSteeringOutcome = schema.SessionSteeringOutcome
type SessionSteeringIdleBehavior = schema.SessionSteeringIdleBehavior
type SessionSteeringCapability = schema.SessionSteeringCapability
type SessionSteeringOptions = schema.SessionSteeringOptions
type SessionSteeringRequest = schema.SessionSteeringRequest
type SessionSteeringResponse = schema.SessionSteeringResponse
type SessionMode = schema.SessionMode
type SessionModeState = schema.SessionModeState
type SessionConfigSelectOption = schema.SessionConfigSelectOption
type SessionConfigOption = schema.SessionConfigOption
type CancelRequest = acpsdk.CancelNotification
type ToolCallLocation = schema.ToolCallLocation
type ToolCallContent = schema.ToolCallContent
type ToolCall = schema.ToolCall
type ToolCallUpdate = schema.ToolCallUpdate
type PlanEntry = schema.PlanEntry
type PlanUpdate = schema.PlanUpdate
type UsageUpdate = schema.UsageUpdate
type UsageCost = schema.UsageCost
type CurrentModeUpdate = acpsdk.SessionCurrentModeUpdate
type PermissionOption = schema.PermissionOption
type RequestPermissionRequest = schema.RequestPermissionRequest
type RequestPermissionResponse = schema.RequestPermissionResponse
type PermissionOutcome = schema.PermissionOutcome
type TextContent = schema.TextContent
type RawUpdate = schema.RawUpdate

const (
	SessionSteeringMetaKey = schema.SessionSteeringMetaKey

	SessionSteeringInjected       = schema.SessionSteeringInjected
	SessionSteeringStartedNewTurn = schema.SessionSteeringStartedNewTurn
	SessionSteeringPromptRequired = schema.SessionSteeringPromptRequired
	SessionSteeringFailed         = schema.SessionSteeringFailed

	SessionSteeringIdlePromptRequired = schema.SessionSteeringIdlePromptRequired
)

// SessionNotification retains the raw update union until provider
// compatibility has been applied.
type SessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// ContentChunk retains the raw content union for delayed decoding.
type ContentChunk struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content"`
	MessageID     string          `json:"messageId,omitempty"`
	Meta          map[string]any  `json:"_meta,omitempty"`
}

type TextChunk struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type AvailableCommandsUpdate = acpsdk.SessionAvailableCommandsUpdate

type ConfigOptionUpdate struct {
	SessionUpdate string                `json:"sessionUpdate"`
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}

// SessionInfoUpdate retains field presence after the SDK has validated the
// standard wire variant. ACP uses an explicit null to clear either field, while
// an absent field leaves the existing value unchanged.
type SessionInfoUpdate struct {
	SessionUpdate    string
	Title            *string
	TitlePresent     bool
	UpdatedAt        *string
	UpdatedAtPresent bool
}

type Update any

type UpdateEnvelope struct {
	SessionID string
	Update    Update
	Raw       json.RawMessage
}
