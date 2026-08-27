package acp

import "github.com/caelis-labs/caelis/protocol/acp/schema"

const (
	UpdateUserMessage   = schema.UpdateUserMessage
	UpdateAgentMessage  = schema.UpdateAgentMessage
	UpdateAgentThought  = schema.UpdateAgentThought
	UpdateToolCall      = schema.UpdateToolCall
	UpdateToolCallInfo  = schema.UpdateToolCallInfo
	UpdatePlan          = schema.UpdatePlan
	UpdateAvailableCmds = schema.UpdateAvailableCmds
	UpdateCurrentMode   = schema.UpdateCurrentMode
	UpdateConfigOption  = schema.UpdateConfigOption
	UpdateSessionInfo   = schema.UpdateSessionInfo
)

const (
	ToolStatusPending    = schema.ToolStatusPending
	ToolStatusInProgress = schema.ToolStatusInProgress
	ToolStatusCompleted  = schema.ToolStatusCompleted
	ToolStatusFailed     = schema.ToolStatusFailed
)

const (
	ToolKindRead    = schema.ToolKindRead
	ToolKindEdit    = schema.ToolKindEdit
	ToolKindSearch  = schema.ToolKindSearch
	ToolKindExecute = schema.ToolKindExecute
)

const (
	PermAllowOnce  = schema.PermAllowOnce
	PermRejectOnce = schema.PermRejectOnce
)

type Update = schema.Update
type SessionNotification = schema.SessionNotification
type SessionSummary = schema.SessionSummary
type SessionListRequest = schema.SessionListRequest
type SessionListResponse = schema.SessionListResponse
type TextContent = schema.TextContent
type ToolCallLocation = schema.ToolCallLocation
type ToolCallContent = schema.ToolCallContent
type ContentChunk = schema.ContentChunk
type ToolCall = schema.ToolCall
type ToolCallUpdate = schema.ToolCallUpdate
type PlanEntry = schema.PlanEntry
type PlanUpdate = schema.PlanUpdate
type CurrentModeUpdate = schema.CurrentModeUpdate
type ConfigOptionUpdate = schema.ConfigOptionUpdate
type SessionInfoUpdate = schema.SessionInfoUpdate
type PermissionOption = schema.PermissionOption
type RequestPermissionRequest = schema.RequestPermissionRequest
type PermissionOutcome = schema.PermissionOutcome
type RequestPermissionResponse = schema.RequestPermissionResponse
