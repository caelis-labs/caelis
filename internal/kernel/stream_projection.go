package kernel

import (
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
)

// StreamRequest is one non-durable output subscription derived from a standard
// running tool update. It is adapter-facing: callers render frames live and
// persist only the final tool result already emitted by the runtime.
type StreamRequest struct {
	HandleID      string
	RunID         string
	TurnID        string
	SessionRef    session.SessionRef
	CallID        string
	ToolName      string
	RawInput      map[string]any
	Ref           stream.Ref
	Cursor        stream.Cursor
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
// event. ACP raw output stays model-visible; Caelis stream cursors live under
// the internal event metadata namespace.
func StreamRequestFromEvent(env EventEnvelope) (StreamRequest, bool) {
	ev := env.Event
	payload := ev.ToolResult
	if ev.Kind != EventKindToolResult || payload == nil {
		return StreamRequest{}, false
	}
	toolName := strings.ToUpper(strings.TrimSpace(payload.ToolName))
	if toolName != "RUN_COMMAND" && toolName != "SPAWN" {
		return StreamRequest{}, false
	}
	if payload.Status != ToolStatusRunning &&
		!boolValue(nestedAny(ev.Meta, "caelis", "runtime", "task", "running")) &&
		!strings.EqualFold(strings.TrimSpace(stringFromNestedMap(ev.Meta, "caelis", "runtime", "task", "state")), "running") {
		return StreamRequest{}, false
	}
	taskID := firstNonEmpty(
		stringFromNestedMap(ev.Meta, "caelis", "runtime", "task", "task_id"),
		stringFromNestedMap(ev.Meta, "caelis", "runtime", "task", "internal_task_id"),
	)
	terminalID := firstNonEmpty(
		terminalIDFromProtocolContent(gatewayToolContent(ev, payload.Content)),
		stringFromNestedMap(ev.Meta, "caelis", "runtime", "task", "terminal_id"),
	)
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
		Ref: stream.Ref{
			SessionID: firstNonEmpty(
				strings.TrimSpace(ev.SessionRef.SessionID),
				stringFromNestedMap(ev.Meta, "caelis", "runtime", "task", "session_id"),
			),
			TaskID:     taskID,
			TerminalID: terminalID,
		},
		Cursor: stream.Cursor{
			Output: int64FromAny(nestedAny(ev.Meta, "caelis", "runtime", "task", "output_cursor")),
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

func gatewayToolContent(ev Event, fallback []session.ProtocolToolCallContent) []session.ProtocolToolCallContent {
	if ev.Protocol != nil && ev.Protocol.Update != nil {
		if content := session.ProtocolToolCallContentOf(ev.Protocol.Update); len(content) > 0 {
			return content
		}
	}
	if ev.Protocol != nil && ev.Protocol.ToolCall != nil {
		if content := session.CloneProtocolToolCallContent(ev.Protocol.ToolCall.Content); len(content) > 0 {
			return content
		}
	}
	return session.CloneProtocolToolCallContent(fallback)
}

func terminalIDFromProtocolContent(content []session.ProtocolToolCallContent) string {
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if terminalID := strings.TrimSpace(item.TerminalID); terminalID != "" {
			return terminalID
		}
	}
	return ""
}

// StreamFrameEvent projects one stream frame into the same ACP-native tool
// update shape used by normal Gateway output. The event is intentionally
// transient; adapters should not append it to durable session history.
func StreamFrameEvent(req StreamRequest, frame stream.Frame) EventEnvelope {
	terminalID := firstNonEmpty(frame.Ref.TerminalID, req.Ref.TerminalID)
	metaOutput := map[string]any{
		"task_id":       firstNonEmpty(frame.Ref.TaskID, req.Ref.TaskID),
		"terminal_id":   terminalID,
		"running":       frame.Running,
		"state":         streamFrameState(frame),
		"output_cursor": frame.Cursor.Output,
	}
	content := terminalACPContent(frame.Text, terminalID)
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
			Meta:       streamFrameToolMeta(streamFrameMeta("append"), req.RawInput, metaOutput, "", firstNonEmpty(frame.Ref.TaskID, req.Ref.TaskID)),
			Protocol: &session.EventProtocol{
				Method:     session.ProtocolMethodSessionUpdate,
				UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
					ToolCallID:    req.CallID,
					Kind:          strings.TrimSpace(req.ToolName),
					Title:         req.ToolName,
					Status:        string(ToolStatusRunning),
					RawInput:      maps.Clone(req.RawInput),
					Content:       session.CloneProtocolToolCallContent(content),
				},
			},
			ToolResult: &ToolResultPayload{
				CallID:        req.CallID,
				ToolName:      req.ToolName,
				RawInput:      maps.Clone(req.RawInput),
				Content:       session.CloneProtocolToolCallContent(content),
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
// the owning SPAWN/RUN_COMMAND panel.
func StreamFrameEvents(req StreamRequest, frame stream.Frame) []EventEnvelope {
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

func subagentStreamFrameEvent(req StreamRequest, frame stream.Frame) (EventEnvelope, bool) {
	if frame.Closed {
		return subagentFinalFrameEvent(req, frame), true
	}
	// For SPAWN subagents we project non-thought frame text as tool result
	// updates so the subagent panel can render step-by-step progress.
	// Reasoning/thought frames are handled via the embedded event path
	// (streamFrameEmbeddedEvent) and should not also produce parent tool
	// result events, which would flood the SPAWN tool with redundant updates.
	if !frame.Running || frame.Text == "" {
		return EventEnvelope{}, false
	}
	if !shouldProjectFrameTextToParentTool(frame) {
		return EventEnvelope{}, false
	}
	if taskID := strings.TrimSpace(req.Ref.TaskID); taskID != "" {
		frame = stream.CloneFrame(frame)
		frame.Ref.TaskID = taskID
	}
	return StreamFrameEvent(req, frame), true
}

func streamFinalFrameEvent(req StreamRequest, frame stream.Frame) EventEnvelope {
	status, isErr := subagentFinalToolStatus(frame)
	terminalID := firstNonEmpty(frame.Ref.TerminalID, req.Ref.TerminalID)
	metaOutput := map[string]any{
		"task_id":       firstNonEmpty(req.Ref.TaskID, frame.Ref.TaskID),
		"terminal_id":   terminalID,
		"running":       false,
		"state":         string(status),
		"output_cursor": frame.Cursor.Output,
	}
	if state := strings.TrimSpace(frame.State); state != "" {
		metaOutput["state"] = state
	}
	finalText := streamFinalTerminalText(frame.Text, frame.Cursor, status)
	content := terminalACPContent(finalText, terminalID)
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
			Meta:       streamFrameToolMeta(streamFrameMeta("final"), req.RawInput, metaOutput, "", firstNonEmpty(req.Ref.TaskID, frame.Ref.TaskID)),
			Protocol: &session.EventProtocol{
				Method:     session.ProtocolMethodSessionUpdate,
				UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
					ToolCallID:    req.CallID,
					Kind:          strings.TrimSpace(req.ToolName),
					Title:         req.ToolName,
					Status:        string(status),
					RawInput:      maps.Clone(req.RawInput),
					Content:       session.CloneProtocolToolCallContent(content),
				},
			},
			ToolResult: &ToolResultPayload{
				CallID:        req.CallID,
				ToolName:      req.ToolName,
				RawInput:      maps.Clone(req.RawInput),
				Content:       session.CloneProtocolToolCallContent(content),
				Status:        status,
				Error:         isErr,
				Actor:         req.Actor,
				Scope:         req.Scope,
				ParticipantID: req.ParticipantID,
			},
		},
	}
}

func subagentFinalFrameEvent(req StreamRequest, frame stream.Frame) EventEnvelope {
	status, isErr := subagentFinalToolStatus(frame)
	taskID := firstNonEmpty(req.Ref.TaskID, frame.Ref.TaskID)
	metaOutput := map[string]any{
		"task_id":     taskID,
		"terminal_id": firstNonEmpty(frame.Ref.TerminalID, req.Ref.TerminalID),
		"running":     false,
		"state":       string(status),
	}
	if state := strings.TrimSpace(frame.State); state != "" {
		metaOutput["state"] = state
	}
	finalMessage := CleanSubagentFinalOutput(frame.Text)
	if finalMessage != "" {
		metaOutput["result"] = finalMessage
	}
	content := textACPContent(finalMessage)
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
			Meta:       streamFrameToolMeta(streamFrameMeta("final"), req.RawInput, metaOutput, "", taskID),
			Protocol: &session.EventProtocol{
				Method:     session.ProtocolMethodSessionUpdate,
				UpdateType: string(session.ProtocolUpdateTypeToolUpdate),
				Update: &session.ProtocolUpdate{
					SessionUpdate: string(session.ProtocolUpdateTypeToolUpdate),
					ToolCallID:    req.CallID,
					Kind:          strings.TrimSpace(req.ToolName),
					Title:         req.ToolName,
					Status:        string(status),
					RawInput:      maps.Clone(req.RawInput),
					Content:       session.CloneProtocolToolCallContent(content),
				},
			},
			ToolResult: &ToolResultPayload{
				CallID:        req.CallID,
				ToolName:      req.ToolName,
				RawInput:      maps.Clone(req.RawInput),
				Content:       session.CloneProtocolToolCallContent(content),
				Status:        status,
				Error:         isErr,
				Actor:         req.Actor,
				Scope:         req.Scope,
				ParticipantID: req.ParticipantID,
			},
		},
	}
}

