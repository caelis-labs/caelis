package session

import "time"

// EventKind identifies the semantic kind of a session event.
type EventKind string

const (
	EventKindUser        EventKind = "user"
	EventKindAssistant   EventKind = "assistant"
	EventKindToolCall    EventKind = "tool_call"
	EventKindToolResult  EventKind = "tool_result"
	EventKindPlan        EventKind = "plan"
	EventKindCompaction  EventKind = "compaction"
	EventKindLifecycle   EventKind = "lifecycle"
	EventKindNotice      EventKind = "notice"
	EventKindSystem      EventKind = "system"
	EventKindHandoff     EventKind = "handoff"
	EventKindParticipant EventKind = "participant"
)

// Visibility controls where an event flows.
//
//	┌──────────┬────────────┬────────────┬───────────────┐
//	│ Persisted│ History    │ Model ctx  │ ACP/TUI       │
//	├──────────┼────────────┼────────────┼───────────────┤
//	│ canonical│ ✓          │ ✓          │ ✓             │
//	│ mirror   │ ✓          │ ✗          │ ✓ (replay)    │
//	│ overlay  │ ✗          │ ✓ (runtime)│ ✗             │
//	│ ui_only  │ ✗          │ ✗          │ ✓ (transient) │
//	└──────────┴────────────┴────────────┴───────────────┘
type Visibility string

const (
	VisibilityCanonical Visibility = "canonical"
	VisibilityMirror    Visibility = "mirror"
	VisibilityOverlay   Visibility = "overlay"
	VisibilityUIOnly    Visibility = "ui_only"
)

// IsPersisted reports whether the event should be written to durable storage.
func (v Visibility) IsPersisted() bool {
	return v == VisibilityCanonical || v == VisibilityMirror
}

// IsModelVisible reports whether the event should be included in model context.
func (v Visibility) IsModelVisible() bool {
	return v == VisibilityCanonical || v == VisibilityOverlay
}

// IsHistoryVisible reports whether the event should appear in replay history.
func (v Visibility) IsHistoryVisible() bool {
	return v == VisibilityCanonical || v == VisibilityMirror
}

// IsTransient reports whether the event is never persisted.
func (v Visibility) IsTransient() bool {
	return v == VisibilityOverlay || v == VisibilityUIOnly
}

// Event is a durable semantic record in a session timeline.
//
// Exactly one payload field should be populated, matching the Kind.
// The event stores semantic data only; ACP wire format, gateway envelope,
// and model messages are computed projections, never stored.
type Event struct {
	// Identity.
	ID         string     `json:"id"`
	SessionRef Ref        `json:"session_ref"`
	Kind       EventKind  `json:"kind"`
	Visibility Visibility `json:"visibility"`
	CreatedAt  time.Time  `json:"created_at"`

	// Scope and origin.
	Actor ActorRef `json:"actor,omitempty"`
	Scope *Scope   `json:"scope,omitempty"`

	// Turn/run correlation.
	TurnID string `json:"turn_id,omitempty"`
	RunID  string `json:"run_id,omitempty"`

	// Semantic payloads — exactly one should be populated per Kind.
	*UserPayload        `json:"user_payload,omitempty"`
	*AssistantPayload   `json:"assistant_payload,omitempty"`
	*ToolCallPayload    `json:"tool_call_payload,omitempty"`
	*ToolResultPayload  `json:"tool_result_payload,omitempty"`
	*PlanPayload        `json:"plan_payload,omitempty"`
	*SystemPayload      `json:"system_payload,omitempty"`
	*CompactionPayload  `json:"compaction_payload,omitempty"`
	*LifecyclePayload   `json:"lifecycle_payload,omitempty"`
	*NoticePayload      `json:"notice_payload,omitempty"`
	*HandoffPayload     `json:"handoff_payload,omitempty"`
	*ParticipantPayload `json:"participant_payload,omitempty"`

	// Provider replay metadata — preserves provider-specific data needed
	// for faithful model context reconstruction (thought signatures,
	// finish reasons, etc.). Stored only when non-empty.
	ProviderMeta map[string]any `json:"provider_meta,omitempty"`
}

// ActorRef identifies who produced the event.
type ActorRef struct {
	// Scope identifies the agent scope: "main", "participant", "subagent".
	Scope string `json:"scope,omitempty"`
	// ScopeID is the specific agent/participant ID within the scope.
	ScopeID string `json:"scope_id,omitempty"`
	// Source identifies the origin: "user", "model", "tool", "acp_agent",
	// "acp_subagent", "acp_participant", "acp_loopback".
	Source string `json:"source,omitempty"`
	// ParticipantID identifies the ACP participant (if any).
	ParticipantID string `json:"participant_id,omitempty"`
	// ParticipantKind classifies the participant: "subagent", "participant".
	ParticipantKind string `json:"participant_kind,omitempty"`
	// DelegationID links to the delegation/task that created this participant.
	DelegationID string `json:"delegation_id,omitempty"`
}

// Scope identifies the execution scope of an event.
type Scope struct {
	// Kind: "turn", "invocation", "subagent", "participant".
	Kind string `json:"kind"`
	// ID is the scope-specific identifier.
	ID string `json:"id"`
	// ParentID links to the parent scope (for nested subagents).
	ParentID string `json:"parent_id,omitempty"`
	// ControllerKind: "main", "sidecar", "delegated".
	ControllerKind string `json:"controller_kind,omitempty"`
	// ParticipantRole: "delegated", "sidecar", "controller".
	ParticipantRole string `json:"participant_role,omitempty"`
	// RemoteACPSessionID is the remote ACP session ID for this scope.
	RemoteACPSessionID string `json:"remote_acp_session_id,omitempty"`
}

// Context cursor state keys for orchestration.
const (
	StateKeyParentLastEvent    = "orchestrator.parent_last_event"
	StateKeyControllerEpoch    = "orchestrator.controller_epoch"
	StateKeyParticipantCursor  = "orchestrator.participant_cursor."  // + participant_id
	StateKeyACPRemoteSessionID = "orchestrator.acp_remote_session." // + delegation_id
	StateKeyACPRemoteCursor    = "orchestrator.acp_remote_cursor."  // + delegation_id
)
