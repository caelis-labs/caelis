// Package client adapts the product-neutral ACP SDK to Caelis's external-Agent
// process and ingress policy. Transport and request lifecycle remain SDK-owned.
package client

import (
	"encoding/json"

	acpsdk "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/caelis/internal/acpagentbridge/steeringwire"
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
type NewSessionRequest = acpsdk.NewSessionRequest
type LoadSessionRequest = acpsdk.LoadSessionRequest
type ResumeSessionRequest = acpsdk.ResumeSessionRequest

// SessionMode is the tolerant external-Agent mode descriptor.
type SessionMode struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionModeState retains the external Agent's optional initial mode state.
type SessionModeState struct {
	AvailableModes []SessionMode `json:"availableModes"`
	CurrentModeID  string        `json:"currentModeId"`
}

// SessionConfigSelectOption is one external-Agent select choice.
type SessionConfigSelectOption struct {
	Value       string `json:"value"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// SessionConfigOption retains a tolerant external-Agent configuration option.
// Standard options are validated through acp-go-sdk before update ingress is
// adapted to this Host-private compatibility shape.
type SessionConfigOption struct {
	Type         string                      `json:"type"`
	ID           string                      `json:"id"`
	Name         string                      `json:"name"`
	Description  string                      `json:"description,omitempty"`
	Category     string                      `json:"category,omitempty"`
	CurrentValue any                         `json:"currentValue"`
	Options      []SessionConfigSelectOption `json:"options,omitempty"`
}

// UnmarshalJSON prefers the SDK's standard config-option union so grouped
// select choices and boolean options are normalized correctly. Older Agents
// that emit the retired flat shape remain readable through the fallback.
func (o *SessionConfigOption) UnmarshalJSON(data []byte) error {
	var standard acpsdk.SessionConfigOption
	if err := json.Unmarshal(data, &standard); err == nil {
		if normalized := sessionConfigOptionsFromSDK([]acpsdk.SessionConfigOption{standard}); len(normalized) == 1 {
			*o = normalized[0]
			return nil
		}
	}
	type flatSessionConfigOption SessionConfigOption
	var flat flatSessionConfigOption
	if err := json.Unmarshal(data, &flat); err != nil {
		return err
	}
	*o = SessionConfigOption(flat)
	return nil
}

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
type SetSessionConfigOptionRequest = acpsdk.SetSessionConfigOptionRequest

// SetSessionConfigOptionResponse is the Host-private normalized view of the
// SDK response used by external-Agent configuration policy.
type SetSessionConfigOptionResponse struct {
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}

// PromptRequest is the Host-private tolerant request sent to external Agents.
// Product agent-side wire handling uses the SDK type directly.
type PromptRequest struct {
	SessionID string            `json:"sessionId"`
	Prompt    []json.RawMessage `json:"prompt"`
}

// PromptResponse retains unknown future stop reasons from external Agents.
// Product agent-side responses use acp-go-sdk directly.
type PromptResponse struct {
	StopReason string `json:"stopReason"`
}
type SessionSteeringOutcome = steeringwire.SessionSteeringOutcome
type SessionSteeringIdleBehavior = steeringwire.SessionSteeringIdleBehavior
type SessionSteeringCapability = steeringwire.SessionSteeringCapability
type SessionSteeringOptions = steeringwire.SessionSteeringOptions
type SessionSteeringRequest = steeringwire.SessionSteeringRequest
type SessionSteeringResponse = steeringwire.SessionSteeringResponse
type CancelRequest = acpsdk.CancelNotification
type CurrentModeUpdate = acpsdk.SessionCurrentModeUpdate
type RequestPermissionRequest = acpsdk.RequestPermissionRequest
type RequestPermissionResponse = acpsdk.RequestPermissionResponse

const (
	SessionSteeringMetaKey = steeringwire.SessionSteeringMetaKey

	SessionSteeringInjected       = steeringwire.SessionSteeringInjected
	SessionSteeringStartedNewTurn = steeringwire.SessionSteeringStartedNewTurn
	SessionSteeringPromptRequired = steeringwire.SessionSteeringPromptRequired
	SessionSteeringFailed         = steeringwire.SessionSteeringFailed

	SessionSteeringIdlePromptRequired = steeringwire.SessionSteeringIdlePromptRequired
)

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
