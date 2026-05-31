package tuiapp

import (
	"strings"
	"time"

	coremodel "github.com/OnslaughtSnail/caelis/core/model"
	coresession "github.com/OnslaughtSnail/caelis/core/session"
	coretool "github.com/OnslaughtSnail/caelis/core/tool"
	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
)

func ProjectSessionEventEnvelopeToTranscriptEvents(env appviewmodel.SessionEventEnvelope) []TranscriptEvent {
	if env.Canonical != nil {
		return ProjectCoreSessionEventToTranscriptEvents(*env.Canonical)
	}
	if env.Transcript != nil {
		return projectViewModelTranscriptItem(*env.Transcript)
	}
	return nil
}

func ProjectCoreSessionEventToTranscriptEvents(event coresession.Event) []TranscriptEvent {
	if event.Type == "" {
		return nil
	}
	scope := coreSessionEventScope(event)
	scopeID := coreSessionEventScopeID(event)
	actor := coreSessionEventActor(event)
	occurredAt := event.Time
	meta := coreSessionEventMeta(event)
	out := make([]TranscriptEvent, 0, 4)

	switch event.Type {
	case coresession.EventUser:
		if scope != ACPProjectionMain {
			break
		}
		if text := strings.TrimSpace(coresession.EventText(event)); text != "" {
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
	case coresession.EventAssistant:
		final := coreSessionNarrativeFinal(event)
		if reasoning := coreSessionReasoningText(event); payloadNarrativeChunkHasContent(reasoning, final) {
			out = append(out, TranscriptEvent{
				Kind:          TranscriptEventNarrative,
				Scope:         scope,
				ScopeID:       scopeID,
				Actor:         actor,
				OccurredAt:    occurredAt,
				NarrativeKind: TranscriptNarrativeReasoning,
				Text:          reasoning,
				Final:         final,
			})
		}
		if text := coreSessionAssistantText(event); payloadNarrativeChunkHasContent(text, final) {
			out = append(out, TranscriptEvent{
				Kind:          TranscriptEventNarrative,
				Scope:         scope,
				ScopeID:       scopeID,
				Actor:         actor,
				OccurredAt:    occurredAt,
				NarrativeKind: TranscriptNarrativeAssistant,
				Text:          text,
				Final:         final,
			})
		}
	case coresession.EventSystem, coresession.EventNotice:
		if text := strings.TrimSpace(coresession.EventText(event)); text != "" {
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
	case coresession.EventToolCall:
		if tool := event.Tool; tool != nil {
			toolName := transcriptToolDisplayName(tool.Name, tool.Title, tool.Kind)
			if strings.EqualFold(strings.TrimSpace(toolName), "PLAN") {
				break
			}
			out = append(out, projectTranscriptToolCall(transcriptToolProjection{
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      actor,
				OccurredAt: occurredAt,
				Meta:       meta,
				CallID:     tool.ID,
				ToolName:   toolName,
				ToolKind:   tool.Kind,
				ToolTitle:  tool.Title,
				Status:     string(tool.Status),
				RawInput:   tool.Input,
				Actions:    appviewmodel.TranscriptActionsFromEvent(event),
			}))
		}
	case coresession.EventToolResult:
		if tool := event.Tool; tool != nil {
			toolName := transcriptToolDisplayName(tool.Name, tool.Title, tool.Kind)
			if strings.EqualFold(strings.TrimSpace(toolName), "PLAN") {
				break
			}
			projected, ok := projectTranscriptToolResult(transcriptToolProjection{
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      actor,
				OccurredAt: occurredAt,
				Meta:       meta,
				CallID:     tool.ID,
				ToolName:   toolName,
				ToolKind:   tool.Kind,
				ToolTitle:  tool.Title,
				Status:     string(tool.Status),
				RawInput:   tool.Input,
				RawOutput:  tool.Output,
				Content:    coreSessionToolContent(tool.Content),
				Error:      tool.Status == coresession.ToolFailed,
				Actions:    appviewmodel.TranscriptActionsFromEvent(event),
			}, transcriptToolStatusCompleted)
			if ok {
				out = append(out, projected)
			}
		}
	case coresession.EventApproval:
		if projected, ok := projectCoreApprovalReview(event, scope, scopeID, actor, occurredAt, meta); ok {
			out = append(out, projected)
		}
	case coresession.EventPlan:
		entries := make([]PlanEntry, 0, len(event.Plan))
		for _, entry := range event.Plan {
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
	case coresession.EventParticipant:
		if state := strings.TrimSpace(coreSessionMetaString(event.Meta, "action")); state != "" {
			out = append(out, TranscriptEvent{
				Kind:       TranscriptEventParticipant,
				Scope:      scope,
				ScopeID:    scopeID,
				OccurredAt: occurredAt,
				State:      state,
			})
		}
	case coresession.EventLifecycle:
		if len(coretool.RuntimeTaskMeta(meta)) > 0 || len(coresession.RuntimeControllerMeta(meta)) > 0 {
			break
		}
		if event.Lifecycle != nil {
			if state := strings.ToLower(strings.TrimSpace(string(event.Lifecycle.Status))); state != "" {
				out = append(out, TranscriptEvent{
					Kind:       TranscriptEventLifecycle,
					Scope:      scope,
					ScopeID:    scopeID,
					Actor:      actor,
					OccurredAt: occurredAt,
					State:      state,
				})
			}
		}
	}

	anchorToolCallID := coreSessionMetaString(meta, "caelis", "runtime", "stream", "parent_call_id")
	anchorToolName := coreSessionMetaString(meta, "caelis", "runtime", "stream", "parent_tool")
	for i := range out {
		out[i].AnchorToolCallID = anchorToolCallID
		out[i].AnchorToolName = anchorToolName
	}
	return out
}

func projectCoreApprovalReview(event coresession.Event, scope ACPProjectionScope, scopeID string, actor string, occurredAt time.Time, meta map[string]any) (TranscriptEvent, bool) {
	if event.Approval == nil {
		return TranscriptEvent{}, false
	}
	status := coreApprovalReviewStatus(event)
	if status == "" {
		return TranscriptEvent{}, false
	}
	risk := firstNonEmpty(
		coreSessionMetaString(meta, "approval_review", "risk_level"),
		coreSessionMetaString(meta, "caelis", "approval_review", "risk_level"),
	)
	auth := firstNonEmpty(
		coreSessionMetaString(meta, "approval_review", "user_authorization"),
		coreSessionMetaString(meta, "caelis", "approval_review", "user_authorization"),
	)
	text := coreApprovalReviewText(status, risk, auth, event.Approval.Reason, meta)
	tool := event.Approval.Tool
	if tool == nil {
		tool = event.Tool
	}
	return TranscriptEvent{
		Kind:            TranscriptEventApproval,
		Scope:           scope,
		ScopeID:         scopeID,
		Actor:           actor,
		OccurredAt:      occurredAt,
		ToolCallID:      coreApprovalToolID(tool),
		ApprovalTool:    coreApprovalToolName(tool),
		ApprovalCommand: coreApprovalToolCommand(tool),
		ApprovalStatus:  status,
		ApprovalRisk:    risk,
		ApprovalAuth:    auth,
		ApprovalText:    text,
		Final:           true,
	}, true
}

func coreApprovalReviewStatus(event coresession.Event) string {
	if event.Approval == nil {
		return ""
	}
	autoReview := strings.EqualFold(coreSessionMetaString(event.Meta, "usage_category"), "auto_review") ||
		coreSessionMetaString(event.Meta, "approval_review", "outcome") != "" ||
		coreSessionMetaString(event.Meta, "caelis", "approval_review", "outcome") != ""
	if !autoReview {
		return ""
	}
	switch event.Approval.Status {
	case coresession.ApprovalApproved:
		return "approved"
	case coresession.ApprovalRejected:
		return "denied"
	default:
		switch strings.ToLower(firstNonEmpty(
			coreSessionMetaString(event.Meta, "approval_review", "outcome"),
			coreSessionMetaString(event.Meta, "caelis", "approval_review", "outcome"),
		)) {
		case "allow", "approved":
			return "approved"
		case "deny", "denied", "reject", "rejected":
			return "denied"
		default:
			return ""
		}
	}
}

func coreApprovalReviewText(status string, risk string, auth string, reason string, meta map[string]any) string {
	text := "Automatic approval review " + strings.TrimSpace(status)
	parts := make([]string, 0, 2)
	if risk = strings.TrimSpace(risk); risk != "" {
		parts = append(parts, "risk: "+risk)
	}
	if auth = strings.TrimSpace(auth); auth != "" {
		parts = append(parts, "authorization: "+auth)
	}
	if len(parts) > 0 {
		text += " (" + strings.Join(parts, ", ") + ")"
	}
	rationale := firstNonEmpty(
		coreSessionMetaString(meta, "approval_review", "rationale"),
		coreSessionMetaString(meta, "caelis", "approval_review", "rationale"),
		strings.TrimSpace(reason),
	)
	if rationale != "" {
		text += ": " + rationale
	}
	return text
}

func coreApprovalToolID(tool *coresession.ToolEvent) string {
	if tool == nil {
		return ""
	}
	return strings.TrimSpace(tool.ID)
}

func coreApprovalToolName(tool *coresession.ToolEvent) string {
	if tool == nil {
		return ""
	}
	return strings.TrimSpace(tool.Name)
}

func coreApprovalToolCommand(tool *coresession.ToolEvent) string {
	if tool == nil {
		return ""
	}
	return approvalCommandPreview(tool.Input)
}

func projectViewModelTranscriptItem(item appviewmodel.TranscriptItem) []TranscriptEvent {
	text := strings.TrimSpace(item.Text)
	if text == "" && strings.TrimSpace(item.ToolName) == "" {
		return nil
	}
	scope := ACPProjectionMain
	scopeID := ""
	if strings.TrimSpace(item.Participant) != "" {
		scope = ACPProjectionParticipant
		scopeID = strings.TrimSpace(item.Participant)
	}
	switch strings.TrimSpace(item.Type) {
	case string(coresession.EventUser):
		if scope != ACPProjectionMain || text == "" {
			return nil
		}
		return []TranscriptEvent{{
			Kind:          TranscriptEventNarrative,
			Scope:         scope,
			ScopeID:       scopeID,
			OccurredAt:    item.Time,
			NarrativeKind: TranscriptNarrativeUser,
			Text:          text,
			Final:         true,
		}}
	case string(coresession.EventAssistant):
		if text == "" {
			return nil
		}
		return []TranscriptEvent{{
			Kind:          TranscriptEventNarrative,
			Scope:         scope,
			ScopeID:       scopeID,
			Actor:         strings.TrimSpace(item.Actor),
			OccurredAt:    item.Time,
			NarrativeKind: TranscriptNarrativeAssistant,
			Text:          text,
			Final:         true,
		}}
	case string(coresession.EventToolCall), string(coresession.EventToolResult):
		if strings.TrimSpace(item.ToolName) == "" && len(item.Actions) == 0 {
			return nil
		}
		return []TranscriptEvent{{
			Kind:        TranscriptEventTool,
			Scope:       scope,
			ScopeID:     scopeID,
			Actor:       strings.TrimSpace(item.Actor),
			OccurredAt:  item.Time,
			ToolName:    strings.TrimSpace(item.ToolName),
			ToolStatus:  strings.TrimSpace(item.ToolStatus),
			ToolActions: cloneTranscriptActions(item.Actions),
			Final:       true,
		}}
	default:
		return nil
	}
}

func coreSessionEventScope(event coresession.Event) ACPProjectionScope {
	if event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) == "" {
		return ACPProjectionMain
	}
	if event.Scope.Participant.Kind == coresession.ParticipantSubagent {
		return ACPProjectionSubagent
	}
	return ACPProjectionParticipant
}

func coreSessionEventScopeID(event coresession.Event) string {
	if event.Scope != nil {
		if id := strings.TrimSpace(event.Scope.Participant.ID); id != "" {
			return id
		}
	}
	if sessionID := strings.TrimSpace(event.SessionID); sessionID != "" {
		return sessionID
	}
	if event.Scope != nil {
		return strings.TrimSpace(event.Scope.TurnID)
	}
	return ""
}

func coreSessionEventActor(event coresession.Event) string {
	if event.Scope != nil && strings.TrimSpace(event.Scope.Participant.ID) != "" {
		participant := event.Scope.Participant
		return firstNonEmpty(participant.Label, participant.AgentName, participant.ID)
	}
	return firstNonEmpty(event.Actor.Name, event.Actor.ID, string(event.Actor.Kind))
}

func coreSessionNarrativeFinal(event coresession.Event) bool {
	return event.Visibility != coresession.VisibilityUIOnly && event.Visibility != coresession.VisibilityOverlay
}

func coreSessionAssistantText(event coresession.Event) string {
	if event.Message != nil {
		if text := event.Message.TextContent(); text != "" {
			return text
		}
		if coreMessageReasoningText(event.Message) != "" {
			return ""
		}
	}
	return strings.TrimSpace(coresession.EventText(event))
}

func coreSessionReasoningText(event coresession.Event) string {
	if event.Message != nil {
		if text := coreMessageReasoningText(event.Message); text != "" {
			return text
		}
	}
	if text := coreSessionMetaString(event.Meta, "caelis", "runtime", "replay", "reasoning_text"); text != "" {
		return text
	}
	return ""
}

func coreMessageReasoningText(message *coremodel.Message) string {
	if message == nil {
		return ""
	}
	parts := make([]string, 0, len(message.Parts))
	for _, part := range message.Parts {
		if part.Kind != coremodel.PartReasoning || part.Reasoning == nil {
			continue
		}
		switch part.Reasoning.Visibility {
		case coremodel.ReasoningHidden, coremodel.ReasoningRedacted, coremodel.ReasoningTokenOnly:
			continue
		}
		if text := part.Reasoning.VisibleText; text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func coreSessionEventMeta(event coresession.Event) map[string]any {
	if event.Tool == nil || len(event.Tool.Meta) == 0 {
		return cloneAnyMap(event.Meta)
	}
	return mergeTranscriptMeta(event.Meta, event.Tool.Meta)
}

func coreSessionToolContent(content []coresession.ToolContent) []schema.ToolCallContent {
	if len(content) == 0 {
		return nil
	}
	out := make([]schema.ToolCallContent, 0, len(content))
	for _, item := range content {
		contentType := strings.TrimSpace(item.Type)
		switch contentType {
		case "":
			if strings.TrimSpace(item.Text) != "" {
				contentType = "content"
			}
		case "text":
			contentType = "content"
		}
		var payload any
		if strings.TrimSpace(item.Text) != "" {
			payload = schema.TextContent{Type: "text", Text: item.Text}
		}
		out = append(out, schema.ToolCallContent{
			Type:       contentType,
			Content:    payload,
			TerminalID: strings.TrimSpace(item.TerminalID),
			Path:       strings.TrimSpace(item.Path),
		})
	}
	return out
}

func coreSessionMetaString(meta map[string]any, path ...string) string {
	if len(meta) == 0 || len(path) == 0 {
		return ""
	}
	var current any = meta
	for _, key := range path {
		mapped, ok := current.(map[string]any)
		if !ok {
			return ""
		}
		current = mapped[strings.TrimSpace(key)]
	}
	value, _ := current.(string)
	return strings.TrimSpace(value)
}
