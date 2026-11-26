package task

import (
	"context"
	"errors"

	"github.com/caelis-labs/caelis/agent-sdk/approval"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
)

type spawnNotStartedError struct {
	err error
}

func (e *spawnNotStartedError) Error() string {
	if e == nil || e.err == nil {
		return ""
	}
	return e.err.Error()
}

func (e *spawnNotStartedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// MarkSpawnNotStarted records positive SubagentRunner proof that no child was
// created and every producer that could publish activity or completion is
// quiescent. It does not replace the underlying error's code or human-readable
// message.
func MarkSpawnNotStarted(err error) error {
	if err == nil || SpawnProvenNotStarted(err) {
		return err
	}
	return &spawnNotStartedError{err: err}
}

// SpawnProvenNotStarted reports whether a SubagentRunner positively proved that
// a failed Spawn created no child, has no handle-owning effect, and cannot
// publish a later callback.
func SpawnProvenNotStarted(err error) bool {
	var marker *spawnNotStartedError
	return errors.As(err, &marker)
}

// SubagentRegistry exposes the spawnable ACP agents available to the runtime.
// App-layer assembly is responsible for registering and wiring the actual ACP
// endpoints or commands behind these descriptors.
type SubagentRegistry interface {
	Get(context.Context, string) (DelegationAgent, error)
	List(context.Context) ([]DelegationAgent, error)
}

// SpawnContext is the system-controlled parent session context inherited by one
// child endpoint. None of these fields are model-controlled tool arguments.
// ApprovalMode is the parent session mode for runners that derive child launch
// configuration from the spawn request; preassembled ACP runners may already
// carry the effective child approval mode in their launch args.
type SpawnContext struct {
	// ContentParts preserves the user request's structured text and attachments.
	ContentParts      []model.ContentPart        `json:"content_parts,omitempty"`
	SessionRef        session.SessionRef         `json:"session_ref,omitempty"`
	Session           session.Session            `json:"session,omitempty"`
	CWD               string                     `json:"cwd,omitempty"`
	TaskID            string                     `json:"task_id,omitempty"`
	ActivityID        string                     `json:"activity_id,omitempty"`
	Handle            string                     `json:"handle,omitempty"`
	Role              session.ParticipantRole    `json:"role,omitempty"`
	ParentCallID      string                     `json:"parent_call_id,omitempty"`
	Mode              string                     `json:"mode,omitempty"`
	ApprovalMode      string                     `json:"approval_mode,omitempty"`
	ApprovalRequester approval.SubagentRequester `json:"-"`
	Output            OutputObserver             `json:"-"`
	// Completion is the Runtime-owned terminal lifecycle path for this child
	// turn. Stream consumers and Task control calls remain observers.
	Completion CompletionSink `json:"-"`
}

// SubagentRunner drives one spawned ACP child instance. The child itself is
// expected to run in its own session and persist its own transcript
// independently. Spawn errors do not imply whether creation occurred.
// Implementations may use MarkSpawnNotStarted only when they can positively
// prove that no child was created and no producer can publish a later callback;
// every other error is an indeterminate creation outcome.
type SubagentRunner interface {
	Spawn(context.Context, SpawnContext, DelegationRequest) (Anchor, DelegationResult, error)
	Wait(context.Context, Anchor, int) (DelegationResult, error)
	Cancel(context.Context, Anchor) error
}

// ReconnectRequest lets an endpoint runner reattach the exact durable child
// Session and Task before Agent communication or history access. It is Runtime
// recovery context, not model-facing input.
type ReconnectRequest struct {
	Spawn  SpawnContext `json:"-"`
	Target Target       `json:"-"`
}

// CloneReconnectRequest copies recovery values while preserving injected
// callback interfaces owned by the current Runtime activation.
func CloneReconnectRequest(in *ReconnectRequest) *ReconnectRequest {
	if in == nil {
		return nil
	}
	out := *in
	out.Spawn.SessionRef = session.NormalizeSessionRef(in.Spawn.SessionRef)
	out.Spawn.Session = session.CloneSession(in.Spawn.Session)
	out.Target = CloneTargetRequest(TargetRequest{Target: in.Target}).Target
	return &out
}

// HistoryRequest identifies one durable child Session whose presentation
// history may be loaded without resuming execution. Anchor and Reconnect are
// trusted Task facts reconstructed by Control, never model input.
type HistoryRequest struct {
	// MetadataOnly omits returned history events when an output observer consumes
	// display replay. The observer may retain a bounded projection of that replay.
	MetadataOnly bool             `json:"-"`
	Anchor       Anchor           `json:"-"`
	Reconnect    ReconnectRequest `json:"-"`
}

// CloneHistoryRequest copies a read-only child Session recovery request.
func CloneHistoryRequest(in HistoryRequest) HistoryRequest {
	reconnect := CloneReconnectRequest(&in.Reconnect)
	out := HistoryRequest{Anchor: CloneAnchor(in.Anchor), MetadataOnly: in.MetadataOnly}
	if reconnect != nil {
		out.Reconnect = *reconnect
	}
	return out
}

// HistoryRunner is the optional read-only extension used when Control lazily
// opens a terminal child workspace whose transcript is provider-owned. The
// implementation must load the existing Session and must not resume execution
// or derive history from the parent Task result. When Spawn.Output implements
// HistoryObserver, replay completion must be observed before subsequent live
// output on that binding. A loaded connection may be retained for later
// authorized input; loading alone must not apply execution configuration.
type HistoryRunner interface {
	LoadHistory(context.Context, HistoryRequest) (session.LoadedSession, error)
}

// PlacementRunner is the optional typed Spawn extension used when Control has
// resolved a model-backed placement that is not an assembled Agent identity.
type PlacementRunner interface {
	SpawnTarget(context.Context, SpawnContext, TargetRequest) (Anchor, DelegationResult, error)
}
