package tuiapp

import (
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
)

type TranscriptEventKind string

const (
	TranscriptEventNarrative   TranscriptEventKind = "narrative"
	TranscriptEventNotice      TranscriptEventKind = "notice"
	TranscriptEventPlan        TranscriptEventKind = "plan"
	TranscriptEventTool        TranscriptEventKind = "tool"
	TranscriptEventApproval    TranscriptEventKind = "approval"
	TranscriptEventParticipant TranscriptEventKind = "participant"
	TranscriptEventLifecycle   TranscriptEventKind = "lifecycle"
	TranscriptEventUsage       TranscriptEventKind = "usage"
)

type TranscriptNarrativeKind string

const (
	TranscriptNarrativeUser      TranscriptNarrativeKind = "user"
	TranscriptNarrativeAssistant TranscriptNarrativeKind = "assistant"
	TranscriptNarrativeReasoning TranscriptNarrativeKind = "reasoning"
	TranscriptNarrativeSystem    TranscriptNarrativeKind = "system"
	TranscriptNarrativeNotice    TranscriptNarrativeKind = "notice"
)

type TranscriptEvent struct {
	Kind       TranscriptEventKind
	Scope      ACPProjectionScope
	ScopeID    string
	Actor      string
	OccurredAt time.Time

	NarrativeKind TranscriptNarrativeKind
	Text          string
	Final         bool

	ToolCallID          string
	ToolName            string
	ToolKind            string
	ToolTitle           string
	ToolArgs            string
	ToolFullArgs        string
	ToolOutput          string
	ToolStream          string
	ToolStatus          string
	ToolError           bool
	ToolOutputSynthetic bool
	ToolTaskID          string
	ToolTaskAction      string
	ToolTaskInput       string
	ToolTaskTargetKind  string

	PlanEntries []PlanEntry

	ApprovalTool    string
	ApprovalCommand string
	ApprovalStatus  string
	ApprovalRisk    string
	ApprovalAuth    string
	ApprovalText    string

	State string

	Usage *kernel.UsageSnapshot

	AnchorToolCallID string
	AnchorToolName   string
}