func subagentFinalToolStatus(frame stream.Frame) (ToolStatus, bool) {
	state := strings.ToLower(strings.TrimSpace(frame.State))
	switch state {
	case "failed":
		return ToolStatusFailed, true
	case "interrupted":
		return ToolStatusInterrupted, true
	case "cancelled", "canceled":
		return ToolStatusCancelled, true
	}
	return ToolStatusCompleted, false
}

func terminalACPContent(text string, terminalID string) []session.ProtocolToolCallContent {
	if !terminalStreamTextHasContent(text) {
		return nil
	}
	item := session.ProtocolToolCallContent{
		Type:       "terminal",
		Content:    session.ProtocolTextContent(text),
		TerminalID: strings.TrimSpace(terminalID),
	}
	return []session.ProtocolToolCallContent{item}
}

func textACPContent(text string) []session.ProtocolToolCallContent {
	if text == "" {
		return nil
	}
	return []session.ProtocolToolCallContent{{
		Type:    "content",
		Content: session.ProtocolTextContent(text),
	}}
}

func streamFinalTerminalText(text string, cursor stream.Cursor, status ToolStatus) string {
	if terminalStreamTextHasContent(text) {
		return text
	}
	if cursor.Output == 0 && (status == ToolStatusCompleted || status == ToolStatusFailed) {
		return "(no output)"
	}
	return ""
}

