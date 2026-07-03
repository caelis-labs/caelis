package eventstream

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

const (
	RunStateIdle            = "idle"
	RunStateRunning         = LifecycleStateRunning
	RunStateWaitingApproval = "waiting_approval"
	RunStateCompleted       = LifecycleStateCompleted
	RunStateFailed          = LifecycleStateFailed
	RunStateInterrupted     = LifecycleStateInterrupted
	RunStateCancelled       = LifecycleStateCancelled
)

// ReplayRequest is the stable client replay request used by GUI, SSE, and
// WebSocket adapters. Cursor accepts Envelope.ProjectionID or Envelope.Cursor.
type ReplayRequest struct {
	SessionID        string `json:"session_id,omitempty"`
	Cursor           string `json:"cursor,omitempty"`
	Limit            int    `json:"limit,omitempty"`
	IncludeTransient bool   `json:"include_transient,omitempty"`
}

// ReplayResult is the stable client replay response. Events are serialized
// directly for WebSocket and as SSE data with Envelope.Cursor as the event id.
type ReplayResult struct {
	SessionID     string     `json:"session_id,omitempty"`
	Events        []Envelope `json:"events,omitempty"`
	NextCursor    string     `json:"next_cursor,omitempty"`
	Durable       bool       `json:"durable,omitempty"`
	HasLiveHandle bool       `json:"has_live_handle,omitempty"`
	RunState      RunState   `json:"run_state,omitempty"`
}

// RunState is the stable client projection of the current session run.
type RunState struct {
	SessionID       string    `json:"session_id,omitempty"`
	HandleID        string    `json:"handle_id,omitempty"`
	RunID           string    `json:"run_id,omitempty"`
	TurnID          string    `json:"turn_id,omitempty"`
	ActiveTurnKind  string    `json:"active_turn_kind,omitempty"`
	Status          string    `json:"status,omitempty"`
	HasActiveTurn   bool      `json:"has_active_turn,omitempty"`
	WaitingApproval bool      `json:"waiting_approval,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	StartedAt       time.Time `json:"started_at,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

// NormalizeRunStatus applies the client protocol status rules shared by direct
// control-plane projections and stream-derived run-state hints.
func NormalizeRunStatus(status string, waitingApproval bool, hasActiveTurn bool) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(status))
	if waitingApproval || normalized == RunStateWaitingApproval {
		return RunStateWaitingApproval, true
	}
	if normalized == "" {
		if hasActiveTurn {
			return RunStateRunning, false
		}
		return RunStateIdle, false
	}
	if normalized == RunStateIdle {
		return RunStateIdle, false
	}
	return normalized, false
}

// ReplayCursor returns the durable resume cursor clients should use when
// switching from a live stream to Replay. It prefers ProjectionID over Cursor.
func ReplayCursor(env Envelope) string {
	return firstNonEmpty(env.ProjectionID, env.Cursor)
}

// SSEEventID returns the event id for SSE transports.
func SSEEventID(env Envelope) string {
	return strings.TrimSpace(env.Cursor)
}

// ToolCallFromEnvelope returns the original ACP tool_call payload when env
// carries one.
func ToolCallFromEnvelope(env Envelope) (schema.ToolCall, bool) {
	if env.Kind != KindSessionUpdate {
		return schema.ToolCall{}, false
	}
	update, ok := CloneUpdate(env.Update).(schema.ToolCall)
	return update, ok
}

// ToolCallUpdateFromEnvelope returns the original ACP tool_call_update payload
// when env carries one.
func ToolCallUpdateFromEnvelope(env Envelope) (schema.ToolCallUpdate, bool) {
	if env.Kind != KindSessionUpdate {
		return schema.ToolCallUpdate{}, false
	}
	update, ok := CloneUpdate(env.Update).(schema.ToolCallUpdate)
	return update, ok
}

// PermissionRequestFromEnvelope returns the original ACP request_permission
// payload when env carries one.
func PermissionRequestFromEnvelope(env Envelope) (schema.RequestPermissionRequest, bool) {
	if env.Kind != KindRequestPermission || env.Permission == nil {
		return schema.RequestPermissionRequest{}, false
	}
	cloned := CloneEnvelope(env)
	if cloned.Permission == nil {
		return schema.RequestPermissionRequest{}, false
	}
	return *cloned.Permission, true
}

// RunStateFromEnvelope returns a stable run-state projection from lifecycle,
// permission, and error envelopes.
func RunStateFromEnvelope(env Envelope) (RunState, bool) {
	base := RunState{
		SessionID: strings.TrimSpace(env.SessionID),
		HandleID:  strings.TrimSpace(env.HandleID),
		RunID:     strings.TrimSpace(env.RunID),
		TurnID:    strings.TrimSpace(env.TurnID),
		UpdatedAt: env.OccurredAt,
	}
	switch {
	case env.Kind == KindLifecycle && env.Lifecycle != nil:
		hasActiveTurn := !IsTerminalLifecycle(env)
		base.Status, base.WaitingApproval = NormalizeRunStatus(env.Lifecycle.State, false, hasActiveTurn)
		base.LastError = strings.TrimSpace(env.Lifecycle.Reason)
		base.HasActiveTurn = hasActiveTurn && base.Status != RunStateIdle
		return base, true
	case env.Kind == KindRequestPermission && env.Permission != nil:
		base.HasActiveTurn = true
		base.Status, base.WaitingApproval = NormalizeRunStatus("", true, base.HasActiveTurn)
		return base, true
	case env.Kind == KindError:
		base.Status, base.WaitingApproval = NormalizeRunStatus(RunStateFailed, false, false)
		base.LastError = strings.TrimSpace(firstNonEmpty(env.Error, errorString(env.Err)))
		return base, true
	default:
		return RunState{}, false
	}
}

// RunStateFromEnvelopes returns the latest run-state projection carried by
// events.
func RunStateFromEnvelopes(events []Envelope) (RunState, bool) {
	var out RunState
	ok := false
	for _, env := range events {
		next, isRunState := RunStateFromEnvelope(env)
		if !isRunState {
			continue
		}
		out = next
		ok = true
	}
	return out, ok
}
