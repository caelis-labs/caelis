package taskstream

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/shell"
	"github.com/caelis-labs/caelis/agent-sdk/tool/builtin/spawn"
	tasktool "github.com/caelis-labs/caelis/agent-sdk/tool/builtin/task"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/internal/eventmeta"
	acpprojector "github.com/caelis-labs/caelis/control/appserver/projection"
	controltaskstream "github.com/caelis-labs/caelis/control/taskstream"
)

const runtimeToolTargetHandleMetaKey = "target_handle"

// taskFrameProjectionRequest carries only the Control Task descriptor facts
// required to project one observation frame for a presentation client.
type taskFrameProjectionRequest struct {
	TurnID    string
	SessionID string
	CallID    string
	ToolName  string
	// TaskHandle is the Session-scoped public Task identity used only for
	// display metadata. TaskID remains the typed stream address.
	TaskHandle        string
	TaskID            string
	DisplayTerminalID string
	Scope             eventstream.Scope
	ParticipantID     string
}

// projectTaskStreamFrame projects one frame for the Task-owned stream. It never
// manufactures a parent Spawn or Task update. Parent status and results remain
// on the Session feed.
func projectTaskStreamFrame(req taskFrameProjectionRequest, frame controltaskstream.Frame) []eventstream.Envelope {
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
		TurnID:     firstString(strings.TrimSpace(frame.TerminalID), strings.TrimSpace(req.TurnID)),
		OccurredAt: occurredAt,
		Scope:      eventstream.ScopeSubagent,
		ScopeID:    strings.TrimSpace(req.TaskID),
		ParentTool: streamParentToolRelation(req),
		Delivery:   streamFrameDelivery(),
		Lifecycle:  &eventstream.Lifecycle{State: state},
		Final:      true,
	}}
}