func terminalStreamTextHasContent(text string) bool {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

func streamFrameToolMeta(meta map[string]any, input map[string]any, output map[string]any, action string, taskID string) map[string]any {
	values := map[string]any{}
	if action = firstNonEmpty(action, stringValue(output["action"]), stringValue(input["action"])); action != "" {
		values[EventMetaRuntimeToolAction] = action
	}
	if taskID = firstNonEmpty(taskID, stringValue(output["handle"]), stringValue(output["task_id"]), stringValue(input["task_id"])); taskID != "" {
		values[EventMetaRuntimeTargetID] = taskID
	}
	for _, key := range []string{"agent", "handle", "mention", "prompt", "target_kind", "input"} {
		if value := firstNonEmpty(stringValue(output[key]), stringValue(input[key])); value != "" {
			values[key] = value
		}
	}
	if len(values) == 0 {
		return meta
	}
	return withCaelisRuntimeSection(meta, EventMetaRuntimeTool, values)
}

func shouldProjectFrameTextToParentTool(frame stream.Frame) bool {
	if update := session.ProtocolUpdateOf(frame.Event); update != nil &&
		strings.TrimSpace(update.SessionUpdate) == string(session.ProtocolUpdateTypeAgentThought) {
		return false
	}
	if frame.Event != nil && frame.Event.Protocol != nil &&
		strings.TrimSpace(frame.Event.Protocol.UpdateType) == string(session.ProtocolUpdateTypeAgentThought) {
		return false
	}
	return true
}

func streamFrameEmbeddedEvent(req StreamRequest, frame stream.Frame) (EventEnvelope, bool) {
	event := session.CloneEvent(frame.Event)
	if event == nil {
		return EventEnvelope{}, false
	}
	if event.Scope != nil && event.Scope.Participant.Kind == session.ParticipantKindSubagent {
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

func streamFrameState(frame stream.Frame) string {
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

func firstNonZeroInt64(values ...int64) int64 {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
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
