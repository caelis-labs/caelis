package projector

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	taskapi "github.com/caelis-labs/caelis/agent-sdk/task"
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
	HandleID   string
	RunID      string
	TurnID     string
	SessionRef session.SessionRef
	// SourceID identifies the physical task stream independently from the
	// tool call that happened to observe it. Subagent turns use their stable
	// runtime turn ID; terminal-backed commands fall back to Ref.TerminalID.
	SourceID       string
	CallID         string
	ToolName       string
	ParentCallID   string
	ParentToolName string
	// TargetKind distinguishes the physical task family behind a TASK call.
	TargetKind taskapi.Kind
	// Observer marks a TASK wait that does not own a second physical stream.
	Observer          bool
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
	sourceID := firstNonEmpty(
		strings.TrimSpace(r.SourceID),
		strings.TrimSpace(r.Ref.TerminalID),
		strings.TrimSpace(r.Ref.TaskID),
	)
	return strings.Join([]string{
		strings.TrimSpace(r.SessionRef.SessionID),
		strings.TrimSpace(r.Ref.TaskID),
		sourceID,
	}, "|")
}

// ProjectStreamFrame projects one runtime stream frame into transient
// ACP-native envelopes for live clients.
func ProjectStreamFrame(req StreamRequest, frame stream.Frame) []eventstream.Envelope {
	parent := parentStreamFrameEvents(req, frame)
	embedded := streamFrameEmbeddedEvents(req, frame)
	out := make([]eventstream.Envelope, 0, len(embedded)+len(parent))
	out = append(out, embedded...)
	out = append(out, parent...)
	return out
}

func parentStreamFrameEvents(req StreamRequest, frame stream.Frame) []eventstream.Envelope {
	if delegatedParentStream(req) {
		// TASK wait is an observer of the Spawn-owned physical stream. Its own
		// canonical tool result is already emitted by Runtime, so replaying a
		// synthetic parent close here would duplicate or reorder that result.
		if req.Observer {
			return nil
		}
		if frame.Closed {
			return []eventstream.Envelope{delegatedFinalFrameEvent(req, frame)}
		}
		return nil
	}
	if frame.Closed {
		return []eventstream.Envelope{streamFinalFrameEvent(req, frame)}
	}
	if frame.Text != "" && shouldProjectFrameTextToParentTool(frame) {
		return []eventstream.Envelope{streamFrameEvent(req, frame)}
	}
	return nil
}

func delegatedParentStream(req StreamRequest) bool {
	canonical, ok := names.Resolve(req.ToolName)
	return ok && (canonical == names.Spawn || canonical == names.Task)
}

func streamFrameEvent(req StreamRequest, frame stream.Frame) eventstream.Envelope {
	return streamToolUpdateEnvelope(req, frame, toolStatusRunning, true, false, frame.Text, streamFrameMeta("append"), true)
}

func streamFinalFrameEvent(req StreamRequest, frame stream.Frame) eventstream.Envelope {
	status, isErr := subagentFinalToolStatus(frame)
	finalText := ""
	if frame.Cursor.Output == 0 {
		finalText = streamFinalTerminalText(frame.Text)
	}
	return streamToolUpdateEnvelope(req, frame, status, true, isErr, finalText, streamFrameMeta("final"), true)
}

func delegatedFinalFrameEvent(req StreamRequest, frame stream.Frame) eventstream.Envelope {
	status, isErr := subagentFinalToolStatus(frame)
	finalMessage := display.CleanSubagentFinalOutput(frame.Text)
	if terminalID := strings.TrimSpace(req.Ref.TerminalID); terminalID != "" {
		frame = stream.CloneFrame(frame)
		frame.Ref.TerminalID = terminalID
	}
	env := streamToolUpdateEnvelope(req, frame, status, true, isErr, "", streamFrameMeta("final"), false)
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

func streamTerminalExitID(req StreamRequest, frame stream.Frame) string {
	if terminalID, ok := display.DisplayTerminalID(req.CallID, req.ToolName); ok {
		return terminalID
	}
	return streamDisplayTerminalID(req, frame)
}

func streamToolUpdateEnvelope(req StreamRequest, frame stream.Frame, status string, includeStatus bool, isErr bool, terminalText string, meta map[string]any, includeDisplayTerminal bool) eventstream.Envelope {
	terminalID := firstNonEmpty(frame.Ref.TerminalID, req.Ref.TerminalID)
	if frame.TruncatedBefore > 0 {
		meta = metautil.WithCompactRuntimeSection(meta, metautil.RuntimeStream, map[string]any{
			metautil.RuntimeStreamTruncated: true,
			metautil.RuntimeStreamBefore:    frame.TruncatedBefore,
		})
	}
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
	if includeDisplayTerminal {
		update = withDisplayTerminalUpdate(update, req.CallID, req.ToolName)
		if frame.Closed {
			// withDisplayTerminalUpdate preserves the Zed-compatible empty terminal
			// anchor and installs the final terminal metadata. The stream close frame
			// is the authoritative runtime exit-code carrier, so retain it here.
			update.Meta = metautil.WithTerminalExit(update.Meta, streamTerminalExitID(req, frame), frame.ExitCode, nil)
		}
	}
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
		Delivery:      streamFrameDelivery(),
		Update:        update,
		Meta:          streamFrameMetaForEnvelope(isErr),
	}
}

func streamFrameDelivery() *eventstream.Delivery {
	return &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
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

func streamFinalTerminalText(text string) string {
	// terminal_output carries exact runtime bytes. The task stream's FinalText
	// may contain this display-only placeholder when no byte was produced; keep
	// that synthetic state out of the protocol and let each Surface render an
	// empty-panel fallback after it has reconciled all earlier stream frames.
	if strings.TrimSpace(text) == "(no output)" {
		return ""
	}
	return text
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
	// Permission routing is a Control interaction, not a child stream delivery
	// concern. The bridge normalizes it into ApprovalRequest and Control later
	// publishes the single active request through the Turn event stream.
	if session.ProtocolPermissionOf(event) != nil {
		return nil
	}
	parentTool := streamParentToolRelation(req)
	event.Meta = streamFrameEventMeta(event.Meta)
	base := EnvelopeBaseFromSessionEvent(req.SessionRef, event, SessionEventTransport{
		HandleID: req.HandleID,
		RunID:    req.RunID,
		TurnID:   req.TurnID,
	})
	out := ProjectSessionEventEnvelope(base, event)
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
		if event.ChildOrigin == nil {
			out[i].Delivery = &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
		}
	}
	return out
}

func streamParentToolRelation(req StreamRequest) *eventstream.ParentToolRelation {
	toolCallID := firstNonEmpty(strings.TrimSpace(req.ParentCallID), strings.TrimSpace(req.CallID))
	if toolCallID == "" {
		return nil
	}
	return &eventstream.ParentToolRelation{
		ToolCallID: toolCallID,
		ToolName:   firstNonEmpty(strings.TrimSpace(req.ParentToolName), strings.TrimSpace(req.ToolName)),
	}
}

func streamFrameSessionEventIsParentToolEcho(req StreamRequest, event *session.Event) bool {
	parentCallID := firstNonEmpty(strings.TrimSpace(req.ParentCallID), strings.TrimSpace(req.CallID))
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
	parentTool := firstNonEmpty(strings.TrimSpace(req.ParentToolName), strings.TrimSpace(req.ToolName))
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
