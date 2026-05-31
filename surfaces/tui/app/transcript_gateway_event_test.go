package tuiapp

import (
	"strings"
	"time"

	"github.com/OnslaughtSnail/caelis/kernel"
	"github.com/OnslaughtSnail/caelis/ports/session"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

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
		usage := TranscriptUsageSnapshot{
			PromptTokens:      ev.Usage.PromptTokens,
			CachedInputTokens: ev.Usage.CachedInputTokens,
			CompletionTokens:  ev.Usage.CompletionTokens,
			ReasoningTokens:   ev.Usage.ReasoningTokens,
			TotalTokens:       ev.Usage.TotalTokens,
		}
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
			toolName := transcriptToolDisplayName(payload.ToolName, payload.ToolTitle, payload.ToolKind)
			if strings.EqualFold(strings.TrimSpace(toolName), "PLAN") {
				break
			}
			out = append(out, projectTranscriptToolCall(transcriptToolProjection{
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      gatewayDisplayActor(ev, payload.Actor),
				OccurredAt: occurredAt,
				Meta:       ev.Meta,
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
			toolName := transcriptToolDisplayName(payload.ToolName, payload.ToolTitle, payload.ToolKind)
			if strings.EqualFold(strings.TrimSpace(toolName), "PLAN") {
				break
			}
			event, ok := projectTranscriptToolResult(transcriptToolProjection{
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      gatewayDisplayActor(ev, payload.Actor),
				OccurredAt: occurredAt,
				Meta:       ev.Meta,
				CallID:     payload.CallID,
				ToolName:   toolName,
				ToolKind:   payload.ToolKind,
				ToolTitle:  payload.ToolTitle,
				Status:     string(payload.Status),
				RawInput:   gatewayProtocolRawInput(ev, payload.RawInput),
				RawOutput:  payload.RawOutput,
				Content:    gatewayProtocolToolContent(ev, payload.Content),
				Error:      payload.Error,
			}, transcriptToolStatusCompleted)
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

func gatewayEventScope(ev kernel.Event) ACPProjectionScope {
	if ev.Origin != nil && ev.Origin.Scope != "" {
		return gatewayProjectionScope(ev.Origin.Scope)
	}
	if ev.Narrative != nil && ev.Narrative.Scope != "" {
		return gatewayProjectionScope(ev.Narrative.Scope)
	}
	if ev.Participant != nil && ev.Participant.Scope != "" {
		return gatewayProjectionScope(ev.Participant.Scope)
	}
	return ACPProjectionMain
}

func gatewayProjectionScope(scope kernel.EventScope) ACPProjectionScope {
	switch scope {
	case kernel.EventScopeParticipant:
		return ACPProjectionParticipant
	case kernel.EventScopeSubagent:
		return ACPProjectionSubagent
	default:
		return ACPProjectionMain
	}
}

func gatewayEventScopeID(ev kernel.Event) string {
	if ev.Origin != nil && strings.TrimSpace(ev.Origin.ScopeID) != "" {
		return strings.TrimSpace(ev.Origin.ScopeID)
	}
	if sessionID := strings.TrimSpace(ev.SessionRef.SessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(ev.TurnID)
}

func gatewayParticipantID(ev kernel.Event) string {
	if ev.Origin != nil && strings.TrimSpace(ev.Origin.ParticipantID) != "" {
		return strings.TrimSpace(ev.Origin.ParticipantID)
	}
	switch {
	case ev.Narrative != nil:
		return strings.TrimSpace(ev.Narrative.ParticipantID)
	case ev.ToolCall != nil:
		return strings.TrimSpace(ev.ToolCall.ParticipantID)
	case ev.ToolResult != nil:
		return strings.TrimSpace(ev.ToolResult.ParticipantID)
	case ev.Participant != nil:
		return strings.TrimSpace(ev.Participant.ParticipantID)
	case ev.Lifecycle != nil:
		return strings.TrimSpace(ev.Lifecycle.ParticipantID)
	default:
		return ""
	}
}

func gatewayUserText(ev kernel.Event) string {
	if ev.Narrative != nil {
		return strings.TrimSpace(ev.Narrative.Text)
	}
	return ""
}

func gatewayNoticeText(ev kernel.Event) string {
	if ev.Narrative != nil {
		return strings.TrimSpace(ev.Narrative.Text)
	}
	return ""
}

func gatewayApprovalSummary(ev kernel.Event) (string, string) {
	if ev.ApprovalPayload != nil {
		return strings.TrimSpace(ev.ApprovalPayload.ToolName), approvalCommandPreview(ev.ApprovalPayload.RawInput)
	}
	if ev.Protocol != nil && ev.Protocol.Permission != nil {
		return strings.TrimSpace(ev.Protocol.Permission.ToolCall.Name), approvalCommandPreview(ev.Protocol.Permission.ToolCall.RawInput)
	}
	return "", ""
}

func gatewayApprovalReviewHintMsg(ev kernel.Event) (ApprovalReviewHintMsg, bool) {
	if ev.ApprovalPayload == nil || !isAutomaticApprovalEvent(ev.ApprovalPayload) {
		return ApprovalReviewHintMsg{}, false
	}
	switch ev.ApprovalPayload.ReviewStatus {
	case kernel.ApprovalReviewStatusInProgress:
		tool := firstNonEmpty(strings.TrimSpace(ev.ApprovalPayload.ToolName), "approval request")
		return ApprovalReviewHintMsg{Text: "Reviewing approval request: " + tool, Pending: true}, true
	case kernel.ApprovalReviewStatusApproved,
		kernel.ApprovalReviewStatusDenied,
		kernel.ApprovalReviewStatusTimedOut,
		kernel.ApprovalReviewStatusFailed:
		return ApprovalReviewHintMsg{}, true
	default:
		return ApprovalReviewHintMsg{}, false
	}
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
		Scope:      scope,
		ScopeID:    scopeID,
		Actor:      gatewayDisplayActor(ev, ""),
		OccurredAt: occurredAt,
		Meta:       ev.Meta,
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
		Scope:      scope,
		ScopeID:    scopeID,
		Actor:      gatewayDisplayActor(ev, ""),
		OccurredAt: occurredAt,
		Meta:       ev.Meta,
		CallID:     update.ToolCallID,
		ToolName:   protocolUpdateToolName(ev, update),
		ToolKind:   update.Kind,
		ToolTitle:  update.Title,
		Status:     update.Status,
		RawInput:   cloneAnyMap(update.RawInput),
		RawOutput:  cloneAnyMap(update.RawOutput),
		Content:    schemaToolContentFromProtocol(session.ProtocolToolCallContentOf(update)),
		Error:      protocolUpdateToolError(update),
	}, transcriptToolStatusRunning)
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
	return transcriptToolDisplayName("", update.Title, update.Kind)
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

func gatewayProtocolRawInput(ev kernel.Event, fallback map[string]any) map[string]any {
	if ev.Protocol != nil && ev.Protocol.Update != nil && len(ev.Protocol.Update.RawInput) > 0 {
		return cloneAnyMap(ev.Protocol.Update.RawInput)
	}
	if ev.Protocol != nil && ev.Protocol.ToolCall != nil && len(ev.Protocol.ToolCall.RawInput) > 0 {
		return cloneAnyMap(ev.Protocol.ToolCall.RawInput)
	}
	return cloneAnyMap(fallback)
}

func gatewayProtocolToolContent(ev kernel.Event, fallback []session.ProtocolToolCallContent) []schema.ToolCallContent {
	if ev.Protocol != nil && ev.Protocol.Update != nil {
		if content := session.ProtocolToolCallContentOf(ev.Protocol.Update); len(content) > 0 {
			return schemaToolContentFromProtocol(content)
		}
	}
	if ev.Protocol != nil && ev.Protocol.ToolCall != nil {
		if content := session.CloneProtocolToolCallContent(ev.Protocol.ToolCall.Content); len(content) > 0 {
			return schemaToolContentFromProtocol(content)
		}
	}
	if content := session.CloneProtocolToolCallContent(fallback); len(content) > 0 {
		return schemaToolContentFromProtocol(content)
	}
	return nil
}

func schemaToolContentFromProtocol(in []session.ProtocolToolCallContent) []schema.ToolCallContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]schema.ToolCallContent, 0, len(in))
	for _, item := range in {
		out = append(out, schema.ToolCallContent{
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
