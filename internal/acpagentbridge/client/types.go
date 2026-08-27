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

type Implementation = schema.Implementation
type InitializeRequest = schema.InitializeRequest
type InitializeResponse = schema.InitializeResponse
type AuthenticateRequest = schema.AuthenticateRequest
type AuthenticateResponse = schema.AuthenticateResponse
type NewSessionRequest = schema.NewSessionRequest
type NewSessionResponse = schema.NewSessionResponse
type SessionListRequest = schema.SessionListRequest
type SessionSummary = schema.SessionSummary
type SessionListResponse = schema.SessionListResponse
type LoadSessionRequest = schema.LoadSessionRequest
type LoadSessionResponse = schema.LoadSessionResponse
type ResumeSessionRequest = schema.ResumeSessionRequest
type ResumeSessionResponse = schema.ResumeSessionResponse
type CloseSessionRequest = schema.CloseSessionRequest
type CloseSessionResponse = schema.CloseSessionResponse
type SetSessionModeRequest = schema.SetSessionModeRequest
type SetSessionModeResponse = schema.SetSessionModeResponse
type SetSessionModelRequest = schema.SetSessionModelRequest
type SetSessionModelResponse = schema.SetSessionModelResponse
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
type ModelInfo = schema.ModelInfo
type SessionModelState = schema.SessionModelState
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
type CurrentModeUpdate = schema.CurrentModeUpdate
type SessionInfoUpdate = schema.SessionInfoUpdate
type PermissionOption = schema.PermissionOption
type RequestPermissionRequest = schema.RequestPermissionRequest
type RequestPermissionResponse = schema.RequestPermissionResponse
type PermissionOutcome = schema.PermissionOutcome
type TextContent = schema.TextContent
type ImageContent = schema.ImageContent
type RawUpdate = schema.RawUpdate
type AvailableCommand = schema.AvailableCommand
type AvailableCommandInput = schema.AvailableCommandInput

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

type AvailableCommandsUpdate struct {
	SessionUpdate     string           `json:"sessionUpdate"`
	AvailableCommands []map[string]any `json:"availableCommands"`
}

type ConfigOptionUpdate struct {
	SessionUpdate string                `json:"sessionUpdate"`
	ConfigOptions []SessionConfigOption `json:"configOptions"`
}

type Update any

type UpdateEnvelope struct {
	SessionID string
	Update    Update
	Raw       json.RawMessage
}
