package projector

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// StreamRequest is one non-durable output subscription derived from a standard
// running tool update. The kernel extracts this request from runtime state;
// projector owns the ACP envelope shape emitted for live clients.
type StreamRequest struct {
	HandleID          string
	RunID             string
	TurnID            string
	SessionRef        session.SessionRef
	CallID            string
	ToolName          string
	RawInput          map[string]any
	Ref               stream.Ref
	DisplayTerminalID string
	Cursor            stream.Cursor
	Origin            *StreamOrigin
	Actor             string
	Scope             eventstream.Scope
	ParticipantID     string
}

// StreamOrigin carries the protocol-level source scope for live stream frames.
type StreamOrigin struct {
	Scope                eventstream.Scope
	ScopeID              string
	Source               string
	Actor                string
	ParticipantID        string
	ParticipantKind      string
	ParticipantSessionID string
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
	parent := parentStreamFrameEvents(req, frame)
	embedded := streamFrameEmbeddedEvents(req, frame, parentStreamFrameHasToolMirror(parent))
	out := make([]eventstream.Envelope, 0, len(embedded)+len(parent))
	out = append(out, embedded...)
	out = append(out, parent...)
	return out
}

func parentStreamFrameEvents(req StreamRequest, frame stream.Frame) []eventstream.Envelope {
	if canonical, ok := names.Resolve(req.ToolName); ok && canonical == names.Spawn {
		return subagentStreamFrameEvents(req, frame)
	}
	if frame.Closed {
		return []eventstream.Envelope{streamFinalFrameEvent(req, frame)}
	}
	if frame.Text != "" && shouldProjectFrameTextToParentTool(frame) {
		return []eventstream.Envelope{streamFrameEvent(req, frame)}
	}
	return nil
}

func parentStreamFrameHasToolMirror(parent []eventstream.Envelope) bool {
	for _, env := range parent {
		if env.Delivery != nil && env.Delivery.IsParentToolMirror {
			return true
		}
	}
	return false
}

func subagentStreamFrameEvents(req StreamRequest, frame stream.Frame) []eventstream.Envelope {
	if frame.Closed {
		return []eventstream.Envelope{subagentFinalFrameEvent(req, frame)}
	}
	if !frame.Running || frame.Text == "" {
		return nil
	}
	if taskID := strings.TrimSpace(req.Ref.TaskID); taskID != "" {
		frame = stream.CloneFrame(frame)
		frame.Ref.TaskID = taskID
		if terminalID := strings.TrimSpace(req.Ref.TerminalID); terminalID != "" {
			frame.Ref.TerminalID = terminalID
		}
	}
	return []eventstream.Envelope{streamFrameEvent(req, frame)}
}

func streamFrameEvent(req StreamRequest, frame stream.Frame) eventstream.Envelope {
	return streamToolUpdateEnvelope(req, frame, toolStatusRunning, true, false, frame.Text, streamFrameMeta("append"))
}

func streamFinalFrameEvent(req StreamRequest, frame stream.Frame) eventstream.Envelope {
	status, isErr := subagentFinalToolStatus(frame)
	finalText := ""
	if frame.Cursor.Output == 0 {
		finalText = streamFinalTerminalText(frame.Text, frame.Cursor, status)
	}
	return streamToolUpdateEnvelope(req, frame, status, true, isErr, finalText, streamFrameMeta("final"))
}

func subagentFinalFrameEvent(req StreamRequest, frame stream.Frame) eventstream.Envelope {
	status, isErr := subagentFinalToolStatus(frame)
	finalMessage := display.CleanSubagentFinalOutput(frame.Text)
	terminalText := ""
	if frame.Cursor.Output == 0 {
		terminalText = finalMessage
	}
	if terminalID := strings.TrimSpace(req.Ref.TerminalID); terminalID != "" {
		frame = stream.CloneFrame(frame)
		frame.Ref.TerminalID = terminalID
	}
	env := streamToolUpdateEnvelope(req, frame, status, true, isErr, terminalText, streamFrameMeta("final"))
	update, _ := env.Update.(ToolCallUpdate)
	taskID := firstNonEmpty(req.Ref.TaskID, frame.Ref.TaskID)
	update.Meta = streamFrameToolMeta(update.Meta, req.RawInput, map[string]any{
		"task_id":     taskID,
		"terminal_id": firstNonEmpty(req.Ref.TerminalID, frame.Ref.TerminalID),
		"running":     false,
		"state":       status,
		"result":      finalMessage,
	}, "", taskID)
	update.Meta = metautil.WithCompactRuntimeSection(update.Meta, metautil.RuntimeTask, map[string]any{
		metautil.RuntimeTaskID:         taskID,
		metautil.RuntimeTaskTerminalID: firstNonEmpty(req.Ref.TerminalID, frame.Ref.TerminalID),
		"output_cursor":                frame.Cursor.Output,
		"running":                      false,
		"state":                        status,
		"result":                       finalMessage,
	})
	env.Update = update
	return env
}

func streamDisplayTerminalID(req StreamRequest, frame stream.Frame) string {
	return firstNonEmpty(req.DisplayTerminalID, frame.Ref.TerminalID, req.Ref.TerminalID, req.CallID)
}

