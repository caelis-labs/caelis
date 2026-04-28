package session

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	sdkmodel "github.com/OnslaughtSnail/caelis/sdk/model"
)

var (
	// ErrSessionNotFound reports that one session ref cannot be resolved.
	ErrSessionNotFound = errors.New("sdk/session: session not found")

	// ErrInvalidSession reports that one session request is incomplete.
	ErrInvalidSession = errors.New("sdk/session: invalid session")

	// ErrInvalidEvent reports that one event payload is incomplete.
	ErrInvalidEvent = errors.New("sdk/session: invalid event")
)

// EventType identifies one canonical session event kind.
type EventType string

const (
	EventTypeUser        EventType = "user"
	EventTypeAssistant   EventType = "assistant"
	EventTypePlan        EventType = "plan"
	EventTypeToolCall    EventType = "tool_call"
	EventTypeToolResult  EventType = "tool_result"
	EventTypeParticipant EventType = "participant"
	EventTypeHandoff     EventType = "handoff"
	EventTypeCompact     EventType = "compact"
	EventTypeNotice      EventType = "notice"
	EventTypeLifecycle   EventType = "lifecycle"
	EventTypeSystem      EventType = "system"
	EventTypeCustom      EventType = "custom"
)

// Visibility defines how one event participates in history and invocation
// context reconstruction.
type Visibility string

const (
	VisibilityCanonical Visibility = "canonical"
	VisibilityUIOnly    Visibility = "ui_only"
	VisibilityOverlay   Visibility = "overlay"
	VisibilityMirror    Visibility = "mirror"
)

// ProtocolUpdateType identifies one ACP-compatible protocol update family.
type ProtocolUpdateType string

const (
	ProtocolUpdateTypeUserMessage  ProtocolUpdateType = "user_message_chunk"
	ProtocolUpdateTypeAgentMessage ProtocolUpdateType = "agent_message_chunk"
	ProtocolUpdateTypeAgentThought ProtocolUpdateType = "agent_thought_chunk"
	ProtocolUpdateTypeToolCall     ProtocolUpdateType = "tool_call"
	ProtocolUpdateTypeToolUpdate   ProtocolUpdateType = "tool_call_update"
	ProtocolUpdateTypePlan         ProtocolUpdateType = "plan"
	ProtocolUpdateTypePermission   ProtocolUpdateType = "request_permission"
)

// ControllerKind identifies the main controller family of one session epoch.
type ControllerKind string

const (
	ControllerKindKernel ControllerKind = "kernel"
	ControllerKindACP    ControllerKind = "acp"
)

// ParticipantKind identifies one attached participant family.
type ParticipantKind string

const (
	ParticipantKindACP      ParticipantKind = "acp"
	ParticipantKindSubagent ParticipantKind = "subagent"
)

// ParticipantRole identifies the role of one attached participant.
type ParticipantRole string

const (
	ParticipantRoleSidecar   ParticipantRole = "sidecar"
	ParticipantRoleDelegated ParticipantRole = "delegated"
	ParticipantRoleObserver  ParticipantRole = "observer"
)

// ActorKind identifies the high-level actor family of one event.
type ActorKind string

const (
	ActorKindUser        ActorKind = "user"
	ActorKindController  ActorKind = "controller"
	ActorKindParticipant ActorKind = "participant"
	ActorKindTool        ActorKind = "tool"
	ActorKindSystem      ActorKind = "system"
)

// WorkspaceRef identifies one workspace boundary.
type WorkspaceRef struct {
	Key string `json:"key,omitempty"`
	CWD string `json:"cwd,omitempty"`
}

