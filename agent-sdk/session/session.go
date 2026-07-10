package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/internal/jsonvalue"
)

var (
	// ErrSessionNotFound reports that one session ref cannot be resolved.
	ErrSessionNotFound = errorcode.New(errorcode.NotFound, "agent-sdk/session: session not found")

	// ErrAmbiguousSession reports that one session ref matches multiple
	// durable session documents and needs a narrower workspace key.
	ErrAmbiguousSession = errorcode.New(errorcode.FailedPrecondition, "agent-sdk/session: ambiguous session")

	// ErrInvalidSession reports that one session request is incomplete.
	ErrInvalidSession = errorcode.New(errorcode.InvalidArgument, "agent-sdk/session: invalid session")

	// ErrInvalidEvent reports that one event payload is incomplete.
	ErrInvalidEvent = errorcode.New(errorcode.InvalidArgument, "agent-sdk/session: invalid event")

	// ErrInvalidTransaction reports a compound mutation without a stable retry
	// identity.
	ErrInvalidTransaction = errorcode.New(errorcode.InvalidArgument, "agent-sdk/session: invalid transaction")

	// ErrInvalidValue reports a session value that cannot be represented by the
	// shared JSON-compatible durable value contract.
	ErrInvalidValue = errorcode.New(errorcode.InvalidArgument, "agent-sdk/session: invalid JSON-compatible value")

	// ErrRevisionConflict reports a failed expected-revision compare-and-swap.
	ErrRevisionConflict = errorcode.New(errorcode.Conflict, "agent-sdk/session: revision conflict")

	// ErrEventConflict reports reuse of a durable event ID with a different
	// canonical payload.
	ErrEventConflict = errorcode.New(errorcode.Conflict, "agent-sdk/session: event conflict")

	// ErrLeaseConflict reports that a live lease is owned elsewhere or that
	// lease identity/revision CAS failed.
	ErrLeaseConflict = errorcode.New(errorcode.Conflict, "agent-sdk/session: lease conflict")

	// ErrUnsupportedLegacyFormat reports an older on-disk session format that is
	// no longer a supported replay source.
	ErrUnsupportedLegacyFormat = errorcode.New(errorcode.Unsupported, "agent-sdk/session: unsupported legacy format")
)

// RevisionConflictError carries the expected and actual session revisions.
type RevisionConflictError struct {
	SessionID string
	Expected  uint64
	Actual    uint64
}

func (e *RevisionConflictError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: session %q expected %d, actual %d", ErrRevisionConflict, strings.TrimSpace(e.SessionID), e.Expected, e.Actual)
}

func (e *RevisionConflictError) Is(target error) bool { return target == ErrRevisionConflict }

func (e *RevisionConflictError) ErrorCode() errorcode.Code { return errorcode.Conflict }

// LeaseConflictError carries the session and stable reason for one failed
// store-level execution lease operation.
type LeaseConflictError struct {
	SessionID string
	Detail    string
}

func (e *LeaseConflictError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: session %q: %s", ErrLeaseConflict, strings.TrimSpace(e.SessionID), strings.TrimSpace(e.Detail))
}

func (e *LeaseConflictError) Is(target error) bool { return target == ErrLeaseConflict }

func (e *LeaseConflictError) ErrorCode() errorcode.Code { return errorcode.Conflict }

// CommittedError reports that a durable store committed a mutation even though
// post-commit apply or reporting returned an error. Callers must treat the
// mutation as durable: re-read state and retry with the same idempotency
// identity rather than inventing a new write or rolling back process state.
type CommittedError struct {
	Err error
}

func (e *CommittedError) Error() string {
	if e == nil || e.Err == nil {
		return "agent-sdk/session: mutation committed"
	}
	return "agent-sdk/session: mutation committed: " + e.Err.Error()
}

func (e *CommittedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *CommittedError) ErrorCode() errorcode.Code { return errorcode.UnknownOutcome }

// IsCommitted reports whether err is a post-commit reporting failure.
func IsCommitted(err error) bool {
	var committed *CommittedError
	return errors.As(err, &committed)
}

// EventConflictError reports a stable event ID reused for different content.
type EventConflictError struct {
	SessionID      string
	EventID        string
	IdempotencyKey string
}

func (e *EventConflictError) Error() string {
	if e == nil {
		return "<nil>"
	}
	identity := strings.TrimSpace(e.EventID)
	if identity == "" {
		identity = strings.TrimSpace(e.IdempotencyKey)
	}
	return fmt.Sprintf("%s: session %q identity %q has different content", ErrEventConflict, strings.TrimSpace(e.SessionID), identity)
}