func ProjectGatewayEventToTranscriptEvents(ev kernel.Event) []TranscriptEvent {
	scope := gatewayEventScope(ev)
	scopeID := gatewayEventScopeID(ev)
	occurredAt := ev.OccurredAt
	update := gatewayACPProtocolUpdate(ev)
	if update != nil {
		ev = gatewayEventWithProtocolUpdateMeta(ev, update)
	}
	anchorToolCallID := kernel.EventMetaString(ev.Meta, "caelis", "runtime", "stream", "parent_call_id")
	anchorToolName := kernel.EventMetaString(ev.Meta, "caelis", "runtime", "stream", "parent_tool")
	out := make([]TranscriptEvent, 0, 4)

	appendUsage := func() {
		if ev.Usage == nil {
			return
		}
		usage := *ev.Usage
		out = append(out, TranscriptEvent{
			Kind:       TranscriptEventUsage,
			Scope:      scope,
			ScopeID:    scopeID,
			OccurredAt: occurredAt,
			Usage:      &usage,
		})
	}

	if projected, handled := projectACPProtocolUpdateToTranscriptEvents(ev, update, scope, scopeID, occurredAt); handled {
		out = append(out, projected...)
		for i := range out {
			out[i].AnchorToolCallID = anchorToolCallID
			out[i].AnchorToolName = anchorToolName
		}
		appendUsage()
		return out
	}

	switch ev.Kind {
	case kernel.EventKindUserMessage:
		if scope != ACPProjectionMain {
			break
		}
		if text := strings.TrimSpace(gatewayUserText(ev)); text != "" {
			out = append(out, TranscriptEvent{
				Kind:          TranscriptEventNarrative,
				Scope:         scope,
				ScopeID:       scopeID,
				OccurredAt:    occurredAt,
				NarrativeKind: TranscriptNarrativeUser,
				Text:          text,
				Final:         true,
			})
		}
	case kernel.EventKindAssistantMessage:
		payload := ev.Narrative
		if payload != nil {
			actor := gatewayDisplayActor(ev, payload.Actor)
			switch payload.Role {
			case kernel.NarrativeRoleUser:
				if scope != ACPProjectionMain {
					break
				}
				if text := strings.TrimSpace(payload.Text); text != "" {
					out = append(out, TranscriptEvent{
						Kind:          TranscriptEventNarrative,
						Scope:         scope,
						ScopeID:       scopeID,
						OccurredAt:    occurredAt,
						NarrativeKind: TranscriptNarrativeUser,
						Text:          text,
						Final:         true,
					})
				}
			case kernel.NarrativeRoleAssistant:
				if payloadNarrativeChunkHasContent(payload.ReasoningText, payload.Final) {
					out = append(out, TranscriptEvent{
						Kind:          TranscriptEventNarrative,
						Scope:         scope,
						ScopeID:       scopeID,
						Actor:         actor,
						OccurredAt:    occurredAt,
						NarrativeKind: TranscriptNarrativeReasoning,
						Text:          payload.ReasoningText,
						Final:         payload.Final,
					})
				}
				if payloadNarrativeChunkHasContent(payload.Text, payload.Final) {
					out = append(out, TranscriptEvent{
						Kind:          TranscriptEventNarrative,
						Scope:         scope,
						ScopeID:       scopeID,
						Actor:         actor,
						OccurredAt:    occurredAt,
						NarrativeKind: TranscriptNarrativeAssistant,
						Text:          payload.Text,
						Final:         payload.Final,
					})
				}
			case kernel.NarrativeRoleSystem:
				if text := strings.TrimSpace(payload.Text); text != "" {
					out = append(out, TranscriptEvent{
						Kind:          TranscriptEventNotice,
						Scope:         scope,
						ScopeID:       scopeID,
						Actor:         actor,
						OccurredAt:    occurredAt,
						NarrativeKind: TranscriptNarrativeSystem,
						Text:          text,
						Final:         true,
					})
				}
			case kernel.NarrativeRoleNotice:
				if text := strings.TrimSpace(payload.Text); text != "" {
					out = append(out, TranscriptEvent{
						Kind:          TranscriptEventNotice,
						Scope:         scope,
						ScopeID:       scopeID,
						Actor:         actor,
						OccurredAt:    occurredAt,
						NarrativeKind: TranscriptNarrativeNotice,
						Text:          text,
						Final:         true,
					})
				}
			}
		}
	case kernel.EventKindToolCall:
		if payload := ev.ToolCall; payload != nil {
			toolName := gatewayToolDisplayName(payload.ToolName, payload.ToolTitle, payload.ToolKind)
			if strings.EqualFold(strings.TrimSpace(toolName), "PLAN") {
				break
			}
			out = append(out, projectTranscriptToolCall(transcriptToolProjection{
				Event:      ev,
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      gatewayDisplayActor(ev, payload.Actor),
				OccurredAt: occurredAt,
				CallID:     payload.CallID,
				ToolName:   toolName,
				ToolKind:   payload.ToolKind,
				ToolTitle:  payload.ToolTitle,
				Status:     string(payload.Status),
				RawInput:   gatewayProtocolRawInput(ev, payload.RawInput),
			}))
		}
	case kernel.EventKindToolResult:
		if payload := ev.ToolResult; payload != nil {
			toolName := gatewayToolDisplayName(payload.ToolName, payload.ToolTitle, payload.ToolKind)
			if strings.EqualFold(strings.TrimSpace(toolName), "PLAN") {
				break
			}
			event, ok := projectTranscriptToolResult(transcriptToolProjection{
				Event:      ev,
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      gatewayDisplayActor(ev, payload.Actor),
				OccurredAt: occurredAt,
				CallID:     payload.CallID,
				ToolName:   toolName,
				ToolKind:   payload.ToolKind,
				ToolTitle:  payload.ToolTitle,
				Status:     string(payload.Status),
				RawInput:   gatewayProtocolRawInput(ev, payload.RawInput),
				RawOutput:  payload.RawOutput,
				Content:    gatewayProtocolToolContent(ev, payload.Content),
				Error:      payload.Error,
			}, string(kernel.ToolStatusCompleted))
			if ok {
				out = append(out, event)
			}
		}
	case kernel.EventKindPlanUpdate:
		if payload := ev.Plan; payload != nil {
			entries := make([]PlanEntry, 0, len(payload.Entries))
			for _, entry := range payload.Entries {
				entries = append(entries, PlanEntry{Content: entry.Content, Status: entry.Status})
			}
			if len(entries) > 0 {
				out = append(out, TranscriptEvent{
					Kind:        TranscriptEventPlan,
					Scope:       scope,
					ScopeID:     scopeID,
					OccurredAt:  occurredAt,
					PlanEntries: entries,
				})
			}
		}
	case kernel.EventKindApprovalRequested, kernel.EventKindApprovalReview:
		if ev.Kind == kernel.EventKindApprovalReview && ev.ApprovalPayload != nil && isAutomaticApprovalEvent(ev.ApprovalPayload) {
			if text := automaticApprovalReviewDisplayText(ev.ApprovalPayload); text != "" {
				out = append(out, TranscriptEvent{
					Kind:            TranscriptEventApproval,
					Scope:           scope,
					ScopeID:         scopeID,
					Actor:           gatewayDisplayActor(ev, ""),
					OccurredAt:      occurredAt,
					ToolCallID:      strings.TrimSpace(ev.ApprovalPayload.ToolCallID),
					ApprovalTool:    strings.TrimSpace(ev.ApprovalPayload.ToolName),
					ApprovalCommand: approvalCommandPreview(ev.ApprovalPayload.RawInput),
					ApprovalStatus:  strings.TrimSpace(string(ev.ApprovalPayload.ReviewStatus)),
					ApprovalRisk:    firstNonEmpty(strings.TrimSpace(ev.ApprovalPayload.Risk), approvalReviewValueFromText(text, "risk")),
					ApprovalAuth:    firstNonEmpty(strings.TrimSpace(ev.ApprovalPayload.Authorization), approvalReviewValueFromText(text, "authorization")),
					ApprovalText:    text,
					Final:           true,
				})
			}
		}
		// Manual approval requests are transient composer overlays driven by
		// PromptRequestMsg. They intentionally do not persist into transcript.
	case kernel.EventKindParticipant:
		if payload := ev.Participant; payload != nil {
			state := strings.TrimSpace(string(payload.Action))
			if state != "" {
				out = append(out, TranscriptEvent{
					Kind:       TranscriptEventParticipant,
					Scope:      scope,
					ScopeID:    scopeID,
					OccurredAt: occurredAt,
					State:      state,
				})
			}
		}
	case kernel.EventKindLifecycle:
		if payload := ev.Lifecycle; payload != nil {
			state := strings.ToLower(strings.TrimSpace(string(payload.Status)))
			if state != "" {
				out = append(out, TranscriptEvent{
					Kind:       TranscriptEventLifecycle,
					Scope:      scope,
					ScopeID:    scopeID,
					Actor:      gatewayDisplayActor(ev, payload.Actor),
					OccurredAt: occurredAt,
					State:      state,
				})
			}
		}
	case kernel.EventKindNotice, kernel.EventKindSystemMessage:
		if text := strings.TrimSpace(gatewayNoticeText(ev)); text != "" {
			out = append(out, TranscriptEvent{
				Kind:          TranscriptEventNotice,
				Scope:         scope,
				ScopeID:       scopeID,
				OccurredAt:    occurredAt,
				NarrativeKind: TranscriptNarrativeNotice,
				Text:          text,
				Final:         true,
			})
		}
	}

	for i := range out {
		out[i].AnchorToolCallID = anchorToolCallID
		out[i].AnchorToolName = anchorToolName
	}
	appendUsage()
	return out
}

