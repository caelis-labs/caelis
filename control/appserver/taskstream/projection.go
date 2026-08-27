package taskstream

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/metautil"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
)

// taskFrameProjectionRequest carries only the Control Task descriptor facts
// required to project one observation frame for a presentation client.
type taskFrameProjectionRequest struct {
	TurnID    string
	SessionID string
	CallID    string
	ToolName  string
	// TaskHandle is the Session-scoped public Task identity used only for
	// display metadata. Ref.TaskID remains the typed stream address.
	TaskHandle        string
	Ref               stream.Ref
	DisplayTerminalID string
	Scope             eventstream.Scope
	ParticipantID     string
}

// projectTaskStreamFrame projects one frame for the Task-owned stream. It never
// manufactures a parent Spawn or Task update. Parent status and results remain
// on the Session feed.
func projectTaskStreamFrame(req taskFrameProjectionRequest, frame stream.Frame) []eventstream.Envelope {
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
		SessionID:  strings.TrimSpace(req.SessionID),
		TurnID:     firstString(strings.TrimSpace(frame.Ref.TerminalID), strings.TrimSpace(req.TurnID)),
		OccurredAt: occurredAt,
		Scope:      eventstream.ScopeSubagent,
		ScopeID:    firstString(strings.TrimSpace(frame.Ref.TaskID), strings.TrimSpace(req.Ref.TaskID)),
		ParentTool: streamParentToolRelation(req),
		Delivery:   streamFrameDelivery(),
		Lifecycle:  &eventstream.Lifecycle{State: state},
		Final:      true,
	}}
}

func commandTaskStreamFrameEvents(req taskFrameProjectionRequest, frame stream.Frame) []eventstream.Envelope {
	if frame.Closed {
		return []eventstream.Envelope{streamFinalFrameEvent(req, frame)}
	}
	if frame.Text != "" && shouldProjectFrameTextToParentTool(frame) {
		return []eventstream.Envelope{streamFrameEvent(req, frame)}
	}
	return nil
}

func delegatedParentStream(req taskFrameProjectionRequest) bool {
	return req.ToolName == spawn.ToolName || req.ToolName == tasktool.ToolName
}

func streamFrameEvent(req taskFrameProjectionRequest, frame stream.Frame) eventstream.Envelope {
	return streamToolUpdateEnvelope(req, frame, toolStatusRunning, true, false, frame.Text, streamFrameMeta("append", frame.Cursor.Output), true)
}

func streamFinalFrameEvent(req taskFrameProjectionRequest, frame stream.Frame) eventstream.Envelope {
	status, isErr := subagentFinalToolStatus(frame)
	finalText := ""
	if frame.Cursor.Output == 0 {
		finalText = streamFinalTerminalText(frame.Text)
	}
	return streamToolUpdateEnvelope(req, frame, status, true, isErr, finalText, streamFrameMeta("final", frame.Cursor.Output), true)
}

func streamDisplayTerminalID(req taskFrameProjectionRequest, frame stream.Frame) string {
	return firstString(req.DisplayTerminalID, frame.Ref.TerminalID, req.Ref.TerminalID, req.CallID)
}

func streamTerminalExitID(req taskFrameProjectionRequest, frame stream.Frame) string {
	if terminalID, ok := commandDisplayTerminalID(req.CallID, req.ToolName); ok {
		return terminalID
	}
	return streamDisplayTerminalID(req, frame)
}