func (e *EventConflictError) Is(target error) bool { return target == ErrEventConflict }

func (e *EventConflictError) ErrorCode() errorcode.Code { return errorcode.Conflict }

// CheckExpectedRevision applies the shared session compare-and-swap contract.
func CheckExpectedRevision(active Session, expected *uint64) error {
	if expected == nil || *expected == active.Revision {
		return nil
	}
	return &RevisionConflictError{SessionID: active.SessionID, Expected: *expected, Actual: active.Revision}
}

// JSONValueError reports which durable session value failed validation.
type JSONValueError struct {
	Scope string
	Err   error
}

func (e *JSONValueError) Error() string {
	if e == nil {
		return "<nil>"
	}
	scope := strings.TrimSpace(e.Scope)
	if scope == "" {
		scope = "value"
	}
	return fmt.Sprintf("%s: %s: %v", ErrInvalidValue, scope, e.Err)
}

func (e *JSONValueError) Unwrap() error {
	if e == nil || e.Err == nil {
		return ErrInvalidValue
	}
	return e.Err
}

func (e *JSONValueError) Is(target error) bool {
	return target == ErrInvalidValue
}

func (e *JSONValueError) ErrorCode() errorcode.Code { return errorcode.InvalidArgument }

// ValidateState validates one state object before a store makes it visible.
func ValidateState(state map[string]any) error {
	return validateJSONMap("state", state)
}

// ValidateMetadata validates one metadata object before durable storage.
func ValidateMetadata(metadata map[string]any) error {
	return validateJSONMap("metadata", metadata)
}

func validateJSONMap(scope string, value map[string]any) error {
	if err := jsonvalue.ValidateMap(value); err != nil {
		return &JSONValueError{Scope: scope, Err: err}
	}
	return nil
}

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
	EventTypeContext     EventType = "context"
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
	// VisibilityJournal marks durable execution-control facts that are excluded
	// from canonical model history and transcript replay.
	VisibilityJournal Visibility = "journal"
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
	ID             string          `json:"id,omitempty"`
	Kind           ParticipantKind `json:"kind,omitempty"`
	Role           ParticipantRole `json:"role,omitempty"`
	AgentName      string          `json:"agent_name,omitempty"`
	Label          string          `json:"label,omitempty"`
	SessionID      string          `json:"session_id,omitempty"`
	Source         string          `json:"source,omitempty"`
	ParentTurnID   string          `json:"parent_turn_id,omitempty"`
	DelegationID   string          `json:"delegation_id,omitempty"`
	ContextSyncSeq int             `json:"context_sync_seq,omitempty"`
	AttachedAt     time.Time       `json:"attached_at,omitempty"`
	ControllerRef  string          `json:"controller_ref,omitempty"`
}

// Session describes one session row.
type Session struct {
	SessionRef
	Revision     uint64               `json:"revision,omitempty"`
	CWD          string               `json:"cwd,omitempty"`
	Title        string               `json:"title,omitempty"`
	Metadata     map[string]any       `json:"metadata,omitempty"`
	Controller   ControllerBinding    `json:"controller,omitempty"`
	Participants []ParticipantBinding `json:"participants,omitempty"`
	CreatedAt    time.Time            `json:"created_at,omitempty"`
	UpdatedAt    time.Time            `json:"updated_at,omitempty"`
}

// SessionLease is a neutral cloud-store coordination record. It carries no
// worker-placement or scheduling policy; those decisions remain in Control.
type SessionLease struct {
	SessionRef   SessionRef `json:"session_ref"`
	LeaseID      string     `json:"lease_id,omitempty"`
	OwnerID      string     `json:"owner_id,omitempty"`
	Revision     uint64     `json:"revision,omitempty"`
	FencingToken uint64     `json:"fencing_token,omitempty"`
	AcquiredAt   time.Time  `json:"acquired_at,omitempty"`
	HeartbeatAt  time.Time  `json:"heartbeat_at,omitempty"`
	ExpiresAt    time.Time  `json:"expires_at,omitempty"`
}

// AcquireSessionLeaseRequest requests a store-level execution lease.
type AcquireSessionLeaseRequest struct {
	SessionRef SessionRef    `json:"session_ref"`
	OwnerID    string        `json:"owner_id,omitempty"`
	TTL        time.Duration `json:"ttl,omitempty"`
}