func streamToolUpdateEnvelope(req StreamRequest, frame stream.Frame, status string, includeStatus bool, isErr bool, terminalText string, meta map[string]any) eventstream.Envelope {
	terminalID := firstNonEmpty(frame.Ref.TerminalID, req.Ref.TerminalID)
	metaOutput := map[string]any{
		"task_id":       firstNonEmpty(frame.Ref.TaskID, req.Ref.TaskID),
		"terminal_id":   terminalID,
		"running":       status == toolStatusRunning,
		"state":         streamFrameState(frame),
		"output_cursor": frame.Cursor.Output,
	}
	if status != toolStatusRunning {
		metaOutput["running"] = false
		metaOutput["state"] = acpToolStatus(status)
	}
	if state := strings.TrimSpace(frame.State); state != "" {
		metaOutput["state"] = state
	}
	occurredAt := frame.UpdatedAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	update := ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(req.CallID),
		Meta:          streamFrameToolMeta(meta, req.RawInput, metaOutput, "", firstNonEmpty(frame.Ref.TaskID, req.Ref.TaskID)),
	}
	if terminalText != "" {
		update.Meta = metautil.WithTerminalOutput(update.Meta, streamDisplayTerminalID(req, frame), terminalText)
	}
	if includeStatus {
		statusText := acpToolStatus(status)
		update.Status = stringPtr(statusText)
	}
	update = withDisplayTerminalUpdate(update, req.CallID, req.ToolName)
	scope := req.Scope
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
		Delivery:      streamFrameDelivery(req, terminalText),
		Update:        update,
		Meta:          streamFrameMetaForEnvelope(isErr),
	}
}

func streamFrameDelivery(req StreamRequest, terminalText string) *eventstream.Delivery {
	delivery := &eventstream.Delivery{Transient: true}
	if terminalText != "" && streamParentToolCompatibilityMirror(req) {
		delivery.IsParentToolMirror = true
	}
	return delivery
}

func streamParentToolCompatibilityMirror(req StreamRequest) bool {
	canonical, ok := names.Resolve(req.ToolName)
	return ok && (canonical == names.Spawn || canonical == names.Task)
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
	return metautil.WithCompactRuntimeSection(nil, metautil.RuntimeTool, map[string]any{"error": true})
}

func subagentFinalToolStatus(frame stream.Frame) (string, bool) {
	state := strings.ToLower(strings.TrimSpace(frame.State))
	switch state {
	case "failed":
		return schema.ToolStatusFailed, true
	case "interrupted":
		return toolStatusInterrupted, true
	case "cancelled", "canceled":
		return toolStatusCancelled, true
	}
	return schema.ToolStatusCompleted, false
}

func streamFinalTerminalText(text string, cursor stream.Cursor, status string) string {
	if terminalStreamTextHasContent(text) {
		return text
	}
	if cursor.Output == 0 && (status == schema.ToolStatusCompleted || status == schema.ToolStatusFailed) {
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
		values[metautil.RuntimeToolAction] = action
	}
	if taskID = firstNonEmpty(taskID, stringValue(output["handle"]), stringValue(output["task_id"]), stringValue(input["task_id"])); taskID != "" {
		values[metautil.RuntimeTargetID] = taskID
	}
	for _, key := range []string{"agent", "handle", "mention", "prompt", "target_kind", "input"} {
		if value := firstNonEmpty(stringValue(output[key]), stringValue(input[key])); value != "" {
			values[key] = value
		}
	}
	if len(values) == 0 {
		return meta
	}
	return metautil.WithCompactRuntimeSection(meta, metautil.RuntimeTool, values)
}

func shouldProjectFrameTextToParentTool(frame stream.Frame) bool {
	if frame.Event != nil && session.ProtocolSessionUpdateTypeOfProtocol(frame.Event.Protocol) == string(session.ProtocolUpdateTypeAgentThought) {
		return false
	}
	return true
}

func streamFrameEmbeddedEvents(req StreamRequest, frame stream.Frame, hasParentToolMirror bool) []eventstream.Envelope {
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
	parentTool := streamParentToolRelation(req)
	event.Meta = streamFrameEventMeta(event.Meta)
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
	for i := range out {
		if out[i].Scope != eventstream.ScopeSubagent {
			continue
		}
		if parentTool != nil {
			parentToolCopy := *parentTool
			out[i].ParentTool = &parentToolCopy
		}
		out[i].Delivery = &eventstream.Delivery{
			Transient:           true,
			HasParentToolMirror: hasParentToolMirror,
		}
	}
	return out
}

func streamParentToolRelation(req StreamRequest) *eventstream.ParentToolRelation {
	toolCallID := strings.TrimSpace(req.CallID)
	if toolCallID == "" {
		return nil
	}
	return &eventstream.ParentToolRelation{
		ToolCallID: toolCallID,
		ToolName:   strings.TrimSpace(req.ToolName),
	}
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

func streamFrameEventMeta(meta map[string]any) map[string]any {
	return metautil.WithCompactRuntimeSection(meta, metautil.RuntimeStream, map[string]any{
		metautil.RuntimeStreamMode: "append",
	})
}

func streamFrameMeta(mode string) map[string]any {
	return metautil.WithCompactRuntimeSection(nil, metautil.RuntimeStream, map[string]any{
		metautil.RuntimeStreamMode: strings.TrimSpace(mode),
	})
}

const (
	toolStatusRunning     = "running"
	toolStatusInterrupted = "interrupted"
	toolStatusCancelled   = "cancelled"
)

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