func gatewayACPProtocolUpdate(ev kernel.Event) *session.ProtocolUpdate {
	if ev.Protocol == nil {
		return nil
	}
	return session.ProtocolUpdateOf(&session.Event{Protocol: ev.Protocol})
}

func gatewayEventWithProtocolUpdateMeta(ev kernel.Event, update *session.ProtocolUpdate) kernel.Event {
	if update == nil || len(update.Meta) == 0 {
		return ev
	}
	ev.Meta = mergeTranscriptMeta(update.Meta, ev.Meta)
	return ev
}

func mergeTranscriptMeta(base map[string]any, overlay map[string]any) map[string]any {
	if len(base) == 0 {
		return cloneAnyMap(overlay)
	}
	out := cloneAnyMap(base)
	for key, value := range overlay {
		if baseMap, ok := out[key].(map[string]any); ok {
			if overlayMap, ok := value.(map[string]any); ok {
				out[key] = mergeTranscriptMeta(baseMap, overlayMap)
				continue
			}
		}
		out[key] = value
	}
	return out
}

func projectACPProtocolUpdateToTranscriptEvents(ev kernel.Event, update *session.ProtocolUpdate, scope ACPProjectionScope, scopeID string, occurredAt time.Time) ([]TranscriptEvent, bool) {
	if update == nil {
		return nil, false
	}
	switch strings.TrimSpace(update.SessionUpdate) {
	case string(session.ProtocolUpdateTypeUserMessage):
		if scope != ACPProjectionMain {
			return nil, true
		}
		if text := strings.TrimSpace(protocolTextContent(update.Content)); text != "" {
			return []TranscriptEvent{{
				Kind:          TranscriptEventNarrative,
				Scope:         scope,
				ScopeID:       scopeID,
				OccurredAt:    occurredAt,
				NarrativeKind: TranscriptNarrativeUser,
				Text:          text,
				Final:         true,
			}}, true
		}
		return nil, true
	case string(session.ProtocolUpdateTypeAgentMessage):
		if text := protocolTextContent(update.Content); text != "" {
			return []TranscriptEvent{{
				Kind:          TranscriptEventNarrative,
				Scope:         scope,
				ScopeID:       scopeID,
				Actor:         gatewayDisplayActor(ev, ""),
				OccurredAt:    occurredAt,
				NarrativeKind: TranscriptNarrativeAssistant,
				Text:          text,
				Final:         gatewayProtocolNarrativeFinal(ev),
			}}, true
		}
		return nil, true
	case string(session.ProtocolUpdateTypeAgentThought):
		if text := protocolTextContent(update.Content); text != "" {
			return []TranscriptEvent{{
				Kind:          TranscriptEventNarrative,
				Scope:         scope,
				ScopeID:       scopeID,
				Actor:         gatewayDisplayActor(ev, ""),
				OccurredAt:    occurredAt,
				NarrativeKind: TranscriptNarrativeReasoning,
				Text:          text,
				Final:         gatewayProtocolNarrativeFinal(ev),
			}}, true
		}
		return nil, true
	case string(session.ProtocolUpdateTypeToolCall):
		if protocolUpdateIsPlanTool(ev, update) {
			return nil, true
		}
		return []TranscriptEvent{projectACPProtocolToolCallEvent(ev, update, scope, scopeID, occurredAt)}, true
	case string(session.ProtocolUpdateTypeToolUpdate):
		if protocolUpdateIsPlanTool(ev, update) {
			return nil, true
		}
		event, ok := projectACPProtocolToolResultEvent(ev, update, scope, scopeID, occurredAt)
		if !ok {
			return nil, true
		}
		return []TranscriptEvent{event}, true
	case string(session.ProtocolUpdateTypePlan):
		entries := make([]PlanEntry, 0, len(update.Entries))
		for _, entry := range update.Entries {
			entries = append(entries, PlanEntry{Content: entry.Content, Status: entry.Status})
		}
		if len(entries) == 0 {
			return nil, true
		}
		return []TranscriptEvent{{
			Kind:        TranscriptEventPlan,
			Scope:       scope,
			ScopeID:     scopeID,
			OccurredAt:  occurredAt,
			PlanEntries: entries,
		}}, true
	default:
		return nil, false
	}
}

