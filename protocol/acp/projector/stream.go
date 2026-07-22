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
	// TaskHandle is the Session-scoped public Task identity used only for
	// display metadata. Ref.TaskID remains the typed stream address.
	TaskHandle        string
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

// ProjectTaskStreamFrame projects one frame for the Task-owned stream. It never
// manufactures a parent Spawn or Task update. Parent status and results remain
// on the Session feed.
func ProjectTaskStreamFrame(req StreamRequest, frame stream.Frame) []eventstream.Envelope {
	if !delegatedParentStream(req) {
		return commandTaskStreamFrameEvents(req, frame)
	}
	embedded := streamFrameEmbeddedEvents(req, frame)
	if len(embedded) > 0 {
		if frame.Closed {
			for i := range embedded {
				embedded[i].Final = true
			}
		}
		return embedded
	}
	if !frame.Closed {
		return nil
	}
	state := strings.ToLower(strings.TrimSpace(frame.State))
	if !eventstream.IsTerminalLifecycleState(state) {
		state = eventstream.LifecycleStateUnknown
	}
	occurredAt := frame.UpdatedAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	return []eventstream.Envelope{{
		Kind:       eventstream.KindLifecycle,
		SessionID:  strings.TrimSpace(req.SessionRef.SessionID),
		TurnID:     firstNonEmpty(strings.TrimSpace(frame.Ref.TerminalID), strings.TrimSpace(req.SourceID)),
		OccurredAt: occurredAt,
		Scope:      eventstream.ScopeSubagent,
		ScopeID:    firstNonEmpty(strings.TrimSpace(frame.Ref.TaskID), strings.TrimSpace(req.Ref.TaskID)),
		ParentTool: streamParentToolRelation(req),
		Delivery:   streamFrameDelivery(),
		Lifecycle:  &eventstream.Lifecycle{State: state},
		Final:      true,
	}}
}

func commandTaskStreamFrameEvents(req StreamRequest, frame stream.Frame) []eventstream.Envelope {
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
	if frame.TruncatedBefore > 0 {
		meta = metautil.WithCompactRuntimeSection(meta, metautil.RuntimeStream, map[string]any{
			metautil.RuntimeStreamTruncated: true,
			metautil.RuntimeStreamBefore:    frame.TruncatedBefore,
		})
	}
	occurredAt := frame.UpdatedAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	update := ToolCallUpdate{
		SessionUpdate: UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(req.CallID),
		Meta:          streamFrameToolMeta(meta, req.RawInput, nil, "", req.TaskHandle),
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
	case "completed":
		return schema.ToolStatusCompleted, false
	case "failed":
		return schema.ToolStatusFailed, true
	case "interrupted":
		return toolStatusInterrupted, true
	case "cancelled", "canceled":
		return toolStatusCancelled, true
	case "terminated", "unknown_outcome":
		return state, true
	default:
		return eventstream.LifecycleStateUnknown, true
	}
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

func streamFrameToolMeta(meta map[string]any, input map[string]any, output map[string]any, action string, taskHandle string) map[string]any {
	values := map[string]any{}
	if action = firstNonEmpty(action, stringValue(output["action"]), stringValue(input["action"])); action != "" {
		values[metautil.RuntimeToolAction] = action
	}
	if taskHandle = firstNonEmpty(taskHandle, stringValue(output["handle"]), stringValue(input["handle"])); taskHandle != "" {
		values[metautil.RuntimeTargetHandle] = taskHandle
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
	out = taskStreamPrimaryEnvelope(out)
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

func taskStreamPrimaryEnvelope(events []eventstream.Envelope) []eventstream.Envelope {
	if len(events) <= 1 {
		return events
	}
	// One SDK Task frame is one public resume unit. Generic Session projection
	// may append a sibling usage_update to a narrative event, but publishing
	// both with the frame cursor would make a mid-record resume lossy. Keep the
	// semantic event; a usage-only frame still projects its usage envelope.
	for _, envelope := range events {
		if eventstream.UpdateType(envelope.Update) != schema.UpdateUsage {
			return []eventstream.Envelope{envelope}
		}
	}
	return events[:1]
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

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return strings.TrimSpace(text)
	}
	return ""
}