func commandTaskStreamFrameEvents(req taskFrameProjectionRequest, frame controltaskstream.Frame) []eventstream.Envelope {
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

func streamFrameEvent(req taskFrameProjectionRequest, frame controltaskstream.Frame) eventstream.Envelope {
	return streamToolUpdateEnvelope(req, frame, toolStatusRunning, true, false, frame.Text, streamFrameMeta("append"), true)
}

func streamFinalFrameEvent(req taskFrameProjectionRequest, frame controltaskstream.Frame) eventstream.Envelope {
	status, isErr := subagentFinalToolStatus(frame)
	finalText := streamFinalTerminalText(frame.Text)
	return streamToolUpdateEnvelope(req, frame, status, true, isErr, finalText, streamFrameMeta("final"), true)
}

func streamDisplayTerminalID(req taskFrameProjectionRequest, frame controltaskstream.Frame) string {
	return firstString(req.DisplayTerminalID, frame.TerminalID, req.TurnID, req.CallID)
}

func streamTerminalExitID(req taskFrameProjectionRequest, frame controltaskstream.Frame) string {
	if terminalID, ok := commandDisplayTerminalID(req.CallID, req.ToolName); ok {
		return terminalID
	}
	return streamDisplayTerminalID(req, frame)
}

func streamToolUpdateEnvelope(req taskFrameProjectionRequest, frame controltaskstream.Frame, status string, includeStatus bool, isErr bool, terminalText string, meta map[string]any, includeDisplayTerminal bool) eventstream.Envelope {
	occurredAt := frame.UpdatedAt
	if occurredAt.IsZero() {
		occurredAt = time.Now()
	}
	update := eventstream.ToolCallUpdate{
		SessionUpdate: eventstream.UpdateToolCallInfo,
		ToolCallID:    strings.TrimSpace(req.CallID),
		Meta:          streamFrameToolMeta(meta, req.TaskHandle),
	}
	if terminalText != "" {
		update.Meta = eventmeta.WithTerminalOutput(update.Meta, streamDisplayTerminalID(req, frame), terminalText)
	}
	if includeStatus {
		statusText := taskStreamToolStatus(status)
		update.Status = &statusText
	}
	if includeDisplayTerminal {
		update = withCommandDisplayTerminal(update, req.CallID, req.ToolName)
		if frame.Closed {
			// The stream close frame is the authoritative runtime exit-code
			// carrier. Keep the content collection omitted and publish lifecycle
			// only through the terminal extension metadata.
			update.Meta = eventmeta.WithTerminalExit(update.Meta, streamTerminalExitID(req, frame), frame.ExitCode, nil)
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

func withCommandDisplayTerminal(update eventstream.ToolCallUpdate, toolCallID string, toolName string) eventstream.ToolCallUpdate {
	toolName = strings.TrimSpace(toolName)
	if toolName != "" {
		update.Meta = eventmeta.WithRuntimeSection(update.Meta, eventmeta.RuntimeTool, map[string]any{
			eventmeta.RuntimeToolName: toolName,
		})
	}
	terminalID, ok := commandDisplayTerminalID(toolCallID, toolName)
	if !ok {
		return update
	}
	update.Meta = eventmeta.WithTerminalInfo(update.Meta, terminalID)
	// The RunCommand tool_call already mounted this terminal. TaskStream output
	// and lifecycle are sparse tool_call_update patches, so repeating a present
	// terminal-only content collection would authoritatively replace rendered
	// output with an empty collection.
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
	case "", eventstream.ToolStatusPending, eventstream.ToolStatusInProgress, eventstream.ToolStatusCompleted, eventstream.ToolStatusFailed:
		return strings.TrimSpace(status)
	case "started", "running", "waiting_approval":
		return eventstream.ToolStatusInProgress
	case "cancelled", "canceled", "interrupted", "terminated", "timed_out", "timeout":
		return eventstream.ToolStatusFailed
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
	return eventmeta.WithCompactRuntimeSection(nil, eventmeta.RuntimeTool, map[string]any{"error": true})
}

func subagentFinalToolStatus(frame controltaskstream.Frame) (string, bool) {
	state := strings.ToLower(strings.TrimSpace(frame.State))
	switch state {
	case "completed":
		return eventstream.ToolStatusCompleted, false
	case "failed":
		return eventstream.ToolStatusFailed, true
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
	// empty-panel fallback after it has applied all earlier FIFO frames.
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
	return eventmeta.WithCompactRuntimeSection(meta, eventmeta.RuntimeTool, map[string]any{
		runtimeToolTargetHandleMetaKey: taskHandle,
	})
}

func shouldProjectFrameTextToParentTool(frame controltaskstream.Frame) bool {
	if frame.Event != nil && session.ProtocolSessionUpdateTypeOfProtocol(frame.Event.Protocol) == string(session.ProtocolUpdateTypeAgentThought) {
		return false
	}
	return true
}

func streamFrameEmbeddedEvents(req taskFrameProjectionRequest, frame controltaskstream.Frame) []eventstream.Envelope {
	event := session.CloneEvent(frame.Event)
	if event == nil {
		return nil
	}
	if event.Scope != nil && event.Scope.Participant.Kind == session.ParticipantKindSubagent {
		taskID := strings.TrimSpace(req.TaskID)
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
	// The child-facing source is the parent topology identity, not the
	// controller executor name retained by a recorded Task frame.
	if session.ProtocolAgentCommunicationOf(event) != nil && event.Actor.Kind == session.ActorKindController {
		event.Actor = session.ParentCommunicationActor()
	}
	parentTool := streamParentToolRelation(req)
	event.Meta = streamFrameEventMeta(event.Meta)
	base := acpprojector.EnvelopeBaseFromSessionEvent(session.SessionRef{SessionID: req.SessionID}, event, acpprojector.SessionEventTransport{
		TurnID: req.TurnID,
	})
	out := acpprojector.ProjectSessionEventEnvelope(base, event)
	out = taskStreamPrimaryEnvelope(out)
	if taskID := strings.TrimSpace(req.TaskID); taskID != "" {
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
		if eventstream.UpdateType(envelope.Update) != eventstream.UpdateUsage {
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
	return eventmeta.WithCompactRuntimeSection(meta, eventmeta.RuntimeStream, map[string]any{
		eventmeta.RuntimeStreamMode: "append",
	})
}

func streamFrameMeta(mode string) map[string]any {
	return eventmeta.WithCompactRuntimeSection(nil, eventmeta.RuntimeStream, map[string]any{
		eventmeta.RuntimeStreamMode: strings.TrimSpace(mode),
	})
}

const (
	toolStatusRunning     = "running"
	toolStatusInterrupted = "interrupted"
	toolStatusCancelled   = "cancelled"
)
