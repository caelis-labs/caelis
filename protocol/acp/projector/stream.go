package projector

import (
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/ports/displaypolicy"
	"github.com/OnslaughtSnail/caelis/ports/gateway"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/ports/stream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/metautil"
)

// StreamRequest is one non-durable output subscription derived from a standard
// running tool update. The kernel extracts this request from runtime state;
// projector owns the ACP envelope shape emitted for live clients.
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
	Origin        *gateway.EventOrigin
	Actor         string
	Scope         gateway.EventScope
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

// ProjectStreamFrame projects one runtime stream frame into transient
// ACP-native envelopes for live clients.
func ProjectStreamFrame(req StreamRequest, frame stream.Frame) []eventstream.Envelope {
	out := make([]eventstream.Envelope, 0, 2)
	embedded := streamFrameEmbeddedEvents(req, frame)
	if strings.EqualFold(strings.TrimSpace(req.ToolName), "SPAWN") {
		parent := subagentStreamFrameEvents(req, frame)
		if len(embedded) > 0 {
			if len(parent) > 0 {
				for i := range embedded {
					embedded[i].Meta = markStreamFrameMirroredToParentTool(embedded[i].Meta)
				}
			}
			out = append(out, embedded...)
		}
		out = append(out, parent...)
		return out
	}
	out = append(out, embedded...)
	if frame.Closed {
		out = append(out, streamFinalFrameEvent(req, frame))
		return out
	}
	if frame.Text != "" && shouldProjectFrameTextToParentTool(frame) {
		out = append(out, streamFrameEvent(req, frame))
	}
	return out
}

func subagentStreamFrameEvents(req StreamRequest, frame stream.Frame) []eventstream.Envelope {
	if frame.Closed {
		return []eventstream.Envelope{subagentFinalFrameEvent(req, frame)}
	}
	if !frame.Running || frame.Text == "" || !shouldProjectFrameTextToParentTool(frame) {
		return nil
	}
	if taskID := strings.TrimSpace(req.Ref.TaskID); taskID != "" {
		frame = stream.CloneFrame(frame)
		frame.Ref.TaskID = taskID
	}
	return []eventstream.Envelope{streamFrameEvent(req, frame)}
}

func streamFrameEvent(req StreamRequest, frame stream.Frame) eventstream.Envelope {
	terminalID := firstNonEmpty(frame.Ref.TerminalID, req.Ref.TerminalID)
	return streamToolUpdateEnvelope(req, frame, gateway.ToolStatusRunning, false, terminalACPContent(frame.Text, terminalID), streamFrameMeta("append"))
}

func streamFinalFrameEvent(req StreamRequest, frame stream.Frame) eventstream.Envelope {
	status, isErr := subagentFinalToolStatus(frame)
	terminalID := firstNonEmpty(frame.Ref.TerminalID, req.Ref.TerminalID)
	finalText := streamFinalTerminalText(frame.Text, frame.Cursor, status)
	return streamToolUpdateEnvelope(req, frame, status, isErr, terminalACPContent(finalText, terminalID), streamFrameMeta("final"))
}

func subagentFinalFrameEvent(req StreamRequest, frame stream.Frame) eventstream.Envelope {
	status, isErr := subagentFinalToolStatus(frame)
	finalMessage := displaypolicy.CleanSubagentFinalOutput(frame.Text)
	env := streamToolUpdateEnvelope(req, frame, status, isErr, textACPContent(finalMessage), streamFrameMeta("final"))
	update, _ := env.Update.(ToolCallUpdate)
	taskID := firstNonEmpty(req.Ref.TaskID, frame.Ref.TaskID)
	update.Meta = streamFrameToolMeta(update.Meta, req.RawInput, map[string]any{
		"task_id":     taskID,
		"terminal_id": firstNonEmpty(frame.Ref.TerminalID, req.Ref.TerminalID),
		"running":     false,
		"state":       string(status),
		"result":      finalMessage,
	}, "", taskID)
	env.Update = update
	return env
}

