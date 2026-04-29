package core

import (
	"maps"
	"strconv"
	"strings"
	"time"

	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
	sdkstream "github.com/OnslaughtSnail/caelis/sdk/stream"
)

// StreamRequest is one non-durable output subscription derived from a standard
// running tool update. It is adapter-facing: callers render frames live and
// persist only the final tool result already emitted by the runtime.
type StreamRequest struct {
	HandleID      string
	RunID         string
	TurnID        string
	SessionRef    sdksession.SessionRef
	CallID        string
	ToolName      string
	RawInput      map[string]any
	Ref           sdkstream.Ref
	Cursor        sdkstream.Cursor
	Origin        *EventOrigin
	Actor         string
	Scope         EventScope
	ParticipantID string
}

// Key returns one stable subscription identity for deduplicating live output
// streams across repeated running snapshots.
func (r StreamRequest) Key() string {
	return strings.Join([]string{
		strings.TrimSpace(r.SessionRef.SessionID),
		strings.TrimSpace(r.Ref.TaskID),
		strings.TrimSpace(r.Ref.TerminalID),
		strings.TrimSpace(r.CallID),
	}, "|")
}

// StreamRequestFromEvent extracts a stream subscription request from a Gateway
// event without relying on Caelis-only display metadata.
func StreamRequestFromEvent(env EventEnvelope) (StreamRequest, bool) {
	ev := env.Event
	payload := ev.ToolResult
	if ev.Kind != EventKindToolResult || payload == nil {
		return StreamRequest{}, false
	}
	toolName := strings.ToUpper(strings.TrimSpace(payload.ToolName))
	if toolName != "BASH" && toolName != "SPAWN" {
		return StreamRequest{}, false
	}
	if payload.Status != ToolStatusRunning && !boolValue(payload.RawOutput["running"]) && !strings.EqualFold(strings.TrimSpace(stringValue(payload.RawOutput["state"])), "running") {
		return StreamRequest{}, false
	}
	taskID := firstNonEmpty(stringValue(payload.RawOutput["task_id"]), stringValue(payload.RawInput["task_id"]))
	terminalID := firstNonEmpty(stringValue(payload.RawOutput["terminal_id"]), stringValue(payload.RawInput["terminal_id"]))
	if taskID == "" && terminalID == "" {
		return StreamRequest{}, false
	}
	req := StreamRequest{
		HandleID:   strings.TrimSpace(ev.HandleID),
		RunID:      strings.TrimSpace(ev.RunID),
		TurnID:     strings.TrimSpace(ev.TurnID),
		SessionRef: ev.SessionRef,
		CallID:     strings.TrimSpace(payload.CallID),
		ToolName:   strings.TrimSpace(payload.ToolName),
		RawInput:   maps.Clone(payload.RawInput),
		Ref: sdkstream.Ref{
			SessionID:  firstNonEmpty(strings.TrimSpace(ev.SessionRef.SessionID), stringValue(payload.RawOutput["session_id"]), stringValue(payload.RawInput["session_id"])),
			TaskID:     taskID,
			TerminalID: terminalID,
		},
		Cursor: sdkstream.Cursor{
			Stdout: int64FromAny(payload.RawOutput["stdout_cursor"]),
			Stderr: int64FromAny(payload.RawOutput["stderr_cursor"]),
		},
		Origin:        cloneEventOrigin(ev.Origin),
		Actor:         strings.TrimSpace(payload.Actor),
		Scope:         payload.Scope,
		ParticipantID: strings.TrimSpace(payload.ParticipantID),
	}
	if req.Scope == "" && req.Origin != nil {
		req.Scope = req.Origin.Scope
	}
	if req.Actor == "" && req.Origin != nil {
		req.Actor = req.Origin.Actor
	}
	if req.ParticipantID == "" && req.Origin != nil {
		req.ParticipantID = req.Origin.ParticipantID
	}
	if req.CallID == "" || req.ToolName == "" || strings.TrimSpace(req.Ref.SessionID) == "" {
		return StreamRequest{}, false
	}
	return req, true
}

// StreamFrameEvent projects one stream frame into the same ACP-native tool
// update shape used by normal Gateway output. The event is intentionally
// transient; adapters should not append it to durable session history.
func StreamFrameEvent(req StreamRequest, frame sdkstream.Frame) EventEnvelope {
	output := map[string]any{
		"task_id":       firstNonEmpty(frame.Ref.TaskID, req.Ref.TaskID),
		"terminal_id":   firstNonEmpty(frame.Ref.TerminalID, req.Ref.TerminalID),
		"stream":        strings.TrimSpace(frame.Stream),
		"text":          frame.Text,
		"running":       frame.Running,
		"state":         streamFrameState(frame),
		"stdout_cursor": frame.Cursor.Stdout,
		"stderr_cursor": frame.Cursor.Stderr,
	}
	if frame.ExitCode != nil {
		output["exit_code"] = *frame.ExitCode
	}
	occurredAt := frame.UpdatedAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	return EventEnvelope{
		Event: Event{
			Kind:       EventKindToolResult,
			HandleID:   req.HandleID,
			RunID:      req.RunID,
			TurnID:     req.TurnID,
			OccurredAt: occurredAt,
			SessionRef: req.SessionRef,
			Origin:     cloneEventOrigin(req.Origin),
			Meta: map[string]any{
				"caelis": map[string]any{
					"transient": true,
					"display": map[string]any{
						"terminal": map[string]any{
							"mode":          "append",
							"task_id":       output["task_id"],
							"terminal_id":   output["terminal_id"],
							"stream":        output["stream"],
							"text":          frame.Text,
							"running":       frame.Running,
							"stdout_cursor": frame.Cursor.Stdout,
							"stderr_cursor": frame.Cursor.Stderr,
						},
					},
				},
			},
			ToolResult: &ToolResultPayload{
				CallID:        req.CallID,
				ToolName:      req.ToolName,
				RawInput:      maps.Clone(req.RawInput),
				RawOutput:     output,
				Status:        ToolStatusRunning,
				Actor:         req.Actor,
				Scope:         req.Scope,
				ParticipantID: req.ParticipantID,
			},
		},
	}
}

func streamFrameState(frame sdkstream.Frame) string {
	if frame.Running {
		return "running"
	}
	if frame.Closed {
		return "completed"
	}
	return ""
}

func cloneEventOrigin(origin *EventOrigin) *EventOrigin {
	if origin == nil {
		return nil
	}
	cloned := *origin
	return &cloned
}

func int64FromAny(value any) int64 {
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int8:
		return int64(typed)
	case int16:
		return int64(typed)
	case int32:
		return int64(typed)
	case int64:
		return typed
	case uint:
		return int64(typed)
	case uint8:
		return int64(typed)
	case uint16:
		return int64(typed)
	case uint32:
		return int64(typed)
	case uint64:
		if typed > ^uint64(0)>>1 {
			return 0
		}
		return int64(typed)
	case float32:
		return int64(typed)
	case float64:
		return int64(typed)
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return err == nil && parsed
	default:
		return false
	}
}
