package acp

import schema "github.com/OnslaughtSnail/caelis/acp/schema"

const (
	MethodSessionUpdate        = schema.MethodSessionUpdate
	MethodSessionReqPermission = schema.MethodSessionReqPermission
)

const (
	UpdateUserMessage  = schema.UpdateUserMessage
	UpdateAgentMessage = schema.UpdateAgentMessage
	UpdateAgentThought = schema.UpdateAgentThought
	UpdateToolCall     = schema.UpdateToolCall
	UpdateToolCallInfo = schema.UpdateToolCallInfo
	UpdatePlan         = schema.UpdatePlan
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
	ToolKindDelete  = schema.ToolKindDelete
	ToolKindMove    = schema.ToolKindMove
	ToolKindSearch  = schema.ToolKindSearch
	ToolKindExecute = schema.ToolKindExecute
	ToolKindThink   = schema.ToolKindThink
	ToolKindFetch   = schema.ToolKindFetch
	ToolKindSwitch  = schema.ToolKindSwitch
	ToolKindOther   = schema.ToolKindOther
)

const (
	PermAllowOnce    = schema.PermAllowOnce
	PermAllowAlways  = schema.PermAllowAlways
	PermRejectOnce   = schema.PermRejectOnce
	PermRejectAlways = schema.PermRejectAlways
)

type Update = schema.Update
type SessionNotification = schema.SessionNotification
type TextContent = schema.TextContent
type ToolCallLocation = schema.ToolCallLocation
type ToolCallContent = schema.ToolCallContent
type ContentChunk = schema.ContentChunk
type ToolCall = schema.ToolCall
type ToolCallUpdate = schema.ToolCallUpdate
type PlanEntry = schema.PlanEntry
type PlanUpdate = schema.PlanUpdate
type PermissionOption = schema.PermissionOption
type RequestPermissionRequest = schema.RequestPermissionRequest
type PermissionOutcome = schema.PermissionOutcome
type RequestPermissionResponse = schema.RequestPermissionResponse