func streamToolUpdateEnvelope(req StreamRequest, frame stream.Frame, status gateway.ToolStatus, isErr bool, content []session.ProtocolToolCallContent, meta map[string]any) eventstream.Envelope {
	terminalID := firstNonEmpty(frame.Ref.TerminalID, req.Ref.TerminalID)
	metaOutput := map[string]any{
		"task_id":       firstNonEmpty(frame.Ref.TaskID, req.Ref.TaskID),
		"terminal_id":   terminalID,
		"running":       status == gateway.ToolStatusRunning,
		"state":         streamFrameState(frame),
		"output_cursor": frame.Cursor.Output,
	}
	if status != gateway.ToolStatusRunning {
		metaOutput["running"] = false
		metaOutput["state"] = string(status)
	}
	if state := strings.TrimSpace(frame.State); state != "" {
		metaOutput["state"] = state
	}
	occurredAt := frame.UpdatedAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	toolName := strings.TrimSpace(req.ToolName)
	update := ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(req.CallID),
		RawInput:      cloneAnyMap(req.RawInput),
		Content:       schemaToolCallContent(content),
		Meta:          streamFrameToolMeta(meta, req.RawInput, metaOutput, "", firstNonEmpty(frame.Ref.TaskID, req.Ref.TaskID)),
	}
	if toolName != "" {
		update.Title = stringPtr(toolName)
		update.Kind = stringPtr(toolName)
	}
	if statusText := strings.TrimSpace(string(status)); statusText != "" {
		update.Status = stringPtr(statusText)
	}
	scope := eventstream.Scope(req.Scope)
	if scope == "" {
		scope = eventstream.ScopeMain
	}
	return eventstream.Envelope{
		Kind:          eventstream.KindSessionUpdate,
		SessionID:     strings.TrimSpace(req.SessionRef.SessionID),
		HandleID:      strings.TrimSpace(req.HandleID),
		RunID:         strings.TrimSpace(req.RunID),
		TurnID:        strings.TrimSpace(req.TurnID),
		OccurredAt:    occurredAt,
		Scope:         scope,
		ScopeID:       streamRequestScopeID(req),
		Actor:         strings.TrimSpace(req.Actor),
		ParticipantID: strings.TrimSpace(req.ParticipantID),
		Update:        update,
		Meta:          streamFrameMetaForEnvelope(isErr),
	}
}

func streamRequestScopeID(req StreamRequest) string {
	if req.Origin != nil && strings.TrimSpace(req.Origin.ScopeID) != "" {
		return strings.TrimSpace(req.Origin.ScopeID)
	}
	return firstNonEmpty(strings.TrimSpace(req.SessionRef.SessionID), strings.TrimSpace(req.TurnID))
}

func streamFrameMetaForEnvelope(isErr bool) map[string]any {
	if !isErr {
		return nil
	}
	return metautil.WithCompactRuntimeSection(nil, gateway.EventMetaRuntimeTool, map[string]any{"error": true})
}

func schemaToolCallContent(in []session.ProtocolToolCallContent) []ToolCallContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]ToolCallContent, 0, len(in))
	for _, item := range in {
		out = append(out, ToolCallContent{
			Type:       strings.TrimSpace(item.Type),
			Content:    item.Content,
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
			OldText:    item.OldText,
			NewText:    item.NewText,
		})
	}
	return out
}

