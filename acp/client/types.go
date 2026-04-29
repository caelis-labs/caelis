package client

import (
	"encoding/json"

	"github.com/OnslaughtSnail/caelis/acp/schema"
)

const (
	JSONRPCVersion = schema.JSONRPCVersion

	MethodInitialize           = schema.MethodInitialize
	MethodAuthenticate         = schema.MethodAuthenticate
	MethodSessionNew           = schema.MethodSessionNew
	MethodSessionList          = schema.MethodSessionList
	MethodSessionLoad          = schema.MethodSessionLoad
	MethodSessionSetMode       = schema.MethodSessionSetMode
	MethodSessionSetConfig     = schema.MethodSessionSetConfig
	MethodSessionPrompt        = schema.MethodSessionPrompt
	MethodSessionCancel        = schema.MethodSessionCancel
	MethodSessionUpdate        = schema.MethodSessionUpdate
	MethodSessionReqPermission = schema.MethodSessionReqPermission
	MethodReadTextFile         = schema.MethodReadTextFile
	MethodWriteTextFile        = schema.MethodWriteTextFile
	MethodTerminalCreate       = schema.MethodTerminalCreate
	MethodTerminalOutput       = schema.MethodTerminalOutput
	MethodTerminalWaitForExit  = schema.MethodTerminalWaitForExit
	MethodTerminalKill         = schema.MethodTerminalKill
	MethodTerminalRelease      = schema.MethodTerminalRelease
)

const (
	UpdateUserMessage   = schema.UpdateUserMessage
	UpdateAgentMessage  = schema.UpdateAgentMessage
	UpdateAgentThought  = schema.UpdateAgentThought
	UpdateToolCall      = schema.UpdateToolCall
	UpdateToolCallState = schema.UpdateToolCallInfo
	UpdateAvailableCmds = schema.UpdateAvailableCmds
	UpdatePlan          = schema.UpdatePlan
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
type SetSessionModeRequest = schema.SetSessionModeRequest
type SetSessionModeResponse = schema.SetSessionModeResponse
type SetSessionConfigOptionRequest = schema.SetSessionConfigOptionRequest
type SetSessionConfigOptionResponse = schema.SetSessionConfigOptionResponse
type PromptRequest = schema.PromptRequest
type PromptResponse = schema.PromptResponse
type SessionMode = schema.SessionMode
type SessionModeState = schema.SessionModeState
type SessionConfigSelectOption = schema.SessionConfigSelectOption
type SessionConfigOption = schema.SessionConfigOption
type CancelRequest = schema.CancelNotification
type ToolCallLocation = schema.ToolCallLocation
type ToolCallContent = schema.ToolCallContent
type ToolCall = schema.ToolCall
type ToolCallUpdate = schema.ToolCallUpdate
type PlanEntry = schema.PlanEntry
type PlanUpdate = schema.PlanUpdate
type CurrentModeUpdate = schema.CurrentModeUpdate
type SessionInfoUpdate = schema.SessionInfoUpdate
type PermissionOption = schema.PermissionOption
type RequestPermissionRequest = schema.RequestPermissionRequest
type RequestPermissionResponse = schema.RequestPermissionResponse
type PermissionOutcome = schema.PermissionOutcome
type EnvVariable = schema.EnvVariable
type CreateTerminalRequest = schema.CreateTerminalRequest
type CreateTerminalResponse = schema.CreateTerminalResponse
type TerminalOutputRequest = schema.TerminalOutputRequest
type TerminalExitStatus = schema.TerminalExitStatus
type TerminalOutputResponse = schema.TerminalOutputResponse
type WaitForTerminalExitRequest = schema.TerminalWaitForExitRequest
type WaitForTerminalExitResponse = schema.TerminalWaitForExitResponse
type KillTerminalRequest = schema.TerminalKillRequest
type ReleaseTerminalRequest = schema.TerminalReleaseRequest
type ReadTextFileRequest = schema.ReadTextFileRequest
type ReadTextFileResponse = schema.ReadTextFileResponse
type WriteTextFileRequest = schema.WriteTextFileRequest
type WriteTextFileResponse = schema.WriteTextFileResponse
type TextContent = schema.TextContent
type ImageContent = schema.ImageContent

type CancelResponse struct{}

// SessionNotification stays client-local so the raw update payload remains a
// json.RawMessage for delayed, type-specific decoding.
type SessionNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

// ContentChunk stays client-local for the same reason: callers want access to
// the raw content payload before choosing a concrete content shape.
type ContentChunk struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content"`
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
}