func gatewayProtocolNarrativeFinal(ev kernel.Event) bool {
	if ev.Narrative != nil {
		return ev.Narrative.Final
	}
	return false
}

func projectACPProtocolToolCallEvent(ev kernel.Event, update *session.ProtocolUpdate, scope ACPProjectionScope, scopeID string, occurredAt time.Time) TranscriptEvent {
	return projectTranscriptToolCall(transcriptToolProjection{
		Event:      ev,
		Scope:      scope,
		ScopeID:    scopeID,
		Actor:      gatewayDisplayActor(ev, ""),
		OccurredAt: occurredAt,
		CallID:     update.ToolCallID,
		ToolName:   protocolUpdateToolName(ev, update),
		ToolKind:   update.Kind,
		ToolTitle:  update.Title,
		Status:     update.Status,
		RawInput:   cloneAnyMap(update.RawInput),
	})
}

func projectACPProtocolToolResultEvent(ev kernel.Event, update *session.ProtocolUpdate, scope ACPProjectionScope, scopeID string, occurredAt time.Time) (TranscriptEvent, bool) {
	return projectTranscriptToolResult(transcriptToolProjection{
		Event:      ev,
		Scope:      scope,
		ScopeID:    scopeID,
		Actor:      gatewayDisplayActor(ev, ""),
		OccurredAt: occurredAt,
		CallID:     update.ToolCallID,
		ToolName:   protocolUpdateToolName(ev, update),
		ToolKind:   update.Kind,
		ToolTitle:  update.Title,
		Status:     update.Status,
		RawInput:   cloneAnyMap(update.RawInput),
		RawOutput:  cloneAnyMap(update.RawOutput),
		Content:    session.ProtocolToolCallContentOf(update),
		Error:      protocolUpdateToolError(update),
	}, string(kernel.ToolStatusRunning))
}