func subagentFinalToolStatus(frame stream.Frame) (gateway.ToolStatus, bool) {
	state := strings.ToLower(strings.TrimSpace(frame.State))
	switch state {
	case "failed":
		return gateway.ToolStatusFailed, true
	case "interrupted":
		return gateway.ToolStatusInterrupted, true
	case "cancelled", "canceled":
		return gateway.ToolStatusCancelled, true
	}
	return gateway.ToolStatusCompleted, false
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

func streamFinalTerminalText(text string, cursor stream.Cursor, status gateway.ToolStatus) string {
	if terminalStreamTextHasContent(text) {
		return text
	}
	if cursor.Output == 0 && (status == gateway.ToolStatusCompleted || status == gateway.ToolStatusFailed) {
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
		values[gateway.EventMetaRuntimeToolAction] = action
	}
	if taskID = firstNonEmpty(taskID, stringValue(output["handle"]), stringValue(output["task_id"]), stringValue(input["task_id"])); taskID != "" {
		values[gateway.EventMetaRuntimeTargetID] = taskID
	}
	for _, key := range []string{"agent", "handle", "mention", "prompt", "target_kind", "input"} {
		if value := firstNonEmpty(stringValue(output[key]), stringValue(input[key])); value != "" {
			values[key] = value
		}
	}
	if len(values) == 0 {
		return meta
	}
	return metautil.WithCompactRuntimeSection(meta, gateway.EventMetaRuntimeTool, values)
}

func shouldProjectFrameTextToParentTool(frame stream.Frame) bool {
	if frame.Event != nil && session.ProtocolSessionUpdateTypeOfProtocol(frame.Event.Protocol) == string(session.ProtocolUpdateTypeAgentThought) {
		return false
	}
	return true
}

func streamFrameEmbeddedEvents(req StreamRequest, frame stream.Frame) []eventstream.Envelope {
	event := session.CloneEvent(frame.Event)
	if event == nil {
		return nil
	}
	if event.Scope != nil && event.Scope.Participant.Kind == session.ParticipantKindSubagent {
		taskID := firstNonEmpty(strings.TrimSpace(frame.Ref.TaskID), strings.TrimSpace(req.Ref.TaskID))
		if taskID != "" && event.Scope.Participant.DelegationID == "" {
			event.Scope.Participant.DelegationID = taskID
		}
	}
	if event.Time.IsZero() {
		event.Time = frame.UpdatedAt
	}
	if streamFrameSessionEventIsParentToolEcho(req, event) {
		return nil
	}
	event.Meta = markStreamFrameTransient(event.Meta)
	event.Meta = markStreamFrameAnchor(event.Meta, req.CallID, req.ToolName)
	out := ProjectSessionEventEnvelope(EnvelopeBaseFromSessionEvent(req.SessionRef, event, SessionEventTransport{
		HandleID: req.HandleID,
		RunID:    req.RunID,
		TurnID:   req.TurnID,
	}), event)
	if taskID := firstNonEmpty(strings.TrimSpace(frame.Ref.TaskID), strings.TrimSpace(req.Ref.TaskID)); taskID != "" {
		for i := range out {
			if out[i].Scope == eventstream.ScopeSubagent {
				out[i].ScopeID = taskID
			}
		}
	}
	return out
}

func streamFrameSessionEventIsParentToolEcho(req StreamRequest, event *session.Event) bool {
	parentCallID := strings.TrimSpace(req.CallID)
	if parentCallID == "" || event == nil {
		return false
	}
	update := session.ProtocolUpdateOf(event)
	callID := ""
	toolName := ""
	if event.Tool != nil {
		callID = strings.TrimSpace(event.Tool.ID)
		toolName = strings.TrimSpace(event.Tool.Name)
	}
	if callID == "" && update != nil {
		callID = strings.TrimSpace(update.ToolCallID)
		toolName = session.CanonicalToolName(event, update)
	}
	if callID == "" || callID != parentCallID {
		return false
	}
	parentTool := strings.TrimSpace(req.ToolName)
	return parentTool == "" || toolName == "" || strings.EqualFold(parentTool, toolName)
}

func markStreamFrameTransient(meta map[string]any) map[string]any {
	out := metautil.WithCompactRuntimeSection(meta, gateway.EventMetaRuntimeStream, map[string]any{
		gateway.EventMetaRuntimeStreamMode: "append",
	})
	caelis, _ := out[gateway.EventMetaRoot].(map[string]any)
	caelis[gateway.EventMetaTransient] = true
	return out
}

func streamFrameMeta(mode string) map[string]any {
	out := metautil.WithCompactRuntimeSection(nil, gateway.EventMetaRuntimeStream, map[string]any{
		gateway.EventMetaRuntimeStreamMode: strings.TrimSpace(mode),
	})
	caelis, _ := out[gateway.EventMetaRoot].(map[string]any)
	caelis[gateway.EventMetaTransient] = true
	return out
}

func markStreamFrameAnchor(meta map[string]any, callID string, toolName string) map[string]any {
	callID = strings.TrimSpace(callID)
	toolName = strings.TrimSpace(toolName)
	if callID == "" && toolName == "" {
		return meta
	}
	out := markStreamFrameTransient(meta)
	caelis, _ := out[gateway.EventMetaRoot].(map[string]any)
	runtimeMeta, _ := caelis[gateway.EventMetaRuntime].(map[string]any)
	streamMeta, _ := runtimeMeta[gateway.EventMetaRuntimeStream].(map[string]any)
	if callID != "" {
		streamMeta[gateway.EventMetaRuntimeStreamParentCallID] = callID
	}
	if toolName != "" {
		streamMeta[gateway.EventMetaRuntimeStreamParentTool] = toolName
	}
	runtimeMeta[gateway.EventMetaRuntimeStream] = streamMeta
	caelis[gateway.EventMetaRuntime] = runtimeMeta
	out[gateway.EventMetaRoot] = caelis
	return out
}

func markStreamFrameMirroredToParentTool(meta map[string]any) map[string]any {
	return metautil.WithCompactRuntimeSection(meta, gateway.EventMetaRuntimeStream, map[string]any{
		gateway.EventMetaRuntimeStreamMirroredToParentTool: true,
	})
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

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}