// SessionRef identifies one logical session.
type SessionRef struct {
	AppName      string `json:"app_name,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	SessionID    string `json:"session_id,omitempty"`
	WorkspaceKey string `json:"workspace_key,omitempty"`
}

// ControllerBinding is the durable active-controller binding for one session.
type ControllerBinding struct {
	Kind            ControllerKind `json:"kind,omitempty"`
	ControllerID    string         `json:"controller_id,omitempty"`
	AgentName       string         `json:"agent_name,omitempty"`
	Label           string         `json:"label,omitempty"`
	EpochID         string         `json:"epoch_id,omitempty"`
	RemoteSessionID string         `json:"remote_session_id,omitempty"`
	ContextSyncSeq  int            `json:"context_sync_seq,omitempty"`
	AttachedAt      time.Time      `json:"attached_at,omitempty"`
	Source          string         `json:"source,omitempty"`
}

// ParticipantBinding is the durable participant attachment for one session.
type ParticipantBinding struct {
	ID            string          `json:"id,omitempty"`
	Kind          ParticipantKind `json:"kind,omitempty"`
	Role          ParticipantRole `json:"role,omitempty"`
	AgentName     string          `json:"agent_name,omitempty"`
	Label         string          `json:"label,omitempty"`
	SessionID     string          `json:"session_id,omitempty"`
	Source        string          `json:"source,omitempty"`
	ParentTurnID  string          `json:"parent_turn_id,omitempty"`
	DelegationID  string          `json:"delegation_id,omitempty"`
	AttachedAt    time.Time       `json:"attached_at,omitempty"`
	ControllerRef string          `json:"controller_ref,omitempty"`
}

// Session describes one session row.
type Session struct {
	SessionRef
	CWD          string               `json:"cwd,omitempty"`
	Title        string               `json:"title,omitempty"`
	Metadata     map[string]any       `json:"metadata,omitempty"`
	Controller   ControllerBinding    `json:"controller,omitempty"`
	Participants []ParticipantBinding `json:"participants,omitempty"`
	CreatedAt    time.Time            `json:"created_at,omitempty"`
	UpdatedAt    time.Time            `json:"updated_at,omitempty"`
}

// LoadedSession is one loaded session plus canonical events and state.
type LoadedSession struct {
	Session Session        `json:"session"`
	Events  []*Event       `json:"events,omitempty"`
	State   map[string]any `json:"state,omitempty"`
}

// SessionSummary is one session listing row.
type SessionSummary struct {
	SessionRef
	CWD       string    `json:"cwd,omitempty"`
	Title     string    `json:"title,omitempty"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

// SessionList is one paged session listing result.
type SessionList struct {
	Sessions   []SessionSummary `json:"sessions,omitempty"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// StartSessionRequest creates or reuses one session skeleton.
type StartSessionRequest struct {
	AppName            string         `json:"app_name,omitempty"`
	UserID             string         `json:"user_id,omitempty"`
	Workspace          WorkspaceRef   `json:"workspace,omitempty"`
	PreferredSessionID string         `json:"preferred_session_id,omitempty"`
	Title              string         `json:"title,omitempty"`
	Metadata           map[string]any `json:"metadata,omitempty"`
}

// LoadSessionRequest loads one session and recent events.
type LoadSessionRequest struct {
	SessionRef       SessionRef `json:"session_ref"`
	Limit            int        `json:"limit,omitempty"`
	IncludeTransient bool       `json:"include_transient,omitempty"`
}

// AppendEventRequest appends one event to one session.
type AppendEventRequest struct {
	SessionRef SessionRef `json:"session_ref"`
	Event      *Event     `json:"event"`
}

// EventsRequest lists events for one session.
type EventsRequest struct {
	SessionRef       SessionRef `json:"session_ref"`
	Limit            int        `json:"limit,omitempty"`
	IncludeTransient bool       `json:"include_transient,omitempty"`
}

// BindControllerRequest replaces the active controller binding for one session.
type BindControllerRequest struct {
	SessionRef SessionRef        `json:"session_ref"`
	Binding    ControllerBinding `json:"binding"`
}

// PutParticipantRequest creates or updates one participant binding.
type PutParticipantRequest struct {
	SessionRef SessionRef         `json:"session_ref"`
	Binding    ParticipantBinding `json:"binding"`
}

// RemoveParticipantRequest detaches one participant binding.
type RemoveParticipantRequest struct {
	SessionRef    SessionRef `json:"session_ref"`
	ParticipantID string     `json:"participant_id,omitempty"`
}

// ListSessionsRequest lists sessions in one workspace or user namespace.
type ListSessionsRequest struct {
	AppName      string `json:"app_name,omitempty"`
	UserID       string `json:"user_id,omitempty"`
	WorkspaceKey string `json:"workspace_key,omitempty"`
	Cursor       string `json:"cursor,omitempty"`
	Limit        int    `json:"limit,omitempty"`
}

// ActorRef identifies the actor associated with one event.
type ActorRef struct {
	Kind ActorKind `json:"kind,omitempty"`
	ID   string    `json:"id,omitempty"`
	Role string    `json:"role,omitempty"`
	Name string    `json:"name,omitempty"`
}

// ControllerRef identifies the controller epoch associated with one event.
type ControllerRef struct {
	Kind    ControllerKind `json:"kind,omitempty"`
	ID      string         `json:"id,omitempty"`
	EpochID string         `json:"epoch_id,omitempty"`
}

// ParticipantRef identifies the participant associated with one event.
type ParticipantRef struct {
	ID           string          `json:"id,omitempty"`
	Kind         ParticipantKind `json:"kind,omitempty"`
	Role         ParticipantRole `json:"role,omitempty"`
	DelegationID string          `json:"delegation_id,omitempty"`
}

// ACPRef identifies ACP-specific origin details for one canonical event.
type ACPRef struct {
	SessionID string `json:"session_id,omitempty"`
	EventType string `json:"event_type,omitempty"`
}

// EventScope is the compact session/controller/participant origin view for one
// canonical event.
type EventScope struct {
	TurnID      string         `json:"turn_id,omitempty"`
	Source      string         `json:"source,omitempty"`
	Controller  ControllerRef  `json:"controller,omitempty"`
	Participant ParticipantRef `json:"participant,omitempty"`
	ACP         ACPRef         `json:"acp,omitempty"`
}

// Store is the low-level durable session persistence boundary.
type Store interface {
	GetOrCreate(context.Context, StartSessionRequest) (Session, error)
	Get(context.Context, SessionRef) (Session, error)
	List(context.Context, ListSessionsRequest) (SessionList, error)
	AppendEvent(context.Context, SessionRef, *Event) (*Event, error)
	Events(context.Context, EventsRequest) ([]*Event, error)
	BindController(context.Context, SessionRef, ControllerBinding) (Session, error)
	PutParticipant(context.Context, SessionRef, ParticipantBinding) (Session, error)
	RemoveParticipant(context.Context, SessionRef, string) (Session, error)
	SnapshotState(context.Context, SessionRef) (map[string]any, error)
	ReplaceState(context.Context, SessionRef, map[string]any) error
	UpdateState(context.Context, SessionRef, func(map[string]any) (map[string]any, error)) error
}

// Service is the stable session-lifecycle boundary consumed by future runtime
// and adapters.
type Service interface {
	StartSession(context.Context, StartSessionRequest) (Session, error)
	LoadSession(context.Context, LoadSessionRequest) (LoadedSession, error)
	Session(context.Context, SessionRef) (Session, error)
	AppendEvent(context.Context, AppendEventRequest) (*Event, error)
	Events(context.Context, EventsRequest) ([]*Event, error)
	ListSessions(context.Context, ListSessionsRequest) (SessionList, error)
	BindController(context.Context, BindControllerRequest) (Session, error)
	PutParticipant(context.Context, PutParticipantRequest) (Session, error)
	RemoveParticipant(context.Context, RemoveParticipantRequest) (Session, error)
	SnapshotState(context.Context, SessionRef) (map[string]any, error)
	ReplaceState(context.Context, SessionRef, map[string]any) error
	UpdateState(context.Context, SessionRef, func(map[string]any) (map[string]any, error)) error
}

// EventNotice is the structured notice payload for one transient notice event.
type EventNotice struct {
	Level string         `json:"level,omitempty"`
	Text  string         `json:"text,omitempty"`
	Kind  string         `json:"kind,omitempty"`
	Meta  map[string]any `json:"meta,omitempty"`
}

// EventLifecycle is the structured lifecycle payload for one runtime event.
type EventLifecycle struct {
	Status string         `json:"status,omitempty"`
	Reason string         `json:"reason,omitempty"`
	Meta   map[string]any `json:"meta,omitempty"`
}

// ProtocolToolCall is the ACP-compatible tool call or tool update view of one
// canonical event.
type ProtocolToolCall struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Kind      string         `json:"kind,omitempty"`
	Title     string         `json:"title,omitempty"`
	Status    string         `json:"status,omitempty"`
	RawInput  map[string]any `json:"raw_input,omitempty"`
	RawOutput map[string]any `json:"raw_output,omitempty"`
}

// ProtocolPlanEntry is one ACP-compatible plan row.
type ProtocolPlanEntry struct {
	Content  string `json:"content,omitempty"`
	Status   string `json:"status,omitempty"`
	Priority string `json:"priority,omitempty"`
}

// ProtocolPlan is the ACP-compatible plan replacement payload.
type ProtocolPlan struct {
	Entries []ProtocolPlanEntry `json:"entries,omitempty"`
}

// ProtocolApprovalOption is one standard ACP permission option.
type ProtocolApprovalOption struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name,omitempty"`
	Kind string `json:"kind,omitempty"`
}

// ProtocolApproval is the ACP-compatible permission request payload. This maps
// to session/request_permission rather than inventing a kernel-only approval
// grammar.
type ProtocolApproval struct {
	ToolCall ProtocolToolCall         `json:"tool_call,omitempty"`
	Options  []ProtocolApprovalOption `json:"options,omitempty"`
}

// ProtocolParticipant is the participant lifecycle payload for one event.
type ProtocolParticipant struct {
	Action string `json:"action,omitempty"`
}

// ProtocolHandoff is the controller-handoff lifecycle payload for one event.
type ProtocolHandoff struct {
	Phase string `json:"phase,omitempty"`
}

// EventProtocol is the ACP-compatible protocol payload carried by one event.
// It groups protocol-shaped extensions under one nested object so Event itself
// stays small and stable.
type EventProtocol struct {
	UpdateType  string               `json:"update_type,omitempty"`
	ToolCall    *ProtocolToolCall    `json:"tool_call,omitempty"`
	Plan        *ProtocolPlan        `json:"plan,omitempty"`
	Approval    *ProtocolApproval    `json:"approval,omitempty"`
	Participant *ProtocolParticipant `json:"participant,omitempty"`
	Handoff     *ProtocolHandoff     `json:"handoff,omitempty"`
}

// Event is the compact canonical event envelope. Durable semantics live in
// typed nested payloads and scope refs rather than top-level ad-hoc fields.
type Event struct {
	ID         string            `json:"id,omitempty"`
	SessionID  string            `json:"session_id,omitempty"`
	Type       EventType         `json:"type,omitempty"`
	Visibility Visibility        `json:"visibility,omitempty"`
	Time       time.Time         `json:"time,omitempty"`
	Actor      ActorRef          `json:"actor,omitempty"`
	Scope      *EventScope       `json:"scope,omitempty"`
	Message    *sdkmodel.Message `json:"message,omitempty"`
	Notice     *EventNotice      `json:"notice,omitempty"`
	Lifecycle  *EventLifecycle   `json:"lifecycle,omitempty"`
	Protocol   *EventProtocol    `json:"protocol,omitempty"`
	Text       string            `json:"text,omitempty"`
	Meta       map[string]any    `json:"meta,omitempty"`
}

// MessageNotice recognizes one system-message runtime notice.
func MessageNotice(msg sdkmodel.Message) (EventNotice, bool) {
	if msg.Role != sdkmodel.RoleSystem {
		return EventNotice{}, false
	}
	text := strings.TrimSpace(msg.TextContent())
	if text == "" {
		return EventNotice{}, false
	}
	lower := strings.ToLower(text)
	switch {
	case strings.HasPrefix(lower, "warn:"):
		return EventNotice{Level: "warn", Text: strings.TrimSpace(text[len("warn:"):])}, true
	case strings.HasPrefix(lower, "note:"):
		return EventNotice{Level: "note", Text: strings.TrimSpace(text[len("note:"):])}, true
	default:
		return EventNotice{}, false
	}
}

// NoticeOf returns the structured notice carried by one event, if any.
func NoticeOf(event *Event) (EventNotice, bool) {
	if event == nil {
		return EventNotice{}, false
	}
	if event.Notice != nil {
		out := *event.Notice
		out.Level = strings.TrimSpace(strings.ToLower(out.Level))
		out.Text = strings.TrimSpace(out.Text)
		out.Kind = strings.TrimSpace(out.Kind)
		out.Meta = maps.Clone(out.Meta)
		if out.Level != "" && out.Text != "" {
			return out, true
		}
	}
	if event.Meta != nil {
		level, _ := event.Meta["notice_level"].(string)
		text, _ := event.Meta["notice_text"].(string)
		kind, _ := event.Meta["kind"].(string)
		level = strings.TrimSpace(strings.ToLower(level))
		text = strings.TrimSpace(text)
		if level != "" && text != "" {
			return EventNotice{
				Level: level,
				Text:  text,
				Kind:  strings.TrimSpace(kind),
				Meta:  maps.Clone(event.Meta),
			}, true
		}
	}
	if event.Message != nil {
		return MessageNotice(*event.Message)
	}
	return EventNotice{}, false
}

// MarkUIOnly annotates one event as UI-only.
func MarkUIOnly(event *Event) *Event {
	if event == nil {
		return nil
	}
	event.Visibility = VisibilityUIOnly
	if event.Type == "" {
		event.Type = EventTypeOf(event)
	}
	return event
}

// MarkOverlay annotates one event as invocation-only overlay state.
func MarkOverlay(event *Event) *Event {
	if event == nil {
		return nil
	}
	event.Visibility = VisibilityOverlay
	if event.Type == "" {
		event.Type = EventTypeOf(event)
	}
	return event
}

// MarkMirror annotates one event as durable transcript-only state.
func MarkMirror(event *Event) *Event {
	if event == nil {
		return nil
	}
	event.Visibility = VisibilityMirror
	if event.Type == "" {
		event.Type = EventTypeOf(event)
	}
	return event
}

// MarkNotice annotates one event as one transient runtime notice.
func MarkNotice(event *Event, level string, text string) *Event {
	if event == nil {
		return nil
	}
	event.Notice = &EventNotice{
		Level: strings.TrimSpace(strings.ToLower(level)),
		Text:  strings.TrimSpace(text),
	}
	event.Visibility = VisibilityUIOnly
	if event.Type == "" {
		event.Type = EventTypeNotice
	}
	return event
}

// IsUIOnly reports whether one event is UI-only.
func IsUIOnly(event *Event) bool {
	return event != nil && event.Visibility == VisibilityUIOnly
}

// IsOverlay reports whether one event is invocation-only overlay state.
func IsOverlay(event *Event) bool {
	return event != nil && event.Visibility == VisibilityOverlay
}

// IsMirror reports whether one event is transcript-only durable state.
func IsMirror(event *Event) bool {
	return event != nil && event.Visibility == VisibilityMirror
}

// IsNotice reports whether one event carries one structured notice.
func IsNotice(event *Event) bool {
	_, ok := NoticeOf(event)
	return ok
}

// IsTransient reports whether one event is runtime-transient only.
func IsTransient(event *Event) bool {
	if event == nil {
		return true
	}
	return IsUIOnly(event) || IsOverlay(event) || IsNotice(event)
}

// IsCanonicalHistoryEvent reports whether one event belongs to durable history.
func IsCanonicalHistoryEvent(event *Event) bool {
	if event == nil {
		return false
	}
	if IsTransient(event) || IsMirror(event) {
		return false
	}
	return true
}

// IsInvocationVisibleEvent reports whether one event may participate in the
// current invocation context.
func IsInvocationVisibleEvent(event *Event) bool {
	if event == nil || IsUIOnly(event) || IsNotice(event) || IsMirror(event) {
		return false
	}
	return true
}

// IsMainInvocationVisibleEvent reports whether one event belongs to the main
// controller context. Participant and subagent transcripts remain durable
// session history, but they must not be replayed into the main agent prompt.
func IsMainInvocationVisibleEvent(event *Event) bool {
	if !IsInvocationVisibleEvent(event) {
		return false
	}
	if event.Scope == nil {
		return true
	}
	return strings.TrimSpace(event.Scope.Participant.ID) == ""
}

// EventTypeOf infers one event type from its content when not explicitly set.
func EventTypeOf(event *Event) EventType {
	if event == nil {
		return EventTypeCustom
	}
	if event.Type != "" {
		return event.Type
	}
	if event.Notice != nil || IsNotice(event) {
		return EventTypeNotice
	}
	if event.Lifecycle != nil {
		return EventTypeLifecycle
	}
	if event.Protocol != nil {
		switch {
		case event.Protocol.Plan != nil || strings.EqualFold(strings.TrimSpace(event.Protocol.UpdateType), "plan"):
			return EventTypePlan
		case event.Protocol.ToolCall != nil:
			return EventTypeToolCall
		case event.Protocol.Approval != nil:
			return EventTypeLifecycle
		case event.Protocol.Participant != nil:
			return EventTypeParticipant
		case event.Protocol.Handoff != nil:
			return EventTypeHandoff
		}
	}
	if event.Message == nil {
		return EventTypeCustom
	}
	switch event.Message.Role {
	case sdkmodel.RoleUser:
		return EventTypeUser
	case sdkmodel.RoleAssistant:
		if len(event.Message.ToolCalls()) > 0 {
			return EventTypeToolCall
		}
		return EventTypeAssistant
	case sdkmodel.RoleTool:
		return EventTypeToolResult
	case sdkmodel.RoleSystem:
		if IsNotice(event) {
			return EventTypeNotice
		}
		return EventTypeSystem
	default:
		return EventTypeCustom
	}
}

// NormalizeSessionRef returns one normalized session ref.
func NormalizeSessionRef(ref SessionRef) SessionRef {
	return SessionRef{
		AppName:      strings.TrimSpace(ref.AppName),
		UserID:       strings.TrimSpace(ref.UserID),
		SessionID:    strings.TrimSpace(ref.SessionID),
		WorkspaceKey: strings.TrimSpace(ref.WorkspaceKey),
	}
}

// CloneSession returns one deep copy of one session.
func CloneSession(in Session) Session {
	out := in
	out.SessionRef = NormalizeSessionRef(in.SessionRef)
	out.CWD = strings.TrimSpace(in.CWD)
	out.Title = strings.TrimSpace(in.Title)
	out.Metadata = maps.Clone(in.Metadata)
	out.Controller = CloneControllerBinding(in.Controller)
	out.Participants = CloneParticipantBindings(in.Participants)
	return out
}

// CloneEvent returns one deep copy of one event.
func CloneEvent(in *Event) *Event {
	if in == nil {
		return nil
	}
	out := *in
	out.Text = in.Text
	out.Meta = maps.Clone(in.Meta)
	out.Actor = CloneActorRef(in.Actor)
	if in.Scope != nil {
		scope := CloneEventScope(*in.Scope)
		out.Scope = &scope
	}
	if in.Notice != nil {
		notice := *in.Notice
		notice.Level = strings.TrimSpace(strings.ToLower(notice.Level))
		notice.Text = strings.TrimSpace(notice.Text)
		notice.Kind = strings.TrimSpace(notice.Kind)
		notice.Meta = maps.Clone(notice.Meta)
		out.Notice = &notice
	}
	if in.Lifecycle != nil {
		lifecycle := *in.Lifecycle
		lifecycle.Status = strings.TrimSpace(lifecycle.Status)
		lifecycle.Reason = strings.TrimSpace(lifecycle.Reason)
		lifecycle.Meta = maps.Clone(lifecycle.Meta)
		out.Lifecycle = &lifecycle
	}
	if in.Protocol != nil {
		protocol := CloneEventProtocol(*in.Protocol)
		out.Protocol = &protocol
	}
	if in.Message != nil {
		message := sdkmodel.CloneMessage(*in.Message)
		out.Message = &message
	}
	return &out
}

// CloneEvents returns one deep copy of one event list.
func CloneEvents(events []*Event) []*Event {
	out := make([]*Event, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		out = append(out, CloneEvent(event))
	}
	return out
}

// FilterEvents returns one filtered event slice for one history query.
func FilterEvents(events []*Event, limit int, includeTransient bool) []*Event {
	filtered := make([]*Event, 0, len(events))
	for _, event := range events {
		if event == nil {
			continue
		}
		if !includeTransient && !IsCanonicalHistoryEvent(event) {
			continue
		}
		filtered = append(filtered, CloneEvent(event))
	}
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[len(filtered)-limit:]
	}
	return filtered
}