func streamToolUpdateEnvelope(req taskFrameProjectionRequest, frame stream.Frame, status string, includeStatus bool, isErr bool, terminalText string, meta map[string]any, includeDisplayTerminal bool) eventstream.Envelope {
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
	update := schema.ToolCallUpdate{
		SessionUpdate: schema.UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(req.CallID),
		Meta:          streamFrameToolMeta(meta, req.TaskHandle),
	}
	if terminalText != "" {
		update.Meta = metautil.WithTerminalOutput(update.Meta, streamDisplayTerminalID(req, frame), terminalText)
	}
	if includeStatus {
		statusText := taskStreamToolStatus(status)
		update.Status = &statusText
	}
	if includeDisplayTerminal {
		update = withCommandDisplayTerminal(update, req.CallID, req.ToolName)
		if frame.Closed {
			// withCommandDisplayTerminal preserves the Zed-compatible empty terminal
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
		SessionID:     strings.TrimSpace(req.SessionID),
		TurnID:        strings.TrimSpace(req.TurnID),
		OccurredAt:    occurredAt,
		Scope:         scope,
		ScopeID:       streamRequestScopeID(req),
		ParticipantID: strings.TrimSpace(req.ParticipantID),
		Delivery:      streamFrameDelivery(),
		Update:        update,
		Meta:          streamFrameMetaForEnvelope(isErr),
	}
}

func withCommandDisplayTerminal(update schema.ToolCallUpdate, toolCallID string, toolName string) schema.ToolCallUpdate {
	toolName = strings.TrimSpace(toolName)
	if toolName != "" {
		update.Meta = metautil.WithRuntimeSection(update.Meta, metautil.RuntimeTool, map[string]any{
			metautil.RuntimeToolName: toolName,
		})
	}
	terminalID, ok := commandDisplayTerminalID(toolCallID, toolName)
	if !ok {
		return update
	}
	update.Meta = metautil.WithTerminalInfo(update.Meta, terminalID)
	update.Content = []schema.ToolCallContent{{Type: "terminal", TerminalID: terminalID}}
	return update
}

func commandDisplayTerminalID(toolCallID string, toolName string) (string, bool) {
	if toolName != shell.RunCommandToolName {
		return "", false
	}
	terminalID := strings.TrimSpace(toolCallID)
	return terminalID, terminalID != ""
}

func taskStreamToolStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", schema.ToolStatusPending, schema.ToolStatusInProgress, schema.ToolStatusCompleted, schema.ToolStatusFailed:
		return strings.TrimSpace(status)
	case "started", "running", "waiting_approval":
		return schema.ToolStatusInProgress
	case "cancelled", "canceled", "interrupted", "terminated", "timed_out", "timeout":
		return schema.ToolStatusFailed
	default:
		return strings.TrimSpace(status)
	}
}

func streamFrameDelivery() *eventstream.Delivery {
	return &eventstream.Delivery{Mode: eventstream.DeliveryTransient}
}

func streamRequestScopeID(req taskFrameProjectionRequest) string {
	return firstString(strings.TrimSpace(req.SessionID), strings.TrimSpace(req.TurnID))
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

func streamFrameToolMeta(meta map[string]any, taskHandle string) map[string]any {
	taskHandle = strings.TrimSpace(taskHandle)
	if taskHandle == "" {
		return meta
	}
	return metautil.WithCompactRuntimeSection(meta, metautil.RuntimeTool, map[string]any{
		metautil.RuntimeTargetHandle: taskHandle,
	})
}

func shouldProjectFrameTextToParentTool(frame stream.Frame) bool {
	if frame.Event != nil && session.ProtocolSessionUpdateTypeOfProtocol(frame.Event.Protocol) == string(session.ProtocolUpdateTypeAgentThought) {
		return false
	}
	return true
}

func streamFrameEmbeddedEvents(req taskFrameProjectionRequest, frame stream.Frame) []eventstream.Envelope {
	event := session.CloneEvent(frame.Event)
	if event == nil {
		return nil
	}
	if event.Scope != nil && event.Scope.Participant.Kind == session.ParticipantKindSubagent {
		taskID := firstString(strings.TrimSpace(frame.Ref.TaskID), strings.TrimSpace(req.Ref.TaskID))
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
	base := acpprojector.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: req.SessionID}, event, acpprojector.SessionEventTransport{
		TurnID: req.TurnID,
	})
	out := acpprojector.ProjectSessionEventEnvelope(base, event)
	out = taskStreamPrimaryEnvelope(out)
	if taskID := firstString(strings.TrimSpace(frame.Ref.TaskID), strings.TrimSpace(req.Ref.TaskID)); taskID != "" {
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

func streamParentToolRelation(req taskFrameProjectionRequest) *eventstream.ParentToolRelation {
	toolCallID := strings.TrimSpace(req.CallID)
	if toolCallID == "" {
		return nil
	}
	return &eventstream.ParentToolRelation{
		ToolCallID: toolCallID,
		ToolName:   strings.TrimSpace(req.ToolName),
	}
}

func streamFrameSessionEventIsParentToolEcho(req taskFrameProjectionRequest, event *session.Event) bool {
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
	return parentTool == "" || toolName == "" || parentTool == toolName
}

func streamFrameEventMeta(meta map[string]any) map[string]any {
	return metautil.WithCompactRuntimeSection(meta, metautil.RuntimeStream, map[string]any{
		metautil.RuntimeStreamMode: "append",
	})
}

func streamFrameMeta(mode string, outputCursor int64) map[string]any {
	return metautil.WithCompactRuntimeSection(nil, metautil.RuntimeStream, map[string]any{
		metautil.RuntimeStreamMode:   strings.TrimSpace(mode),
		metautil.RuntimeOutputCursor: max(outputCursor, 0),
	})
}

const (
	toolStatusRunning     = "running"
	toolStatusInterrupted = "interrupted"
	toolStatusCancelled   = "cancelled"
)