func protocolUpdateIsPlanTool(ev kernel.Event, update *session.ProtocolUpdate) bool {
	if update == nil {
		return false
	}
	for _, value := range []string{
		protocolUpdateToolName(ev, update),
		update.Kind,
		update.Title,
	} {
		if strings.EqualFold(strings.TrimSpace(value), "PLAN") {
			return true
		}
	}
	return false
}

func protocolUpdateToolName(ev kernel.Event, update *session.ProtocolUpdate) string {
	if update == nil {
		return ""
	}
	if name := kernel.EventMetaString(ev.Meta, "caelis", "runtime", "tool", "name"); name != "" {
		return name
	}
	if name := terminalInfoToolName(ev.Meta); name != "" {
		return name
	}
	if ev.Protocol != nil && ev.Protocol.ToolCall != nil {
		if name := strings.TrimSpace(ev.Protocol.ToolCall.Name); name != "" {
			return name
		}
	}
	return gatewayToolDisplayName("", update.Title, update.Kind)
}

func protocolUpdateToolError(update *session.ProtocolUpdate) bool {
	if update == nil {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(update.Status))
	if status == "failed" || status == "error" {
		return true
	}
	if value, ok := update.RawOutput["is_error"].(bool); ok && value {
		return true
	}
	if value, ok := update.RawOutput["error"].(string); ok && strings.TrimSpace(value) != "" && status != "completed" {
		return true
	}
	return false
}

func payloadNarrativeChunkHasContent(text string, final bool) bool {
	if text == "" {
		return false
	}
	if !final {
		return true
	}
	return strings.TrimSpace(text) != ""
}

func gatewayToolDisplayName(name string, title string, kind string) string {
	if name = strings.TrimSpace(name); name != "" {
		return name
	}
	if kind = strings.TrimSpace(kind); kind != "" {
		return kind
	}
	return strings.TrimSpace(title)
}

func gatewayEventFromACP(ev kernel.Event) bool {
	if ev.Origin == nil {
		return false
	}
	source := strings.ToLower(strings.TrimSpace(ev.Origin.Source))
	if source == "acp" || source == "acp_participant" || source == "acp_subagent" || strings.HasPrefix(source, "acp_") {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(ev.Origin.ParticipantKind), "acp")
}

func gatewayDisplayActor(ev kernel.Event, fallback string) string {
	if ev.Origin != nil {
		if actor := strings.TrimSpace(ev.Origin.Actor); actor != "" {
			return actor
		}
	}
	return strings.TrimSpace(fallback)
}

func transcriptToolStream(status string, isErr bool) string {
	if isErr || strings.EqualFold(strings.TrimSpace(status), "failed") {
		return "stderr"
	}
	return "stdout"
}

func transcriptToolStatusFinal(status string, isErr bool) bool {
	if isErr {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "completed", "failed", "interrupted", "cancelled", "canceled":
		return true
	default:
		return false
	}
}

func gatewayProtocolRawInput(ev kernel.Event, fallback map[string]any) map[string]any {
	if ev.Protocol != nil && ev.Protocol.Update != nil && len(ev.Protocol.Update.RawInput) > 0 {
		return cloneAnyMap(ev.Protocol.Update.RawInput)
	}
	if ev.Protocol != nil && ev.Protocol.ToolCall != nil && len(ev.Protocol.ToolCall.RawInput) > 0 {
		return cloneAnyMap(ev.Protocol.ToolCall.RawInput)
	}
	return cloneAnyMap(fallback)
}