// CloneState returns one shallow copy of one session state map.
func CloneState(state map[string]any) map[string]any {
	return maps.Clone(state)
}

// CloneControllerBinding returns one normalized controller binding copy.
func CloneControllerBinding(in ControllerBinding) ControllerBinding {
	return ControllerBinding{
		Kind:            in.Kind,
		ControllerID:    strings.TrimSpace(in.ControllerID),
		AgentName:       strings.TrimSpace(in.AgentName),
		Label:           strings.TrimSpace(in.Label),
		EpochID:         strings.TrimSpace(in.EpochID),
		RemoteSessionID: strings.TrimSpace(in.RemoteSessionID),
		ContextSyncSeq:  in.ContextSyncSeq,
		AttachedAt:      in.AttachedAt,
		Source:          strings.TrimSpace(in.Source),
	}
}

// CloneParticipantBinding returns one normalized participant binding copy.
func CloneParticipantBinding(in ParticipantBinding) ParticipantBinding {
	return ParticipantBinding{
		ID:            strings.TrimSpace(in.ID),
		Kind:          in.Kind,
		Role:          in.Role,
		AgentName:     strings.TrimSpace(in.AgentName),
		Label:         strings.TrimSpace(in.Label),
		SessionID:     strings.TrimSpace(in.SessionID),
		Source:        strings.TrimSpace(in.Source),
		ParentTurnID:  strings.TrimSpace(in.ParentTurnID),
		DelegationID:  strings.TrimSpace(in.DelegationID),
		AttachedAt:    in.AttachedAt,
		ControllerRef: strings.TrimSpace(in.ControllerRef),
	}
}