// HeartbeatSessionLeaseRequest renews one existing lease with lease CAS.
type HeartbeatSessionLeaseRequest struct {
	SessionRef            SessionRef    `json:"session_ref"`
	LeaseID               string        `json:"lease_id,omitempty"`
	OwnerID               string        `json:"owner_id,omitempty"`
	ExpectedLeaseRevision uint64        `json:"expected_lease_revision,omitempty"`
	TTL                   time.Duration `json:"ttl,omitempty"`
}

// ReleaseSessionLeaseRequest releases one existing lease with lease CAS.
type ReleaseSessionLeaseRequest struct {
	SessionRef            SessionRef `json:"session_ref"`
	LeaseID               string     `json:"lease_id,omitempty"`
	OwnerID               string     `json:"owner_id,omitempty"`
	ExpectedLeaseRevision uint64     `json:"expected_lease_revision,omitempty"`
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
	CWD       string         `json:"cwd,omitempty"`
	Title     string         `json:"title,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
	UpdatedAt time.Time      `json:"updated_at,omitempty"`
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
	SessionRef       SessionRef    `json:"session_ref"`
	ExpectedRevision *uint64       `json:"expected_revision,omitempty"`
	MutationGuard    MutationGuard `json:"mutation_guard,omitempty"`
	Event            *Event        `json:"event"`
}

// AppendEventsRequest appends multiple events to one session as one batch.
// Implementations must validate the full batch before making any event durable.
type AppendEventsRequest struct {
	SessionRef       SessionRef    `json:"session_ref"`
	ExpectedRevision *uint64       `json:"expected_revision,omitempty"`
	MutationGuard    MutationGuard `json:"mutation_guard,omitempty"`
	Events           []*Event      `json:"events"`
}

// AppendEventsAndUpdateStateRequest appends multiple events and derives the
// next session state in one store transaction. TransactionID identifies the
// complete event/state mutation across retries. UpdateState receives the
// normalized events that will be returned to the caller; an event-derived
// callback is not repeated when every input event deduplicates. A pure-state
// mutation uses no Events. MutationDigest identifies the callback semantics;
// it is combined with canonical event payloads and bound to TransactionID.
type AppendEventsAndUpdateStateRequest struct {
	SessionRef       SessionRef
	ExpectedRevision *uint64
	MutationGuard    MutationGuard
	TransactionID    string
	MutationDigest   string
	Events           []*Event
	UpdateState      func(storedEvents []*Event, state map[string]any) (map[string]any, error)
}

// EventsRequest lists events for one session.
type EventsRequest struct {
	SessionRef       SessionRef `json:"session_ref"`
	Limit            int        `json:"limit,omitempty"`
	IncludeTransient bool       `json:"include_transient,omitempty"`
}

// BindControllerRequest replaces the active controller binding for one session.
type BindControllerRequest struct {
	SessionRef    SessionRef        `json:"session_ref"`
	MutationGuard MutationGuard     `json:"mutation_guard,omitempty"`
	Binding       ControllerBinding `json:"binding"`
}

// BindControllerWithEventRequest atomically commits one controller ownership
// transition and its matching durable transfer event.
type BindControllerWithEventRequest struct {
	SessionRef       SessionRef        `json:"session_ref"`
	ExpectedRevision *uint64           `json:"expected_revision,omitempty"`
	MutationGuard    MutationGuard     `json:"mutation_guard,omitempty"`
	Binding          ControllerBinding `json:"binding"`
	Event            *Event            `json:"event"`
}

// PutParticipantRequest creates or updates one participant binding.
type PutParticipantRequest struct {
	SessionRef    SessionRef         `json:"session_ref"`
	MutationGuard MutationGuard      `json:"mutation_guard,omitempty"`
	Binding       ParticipantBinding `json:"binding"`
}

// RemoveParticipantRequest detaches one participant binding.
type RemoveParticipantRequest struct {
	SessionRef    SessionRef    `json:"session_ref"`
	MutationGuard MutationGuard `json:"mutation_guard,omitempty"`
	ParticipantID string        `json:"participant_id,omitempty"`
}

// PutParticipantWithEventRequest creates or updates one participant binding and
// appends the matching lifecycle event in one store transaction.
type PutParticipantWithEventRequest struct {
	SessionRef       SessionRef         `json:"session_ref"`
	ExpectedRevision *uint64            `json:"expected_revision,omitempty"`
	MutationGuard    MutationGuard      `json:"mutation_guard,omitempty"`
	Binding          ParticipantBinding `json:"binding"`
	Event            *Event             `json:"event"`
}

// RemoveParticipantWithEventRequest removes one participant binding and appends
// the matching lifecycle event in one store transaction.
type RemoveParticipantWithEventRequest struct {
	SessionRef       SessionRef    `json:"session_ref"`
	ExpectedRevision *uint64       `json:"expected_revision,omitempty"`
	MutationGuard    MutationGuard `json:"mutation_guard,omitempty"`
	ParticipantID    string        `json:"participant_id,omitempty"`
	Event            *Event        `json:"event"`
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

// EventInvocation records runtime-owned model invocation context for one event.
// Provider token usage remains in provider metadata; this context lets usage
// accounting group those tokens without overloading the provider Usage shape.
type EventInvocation struct {
	Provider            string `json:"provider,omitempty"`
	Model               string `json:"model,omitempty"`
	ContextWindowTokens int    `json:"context_window_tokens,omitempty"`
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

// Lifecycle starts, loads, and lists sessions without granting mutation of an
// already active session.
type Lifecycle interface {
	StartSession(context.Context, StartSessionRequest) (Session, error)
	LoadSession(context.Context, LoadSessionRequest) (LoadedSession, error)
	ListSessions(context.Context, ListSessionsRequest) (SessionList, error)
}

// Reader exposes immutable session and event snapshots.
type Reader interface {
	Session(context.Context, SessionRef) (Session, error)
	Events(context.Context, EventsRequest) ([]*Event, error)
}

// EventAppender appends canonical events with optional revision CAS.
type EventAppender interface {
	AppendEvent(context.Context, AppendEventRequest) (*Event, error)
}

// ControllerBindingStore mutates the durable active-controller binding.
type ControllerBindingStore interface {
	BindController(context.Context, BindControllerRequest) (Session, error)
}

// ParticipantBindingStore mutates durable participant bindings.
type ParticipantBindingStore interface {
	PutParticipant(context.Context, PutParticipantRequest) (Session, error)
	RemoveParticipant(context.Context, RemoveParticipantRequest) (Session, error)
}

// StateReader exposes recursively isolated durable state snapshots.
type StateReader interface {
	SnapshotState(context.Context, SessionRef) (map[string]any, error)
}

// StateWriter replaces or transactionally updates durable session state.
type StateWriter interface {
	ReplaceState(context.Context, SessionRef, map[string]any) error
	UpdateState(context.Context, SessionRef, func(map[string]any) (map[string]any, error)) error
}

// StateStore combines durable state reads and writes.
type StateStore interface {
	StateReader
	StateWriter
}

// Service is the full reference session service. Consumers should accept the
// narrow interfaces above unless they genuinely need the aggregate.
type Service interface {
	Lifecycle
	Reader
	EventAppender
	ControllerBindingStore
	ParticipantBindingStore
	StateStore
}

// ParticipantLifecycleService is implemented by stores that can atomically
// change participant bindings and append their replayable lifecycle events.
type ParticipantLifecycleService interface {
	PutParticipantWithEvent(context.Context, PutParticipantWithEventRequest) (Session, *Event, error)
	RemoveParticipantWithEvent(context.Context, RemoveParticipantWithEventRequest) (Session, *Event, error)
}

// ControllerHandoffService is implemented by stores that atomically commit a
// controller binding and its matching durable handoff event.
type ControllerHandoffService interface {
	BindControllerWithEvent(context.Context, BindControllerWithEventRequest) (Session, *Event, error)
}

// EventBatchService is implemented by stores that can validate and append a
// batch of events without exposing partially appended durable history.
type EventBatchService interface {
	AppendEvents(context.Context, AppendEventsRequest) ([]*Event, error)
}

// EventBatchStateService is implemented by stores that can append an event
// batch and update session state without exposing only one side of the commit.
type EventBatchStateService interface {
	AppendEventsAndUpdateState(context.Context, AppendEventsAndUpdateStateRequest) ([]*Event, error)
}

// SessionLeaseService coordinates exclusive session execution across Runtime
// instances. Control owns placement and heartbeat policy; stores own lease CAS.
type SessionLeaseService interface {
	AcquireSessionLease(context.Context, AcquireSessionLeaseRequest) (SessionLease, error)
	HeartbeatSessionLease(context.Context, HeartbeatSessionLeaseRequest) (SessionLease, error)
	ReleaseSessionLease(context.Context, ReleaseSessionLeaseRequest) error
}

// SessionLeaseReader reloads the current durable lease after an unknown
// reporting outcome. It does not acquire or renew ownership.
type SessionLeaseReader interface {
	SessionLease(context.Context, SessionRef) (SessionLease, error)
}