func gatewayProtocolToolContent(ev kernel.Event, fallback []session.ProtocolToolCallContent) []session.ProtocolToolCallContent {
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
	if content := session.CloneProtocolToolCallContent(fallback); len(content) > 0 {
		return content
	}
	return nil
}

func standardToolOutput(status string, isErr bool) string {
	if isErr || strings.EqualFold(strings.TrimSpace(status), string(kernel.ToolStatusFailed)) {
		return "failed"
	}
	if transcriptToolStatusFinal(status, isErr) {
		return "completed"
	}
	return ""
}

func suppressToolResultOutput(toolName string, toolKind string, output string, synthetic bool, isErr bool) bool {
	if isErr {
		return false
	}
	if !isExplorationSummaryTool(toolName, toolKind) {
		return false
	}
	trimmed := strings.TrimSpace(output)
	return synthetic || strings.EqualFold(trimmed, "completed")
}

func isExplorationSummaryTool(toolName string, toolKind string) bool {
	switch strings.ToUpper(strings.TrimSpace(toolSemanticName(toolName, toolKind))) {
	case "READ", "LIST", "GLOB", "SEARCH", "RG", "FIND":
		return true
	default:
		return false
	}
}

func terminalFinalWithoutContent(toolName string, toolKind string, status string, isErr bool) bool {
	if !transcriptToolStatusFinal(status, isErr) {
		return false
	}
	if isErr || strings.EqualFold(strings.TrimSpace(status), string(kernel.ToolStatusFailed)) {
		return false
	}
	return isTerminalPanelToolKind(toolName, toolKind)
}

func terminalNoOutputPlaceholder(toolName string, toolKind string, rawOutput map[string]any, meta map[string]any, content []session.ProtocolToolCallContent, status string, isErr bool) bool {
	if !terminalFinalWithoutContent(toolName, toolKind, status, isErr) {
		return false
	}
	if terminalContentText(content) != "" {
		return false
	}
	if terminalRawOutputHasText(rawOutput) {
		return false
	}
	if terminalRuntimeOutputText(meta) != "" || terminalOutputMetaText(meta) != "" {
		return false
	}
	return len(content) == 0 || hasStandardTerminalContent(content)
}

func terminalRawOutputHasText(rawOutput map[string]any) bool {
	for _, key := range []string{"result", "output", "stdout", "stderr", "error", "latest_output", "output_preview", "final_message", "finalMessage", "text"} {
		if text := asString(rawOutput[key]); strings.TrimSpace(text) != "" {
			return true
		}
	}
	return false
}

func terminalToolOutputText(toolName string, toolKind string, rawOutput map[string]any, meta map[string]any, content []session.ProtocolToolCallContent, status string, isErr bool) string {
	if !isTerminalPanelToolKind(toolName, toolKind) && !strings.EqualFold(strings.TrimSpace(toolName), "TASK") {
		return ""
	}
	if text := terminalOutputMetaText(meta); text != "" {
		return text
	}
	if text := terminalRuntimeOutputText(meta); text != "" {
		return text
	}
	if text := terminalContentText(content); text != "" {
		return text
	}
	if !hasStandardTerminalContent(content) {
		return ""
	}
	name := strings.ToUpper(strings.TrimSpace(toolName))
	if name == "SPAWN" {
		if isErr || strings.EqualFold(strings.TrimSpace(status), string(kernel.ToolStatusFailed)) {
			return firstNonEmpty(asString(rawOutput["stderr"]), asString(rawOutput["error"]))
		}
		if transcriptToolStatusFinal(status, isErr) {
			return firstNonEmpty(asString(rawOutput["final_message"]), asString(rawOutput["finalMessage"]), asString(rawOutput["result"]), asString(rawOutput["output"]), asString(rawOutput["text"]))
		}
		return firstNonEmpty(asString(rawOutput["text"]), asString(rawOutput["stdout"]), asString(rawOutput["output_preview"]), asString(rawOutput["stderr"]))
	}
	if terminalTaskStillRunning(rawOutput, meta) {
		return firstNonEmpty(asString(rawOutput["latest_output"]), asString(rawOutput["output_preview"]))
	}
	if !transcriptToolStatusFinal(status, isErr) {
		return firstNonEmpty(asString(rawOutput["latest_output"]), asString(rawOutput["output_preview"]))
	}
	if text := firstNonEmpty(asString(rawOutput["result"]), asString(rawOutput["output"]), asString(rawOutput["stdout"]), asString(rawOutput["stderr"]), asString(rawOutput["error"])); text != "" {
		return text
	}
	return ""
}

