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
	if toolName != "BASH" && toolName != "SPAWN" && toolName != "TASK" {
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
			Meta:       streamFrameMeta("append"),
			Protocol: &sdksession.EventProtocol{
				Method:     sdksession.ProtocolMethodSessionUpdate,
				UpdateType: string(sdksession.ProtocolUpdateTypeToolUpdate),
				Update: &sdksession.ProtocolUpdate{
					SessionUpdate: string(sdksession.ProtocolUpdateTypeToolUpdate),
					ToolCallID:    req.CallID,
					Kind:          strings.TrimSpace(req.ToolName),
					Title:         req.ToolName,
					Status:        string(ToolStatusRunning),
					RawInput:      maps.Clone(req.RawInput),
					RawOutput:     output,
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

// StreamFrameEvents projects one runtime stream frame into all transient gateway
// events needed by renderers. Structured child session events are preserved for
// subagent panels, while the standard tool-result update remains available for
// the owning SPAWN/BASH panel.
func StreamFrameEvents(req StreamRequest, frame sdkstream.Frame) []EventEnvelope {
	out := make([]EventEnvelope, 0, 2)
	if frame.Event != nil {
		if env, ok := streamFrameEmbeddedEvent(req, frame); ok {
			out = append(out, env)
		}
	}
	if strings.EqualFold(strings.TrimSpace(req.ToolName), "SPAWN") {
		if env, ok := subagentStreamFrameEvent(req, frame); ok {
			out = append(out, env)
		}
		return out
	}
	if frame.Closed {
		out = append(out, streamFinalFrameEvent(req, frame))
		return out
	}
	if frame.Text != "" && shouldProjectFrameTextToParentTool(frame) {
		out = append(out, StreamFrameEvent(req, frame))
	}
	return out
}

func subagentStreamFrameEvent(req StreamRequest, frame sdkstream.Frame) (EventEnvelope, bool) {
	if frame.Closed {
		return subagentFinalFrameEvent(req, frame), true
	}
	if !frame.Running || frame.Text == "" || !shouldProjectFrameTextToParentTool(frame) {
		return EventEnvelope{}, false
	}
	if taskID := strings.TrimSpace(req.Ref.TaskID); taskID != "" {
		frame = sdkstream.CloneFrame(frame)
		frame.Ref.TaskID = taskID
	}
	return StreamFrameEvent(req, frame), true
}

func streamFinalFrameEvent(req StreamRequest, frame sdkstream.Frame) EventEnvelope {
	status, isErr := subagentFinalToolStatus(frame)
	output := map[string]any{
		"task_id":       firstNonEmpty(req.Ref.TaskID, frame.Ref.TaskID),
		"terminal_id":   firstNonEmpty(frame.Ref.TerminalID, req.Ref.TerminalID),
		"stream":        strings.TrimSpace(frame.Stream),
		"running":       false,
		"state":         string(status),
		"stdout_cursor": frame.Cursor.Stdout,
		"stderr_cursor": frame.Cursor.Stderr,
	}
	if state := strings.TrimSpace(frame.State); state != "" {
		output["state"] = state
	}
	if text := strings.TrimSpace(frame.Text); text != "" {
		output["text"] = text
	}
	for _, key := range []string{"target_kind", "handle", "mention", "agent", "prompt", "result", "final_message", "finalMessage", "output", "stdout", "stderr", "output_preview", "error"} {
		if value := strings.TrimSpace(stringValue(frame.Result[key])); value != "" {
			output[key] = value
		}
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
			Meta:       streamFrameMeta("final"),
			Protocol: &sdksession.EventProtocol{
				Method:     sdksession.ProtocolMethodSessionUpdate,
				UpdateType: string(sdksession.ProtocolUpdateTypeToolUpdate),
				Update: &sdksession.ProtocolUpdate{
					SessionUpdate: string(sdksession.ProtocolUpdateTypeToolUpdate),
					ToolCallID:    req.CallID,
					Kind:          strings.TrimSpace(req.ToolName),
					Title:         req.ToolName,
					Status:        string(status),
					RawInput:      maps.Clone(req.RawInput),
					RawOutput:     output,
				},
			},
			ToolResult: &ToolResultPayload{
				CallID:        req.CallID,
				ToolName:      req.ToolName,
				RawInput:      maps.Clone(req.RawInput),
				RawOutput:     output,
				Status:        status,
				Error:         isErr,
				Actor:         req.Actor,
				Scope:         req.Scope,
				ParticipantID: req.ParticipantID,
			},
		},
	}
}

func subagentFinalFrameEvent(req StreamRequest, frame sdkstream.Frame) EventEnvelope {
	status, isErr := subagentFinalToolStatus(frame)
	output := map[string]any{
		"task_id":     firstNonEmpty(req.Ref.TaskID, frame.Ref.TaskID),
		"terminal_id": firstNonEmpty(frame.Ref.TerminalID, req.Ref.TerminalID),
		"running":     false,
		"state":       string(status),
	}
	if state := strings.TrimSpace(frame.State); state != "" {
		output["state"] = state
	}
	for _, key := range []string{"handle", "mention", "agent", "prompt"} {
		if value := strings.TrimSpace(stringValue(frame.Result[key])); value != "" {
			output[key] = value
		}
	}
	if result := CleanSubagentFinalOutput(firstNonEmpty(
		stringValue(frame.Result["result"]),
		stringValue(frame.Result["final_message"]),
		stringValue(frame.Result["finalMessage"]),
		stringValue(frame.Result["output"]),
		stringValue(frame.Result["stdout"]),
		stringValue(frame.Result["output_preview"]),
		frame.Text,
	)); result != "" {
		output["result"] = result
	}
	if errText := strings.TrimSpace(stringValue(frame.Result["error"])); errText != "" {
		output["error"] = errText
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
			Meta:       streamFrameMeta("final"),
			Protocol: &sdksession.EventProtocol{
				Method:     sdksession.ProtocolMethodSessionUpdate,
				UpdateType: string(sdksession.ProtocolUpdateTypeToolUpdate),
				Update: &sdksession.ProtocolUpdate{
					SessionUpdate: string(sdksession.ProtocolUpdateTypeToolUpdate),
					ToolCallID:    req.CallID,
					Kind:          strings.TrimSpace(req.ToolName),
					Title:         req.ToolName,
					Status:        string(status),
					RawInput:      maps.Clone(req.RawInput),
					RawOutput:     output,
				},
			},
			ToolResult: &ToolResultPayload{
				CallID:        req.CallID,
				ToolName:      req.ToolName,
				RawInput:      maps.Clone(req.RawInput),
				RawOutput:     output,
				Status:        status,
				Error:         isErr,
				Actor:         req.Actor,
				Scope:         req.Scope,
				ParticipantID: req.ParticipantID,
			},
		},
	}
}

func subagentFinalToolStatus(frame sdkstream.Frame) (ToolStatus, bool) {
	state := strings.ToLower(strings.TrimSpace(firstNonEmpty(frame.State, stringValue(frame.Result["state"]))))
	switch state {
	case "failed":
		return ToolStatusFailed, true
	case "interrupted":
		return ToolStatusInterrupted, true
	case "cancelled", "canceled":
		return ToolStatusCancelled, true
	}
	if frame.ExitCode != nil && *frame.ExitCode != 0 {
		return ToolStatusFailed, true
	}
	return ToolStatusCompleted, false
}

func shouldProjectFrameTextToParentTool(frame sdkstream.Frame) bool {
	if strings.EqualFold(strings.TrimSpace(frame.Stream), "reasoning") {
		return false
	}
	if update := sdksession.ProtocolUpdateOf(frame.Event); update != nil &&
		strings.TrimSpace(update.SessionUpdate) == string(sdksession.ProtocolUpdateTypeAgentThought) {
		return false
	}
	if frame.Event != nil && frame.Event.Protocol != nil &&
		strings.TrimSpace(frame.Event.Protocol.UpdateType) == string(sdksession.ProtocolUpdateTypeAgentThought) {
		return false
	}
	return true
}

func streamFrameEmbeddedEvent(req StreamRequest, frame sdkstream.Frame) (EventEnvelope, bool) {
	event := sdksession.CloneEvent(frame.Event)
	if event == nil {
		return EventEnvelope{}, false
	}
	if event.Scope != nil && event.Scope.Participant.Kind == sdksession.ParticipantKindSubagent {
		taskID := firstNonEmpty(strings.TrimSpace(frame.Ref.TaskID), strings.TrimSpace(req.Ref.TaskID))
		if taskID != "" {
			if event.Scope.Participant.DelegationID == "" {
				event.Scope.Participant.DelegationID = taskID
			}
		}
	}
	if event.Time.IsZero() {
		event.Time = frame.UpdatedAt
	}
	env, ok := ProjectSessionEvent(req.SessionRef, event)
	if !ok {
		return EventEnvelope{}, false
	}
	if streamFrameEventIsParentToolEcho(req, env.Event) {
		return EventEnvelope{}, false
	}
	if taskID := firstNonEmpty(strings.TrimSpace(frame.Ref.TaskID), strings.TrimSpace(req.Ref.TaskID)); taskID != "" &&
		env.Event.Origin != nil && env.Event.Origin.Scope == EventScopeSubagent {
		env.Event.Origin.ScopeID = taskID
	}
	env.Event.HandleID = firstNonEmpty(strings.TrimSpace(env.Event.HandleID), strings.TrimSpace(req.HandleID))
	env.Event.RunID = firstNonEmpty(strings.TrimSpace(env.Event.RunID), strings.TrimSpace(req.RunID))
	env.Event.SessionRef = req.SessionRef
	env.Event.Meta = markStreamFrameTransient(env.Event.Meta)
	env.Event.Meta = markStreamFrameAnchor(env.Event.Meta, req.CallID, req.ToolName)
	if env.Event.OccurredAt.IsZero() {
		env.Event.OccurredAt = frame.UpdatedAt
	}
	return env, true
}

func streamFrameEventIsParentToolEcho(req StreamRequest, ev Event) bool {
	parentCallID := strings.TrimSpace(req.CallID)
	if parentCallID == "" {
		return false
	}
	callID := ""
	toolName := ""
	switch ev.Kind {
	case EventKindToolCall:
		if ev.ToolCall == nil {
			return false
		}
		callID = strings.TrimSpace(ev.ToolCall.CallID)
		toolName = strings.TrimSpace(ev.ToolCall.ToolName)
	case EventKindToolResult:
		if ev.ToolResult == nil {
			return false
		}
		callID = strings.TrimSpace(ev.ToolResult.CallID)
		toolName = strings.TrimSpace(ev.ToolResult.ToolName)
	default:
		return false
	}
	if callID == "" || callID != parentCallID {
		return false
	}
	parentTool := strings.TrimSpace(req.ToolName)
	return parentTool == "" || toolName == "" || strings.EqualFold(parentTool, toolName)
}

func markStreamFrameTransient(meta map[string]any) map[string]any {
	out := withCaelisRuntimeSection(meta, EventMetaRuntimeStream, map[string]any{
		EventMetaRuntimeStreamMode: "append",
	})
	caelis, _ := out[EventMetaRoot].(map[string]any)
	caelis[EventMetaTransient] = true
	return out
}

func streamFrameMeta(mode string) map[string]any {
	out := withCaelisRuntimeSection(nil, EventMetaRuntimeStream, map[string]any{
		EventMetaRuntimeStreamMode: strings.TrimSpace(mode),
	})
	caelis, _ := out[EventMetaRoot].(map[string]any)
	caelis[EventMetaTransient] = true
	return out
}

func markStreamFrameAnchor(meta map[string]any, callID string, toolName string) map[string]any {
	callID = strings.TrimSpace(callID)
	toolName = strings.TrimSpace(toolName)
	if callID == "" && toolName == "" {
		return meta
	}
	out := markStreamFrameTransient(meta)
	caelis, _ := out[EventMetaRoot].(map[string]any)
	runtimeMeta, _ := caelis[EventMetaRuntime].(map[string]any)
	streamMeta, _ := runtimeMeta[EventMetaRuntimeStream].(map[string]any)
	if callID != "" {
		streamMeta[EventMetaRuntimeStreamParentCallID] = callID
	}
	if toolName != "" {
		streamMeta[EventMetaRuntimeStreamParentTool] = toolName
	}
	runtimeMeta[EventMetaRuntimeStream] = streamMeta
	caelis[EventMetaRuntime] = runtimeMeta
	out[EventMetaRoot] = caelis
	return out
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

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}