// CloneParticipantBindings returns one normalized participant binding list.
func CloneParticipantBindings(in []ParticipantBinding) []ParticipantBinding {
	if len(in) == 0 {
		return nil
	}
	out := make([]ParticipantBinding, 0, len(in))
	for _, item := range in {
		out = append(out, CloneParticipantBinding(item))
	}
	return out
}

// CloneActorRef returns one normalized actor ref copy.
func CloneActorRef(in ActorRef) ActorRef {
	return ActorRef{
		Kind: in.Kind,
		ID:   strings.TrimSpace(in.ID),
		Role: strings.TrimSpace(in.Role),
		Name: strings.TrimSpace(in.Name),
	}
}

// CloneEventScope returns one normalized event scope copy.
func CloneEventScope(in EventScope) EventScope {
	return EventScope{
		TurnID: strings.TrimSpace(in.TurnID),
		Source: strings.TrimSpace(in.Source),
		Controller: ControllerRef{
			Kind:    in.Controller.Kind,
			ID:      strings.TrimSpace(in.Controller.ID),
			EpochID: strings.TrimSpace(in.Controller.EpochID),
		},
		Participant: ParticipantRef{
			ID:           strings.TrimSpace(in.Participant.ID),
			Kind:         in.Participant.Kind,
			Role:         in.Participant.Role,
			DelegationID: strings.TrimSpace(in.Participant.DelegationID),
		},
		ACP: ACPRef{
			SessionID: strings.TrimSpace(in.ACP.SessionID),
			EventType: strings.TrimSpace(in.ACP.EventType),
		},
	}
}