func taskWaitControlResult(semanticName string, rawInput map[string]any, displayOutput map[string]any, meta map[string]any) bool {
	return strings.EqualFold(strings.TrimSpace(semanticName), "TASK") &&
		strings.EqualFold(toolDisplayTaskAction(rawInput, displayOutput, meta), "wait")
}

func terminalTaskStillRunning(rawOutput map[string]any, meta map[string]any) bool {
	if boolValue(rawOutput["running"]) {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(asString(rawOutput["state"])), "running") {
		return true
	}
	taskMeta := eventRuntimeTaskMeta(meta)
	if boolValue(taskMeta["running"]) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(asString(taskMeta["state"])), "running")
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func hasStandardTerminalContent(content []session.ProtocolToolCallContent) bool {
	for _, item := range content {
		if strings.EqualFold(strings.TrimSpace(item.Type), "terminal") && strings.TrimSpace(item.TerminalID) != "" {
			return true
		}
	}
	return false
}

func terminalContentText(content []session.ProtocolToolCallContent) string {
	var out strings.Builder
	for _, item := range content {
		if !strings.EqualFold(strings.TrimSpace(item.Type), "terminal") {
			continue
		}
		if text := protocolTextContent(item.Content); text != "" {
			appendTerminalContentText(&out, text)
		}
	}
	return out.String()
}

func appendTerminalContentText(out *strings.Builder, text string) {
	if out == nil || text == "" {
		return
	}
	if out.Len() > 0 && !strings.HasSuffix(out.String(), "\n") && !strings.HasPrefix(text, "\n") {
		out.WriteByte('\n')
	}
	out.WriteString(text)
}

func terminalOutputMetaText(meta map[string]any) string {
	output := terminalMetaSection(meta, "terminal_output")
	return asString(output["data"])
}

func terminalRuntimeOutputText(meta map[string]any) string {
	taskMeta := eventRuntimeTaskMeta(meta)
	for _, key := range []string{"output_text", "latest_output", "output_preview", "result", "output", "stdout", "stderr", "error", "final_message", "finalMessage", "text"} {
		if text := asString(taskMeta[key]); text != "" {
			return text
		}
	}
	return ""
}

func terminalInfoToolName(meta map[string]any) string {
	info := terminalMetaSection(meta, "terminal_info")
	return firstNonEmpty(asString(info["tool"]), asString(info["tool_name"]), asString(info["name"]))
}

func terminalMetaSection(meta map[string]any, key string) map[string]any {
	if len(meta) == 0 {
		return nil
	}
	section, _ := meta[key].(map[string]any)
	return section
}

func toolDisplayMetaOutput(toolName string, meta map[string]any) map[string]any {
	out := map[string]any{}
	toolMeta := eventRuntimeToolMeta(meta)
	taskMeta := eventRuntimeTaskMeta(meta)
	switch strings.ToUpper(strings.TrimSpace(toolName)) {
	case "RUN_COMMAND", "SPAWN", "TASK":
		if taskID := firstNonEmpty(asString(toolMeta["target_id"]), asString(taskMeta["task_id"])); taskID != "" {
			out["task_id"] = taskID
		}
		for _, key := range []string{"effective_yield_time_ms", "yield_time_ms_defaulted"} {
			if value, ok := toolMeta[key]; ok {
				out[key] = value
			}
		}
		if strings.EqualFold(toolName, "RUN_COMMAND") {
			break
		}
		for _, key := range []string{"agent", "agent_id", "handle", "mention", "prompt", "target_kind", "action", "input"} {
			if value, ok := toolMeta[key]; ok {
				out[key] = value
			}
			if value, ok := taskMeta[key]; ok {
				out[key] = value
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func eventRuntimeToolMeta(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	toolMeta, _ := runtimeMeta["tool"].(map[string]any)
	return toolMeta
}

func eventRuntimeTaskMeta(meta map[string]any) map[string]any {
	caelis, _ := meta["caelis"].(map[string]any)
	runtimeMeta, _ := caelis["runtime"].(map[string]any)
	taskMeta, _ := runtimeMeta["task"].(map[string]any)
	return taskMeta
}