// CloneEventProtocol returns one normalized event protocol payload copy.
func CloneEventProtocol(in EventProtocol) EventProtocol {
	out := EventProtocol{
		UpdateType: strings.TrimSpace(in.UpdateType),
	}
	if in.ToolCall != nil {
		call := *in.ToolCall
		call.ID = strings.TrimSpace(call.ID)
		call.Name = strings.TrimSpace(call.Name)
		call.Kind = strings.TrimSpace(call.Kind)
		call.Title = strings.TrimSpace(call.Title)
		call.Status = strings.TrimSpace(call.Status)
		call.RawInput = maps.Clone(call.RawInput)
		call.RawOutput = maps.Clone(call.RawOutput)
		out.ToolCall = &call
	}
	if in.Plan != nil {
		plan := ProtocolPlan{}
		if len(in.Plan.Entries) > 0 {
			plan.Entries = make([]ProtocolPlanEntry, 0, len(in.Plan.Entries))
			for _, item := range in.Plan.Entries {
				plan.Entries = append(plan.Entries, ProtocolPlanEntry{
					Content:  strings.TrimSpace(item.Content),
					Status:   strings.TrimSpace(item.Status),
					Priority: strings.TrimSpace(item.Priority),
				})
			}
		}
		out.Plan = &plan
	}
	if in.Approval != nil {
		approval := ProtocolApproval{
			ToolCall: cloneProtocolToolCall(in.Approval.ToolCall),
		}
		if len(in.Approval.Options) > 0 {
			approval.Options = make([]ProtocolApprovalOption, 0, len(in.Approval.Options))
			for _, item := range in.Approval.Options {
				approval.Options = append(approval.Options, ProtocolApprovalOption{
					ID:   strings.TrimSpace(item.ID),
					Name: strings.TrimSpace(item.Name),
					Kind: strings.TrimSpace(item.Kind),
				})
			}
		}
		out.Approval = &approval
	}
	if in.Participant != nil {
		participant := *in.Participant
		participant.Action = strings.TrimSpace(participant.Action)
		out.Participant = &participant
	}
	if in.Handoff != nil {
		handoff := *in.Handoff
		handoff.Phase = strings.TrimSpace(handoff.Phase)
		out.Handoff = &handoff
	}
	return out
}

func cloneProtocolToolCall(in ProtocolToolCall) ProtocolToolCall {
	call := in
	call.ID = strings.TrimSpace(call.ID)
	call.Name = strings.TrimSpace(call.Name)
	call.Kind = strings.TrimSpace(call.Kind)
	call.Title = strings.TrimSpace(call.Title)
	call.Status = strings.TrimSpace(call.Status)
	call.RawInput = maps.Clone(call.RawInput)
	call.RawOutput = maps.Clone(call.RawOutput)
	return call
}

// CloneSessionSummaries returns one copy of one session summary slice.
func CloneSessionSummaries(items []SessionSummary) []SessionSummary {
	if len(items) == 0 {
		return nil
	}
	out := slices.Clone(items)
	for i := range out {
		out[i].SessionRef = NormalizeSessionRef(out[i].SessionRef)
		out[i].CWD = strings.TrimSpace(out[i].CWD)
		out[i].Title = strings.TrimSpace(out[i].Title)
	}
	return out
}
