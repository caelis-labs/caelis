package tuiapp

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/protocol/acp/schema"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/acpprojector"
)

func ProjectACPEventToTranscriptEvents(env eventstream.Envelope) []TranscriptEvent {
	scope := acpEventScope(env.Scope)
	scopeID := acpEventScopeID(env)
	occurredAt := env.OccurredAt
	meta := mergeTranscriptMeta(acpUpdateMeta(env.Update), env.Meta)
	anchorToolCallID := metaString(meta, "caelis", "runtime", "stream", "parent_call_id")
	anchorToolName := metaString(meta, "caelis", "runtime", "stream", "parent_tool")
	out := make([]TranscriptEvent, 0, 2)
	switch env.Kind {
	case eventstream.KindSessionUpdate:
		out = append(out, projectACPSessionUpdate(env, meta, scope, scopeID)...)
	case eventstream.KindNotice:
		if text := strings.TrimSpace(env.Notice); text != "" {
			out = append(out, TranscriptEvent{
				Kind:          TranscriptEventNotice,
				Scope:         scope,
				ScopeID:       scopeID,
				Actor:         strings.TrimSpace(env.Actor),
				OccurredAt:    occurredAt,
				NarrativeKind: TranscriptNarrativeNotice,
				Text:          text,
				Final:         true,
			})
		}
	case eventstream.KindParticipant:
		if env.Participant != nil && strings.TrimSpace(env.Participant.State) != "" {
			out = append(out, TranscriptEvent{
				Kind:       TranscriptEventParticipant,
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      strings.TrimSpace(env.Actor),
				OccurredAt: occurredAt,
				State:      strings.TrimSpace(env.Participant.State),
			})
		}
	case eventstream.KindLifecycle:
		if env.Lifecycle != nil && strings.TrimSpace(env.Lifecycle.State) != "" {
			out = append(out, TranscriptEvent{
				Kind:       TranscriptEventLifecycle,
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      strings.TrimSpace(env.Actor),
				OccurredAt: occurredAt,
				State:      strings.TrimSpace(env.Lifecycle.State),
			})
		}
	case eventstream.KindApprovalReview:
		if event, ok := projectACPApprovalReview(env, scope, scopeID); ok {
			out = append(out, event)
		}
	case eventstream.KindUsage:
		if env.Usage != nil {
			usage := *env.Usage
			out = append(out, TranscriptEvent{
				Kind:       TranscriptEventUsage,
				Scope:      scope,
				ScopeID:    scopeID,
				Actor:      strings.TrimSpace(env.Actor),
				OccurredAt: occurredAt,
				Usage:      &usage,
			})
		}
	}
	for i := range out {
		out[i].AnchorToolCallID = anchorToolCallID
		out[i].AnchorToolName = anchorToolName
	}
	return out
}

func projectACPSessionUpdate(env eventstream.Envelope, meta map[string]any, scope ACPProjectionScope, scopeID string) []TranscriptEvent {
	switch update := env.Update.(type) {
	case schema.ContentChunk:
		return projectACPContentChunk(env, update, scope, scopeID)
	case schema.ToolCall:
		if acpToolIsPlan(update.Title, update.Kind) {
			return nil
		}
		return []TranscriptEvent{projectTranscriptToolCall(transcriptToolProjection{
			Scope:      scope,
			ScopeID:    scopeID,
			Actor:      strings.TrimSpace(env.Actor),
			OccurredAt: env.OccurredAt,
			Meta:       meta,
			CallID:     update.ToolCallID,
			ToolName:   acpUpdateToolName(meta, update.Title, update.Kind),
			ToolKind:   update.Kind,
			ToolTitle:  update.Title,
			Status:     update.Status,
			RawInput:   acpRawMap(update.RawInput),
			Content:    acpToolContentToDisplay(update.Content),
		})}
	case schema.ToolCallUpdate:
		title := stringFromPtr(update.Title)
		kind := stringFromPtr(update.Kind)
		if acpToolIsPlan(title, kind) {
			return nil
		}
		event, ok := projectTranscriptToolResult(transcriptToolProjection{
			Scope:      scope,
			ScopeID:    scopeID,
			Actor:      strings.TrimSpace(env.Actor),
			OccurredAt: env.OccurredAt,
			Meta:       meta,
			CallID:     update.ToolCallID,
			ToolName:   acpUpdateToolName(meta, title, kind),
			ToolKind:   kind,
			ToolTitle:  title,
			Status:     stringFromPtr(update.Status),
			RawInput:   acpRawMap(update.RawInput),
			RawOutput:  acpRawMap(update.RawOutput),
			Content:    acpToolContentToDisplay(update.Content),
			Error:      acpToolUpdateError(update),
		}, "in_progress")
		if !ok {
			return nil
		}
		return []TranscriptEvent{event}
	case schema.PlanUpdate:
		entries := make([]PlanEntry, 0, len(update.Entries))
		for _, entry := range update.Entries {
			entries = append(entries, PlanEntry{Content: entry.Content, Status: entry.Status})
		}
		if len(entries) == 0 {
			return nil
		}
		return []TranscriptEvent{{
			Kind:        TranscriptEventPlan,
			Scope:       scope,
			ScopeID:     scopeID,
			Actor:       strings.TrimSpace(env.Actor),
			OccurredAt:  env.OccurredAt,
			PlanEntries: entries,
		}}
	default:
		return nil
	}
}

func projectACPContentChunk(env eventstream.Envelope, update schema.ContentChunk, scope ACPProjectionScope, scopeID string) []TranscriptEvent {
	text := protocolTextContent(update.Content)
	if text == "" {
		return nil
	}
	switch strings.TrimSpace(update.SessionUpdate) {
	case schema.UpdateUserMessage:
		if scope != ACPProjectionMain {
			return nil
		}
		return []TranscriptEvent{{
			Kind:          TranscriptEventNarrative,
			Scope:         scope,
			ScopeID:       scopeID,
			OccurredAt:    env.OccurredAt,
			NarrativeKind: TranscriptNarrativeUser,
			Text:          strings.TrimSpace(text),
			Final:         true,
		}}
	case schema.UpdateAgentMessage:
		return []TranscriptEvent{{
			Kind:          TranscriptEventNarrative,
			Scope:         scope,
			ScopeID:       scopeID,
			Actor:         strings.TrimSpace(env.Actor),
			OccurredAt:    env.OccurredAt,
			NarrativeKind: TranscriptNarrativeAssistant,
			Text:          text,
			Final:         env.Final,
		}}
	case schema.UpdateAgentThought:
		return []TranscriptEvent{{
			Kind:          TranscriptEventNarrative,
			Scope:         scope,
			ScopeID:       scopeID,
			Actor:         strings.TrimSpace(env.Actor),
			OccurredAt:    env.OccurredAt,
			NarrativeKind: TranscriptNarrativeReasoning,
			Text:          text,
			Final:         env.Final,
		}}
	default:
		return nil
	}
}

func projectACPApprovalReview(env eventstream.Envelope, scope ACPProjectionScope, scopeID string) (TranscriptEvent, bool) {
	if env.ApprovalReview == nil {
		return TranscriptEvent{}, false
	}
	text := acpApprovalReviewDisplayText(*env.ApprovalReview)
	if text == "" {
		return TranscriptEvent{}, false
	}
	return TranscriptEvent{
		Kind:            TranscriptEventApproval,
		Scope:           scope,
		ScopeID:         scopeID,
		Actor:           strings.TrimSpace(env.Actor),
		OccurredAt:      env.OccurredAt,
		ToolCallID:      strings.TrimSpace(env.ApprovalReview.ToolCallID),
		ApprovalTool:    strings.TrimSpace(env.ApprovalReview.ToolName),
		ApprovalCommand: approvalCommandPreview(env.ApprovalReview.RawInput),
		ApprovalStatus:  strings.TrimSpace(env.ApprovalReview.Status),
		ApprovalRisk:    firstNonEmpty(strings.TrimSpace(env.ApprovalReview.Risk), approvalReviewValueFromText(text, "risk")),
		ApprovalAuth:    firstNonEmpty(strings.TrimSpace(env.ApprovalReview.Authorization), approvalReviewValueFromText(text, "authorization")),
		ApprovalText:    text,
		Final:           true,
	}, true
}

func acpApprovalReviewDisplayText(review eventstream.ApprovalReview) string {
	switch strings.ToLower(strings.TrimSpace(review.Status)) {
	case "approved", "denied", "timed_out", "failed":
		return firstNonEmpty(strings.TrimSpace(review.Text), "Automatic approval review "+strings.TrimSpace(review.Status))
	default:
		return strings.TrimSpace(review.Text)
	}
}

func acpEventScope(scope eventstream.Scope) ACPProjectionScope {
	switch scope {
	case eventstream.ScopeParticipant:
		return ACPProjectionParticipant
	case eventstream.ScopeSubagent:
		return ACPProjectionSubagent
	default:
		return ACPProjectionMain
	}
}

func acpEventScopeID(env eventstream.Envelope) string {
	if scopeID := strings.TrimSpace(env.ScopeID); scopeID != "" {
		return scopeID
	}
	if sessionID := strings.TrimSpace(env.SessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(env.TurnID)
}

func acpUpdateToolName(meta map[string]any, title string, kind string) string {
	if name := metaString(meta, "caelis", "runtime", "tool", "name"); name != "" {
		return name
	}
	if name := terminalInfoToolName(meta); name != "" {
		return name
	}
	return transcriptToolDisplayName("", title, kind)
}

func acpUpdateMeta(update schema.Update) map[string]any {
	switch typed := update.(type) {
	case schema.ToolCall:
		return cloneAnyMap(typed.Meta)
	case schema.ToolCallUpdate:
		return cloneAnyMap(typed.Meta)
	default:
		return nil
	}
}

func acpToolIsPlan(values ...string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), "PLAN") {
			return true
		}
	}
	return false
}

func acpToolUpdateError(update schema.ToolCallUpdate) bool {
	status := strings.ToLower(strings.TrimSpace(stringFromPtr(update.Status)))
	if status == "failed" || status == "error" {
		return true
	}
	rawOutput := acpRawMap(update.RawOutput)
	if value, ok := rawOutput["is_error"].(bool); ok && value {
		return true
	}
	if value, ok := rawOutput["error"].(string); ok && strings.TrimSpace(value) != "" && status != "completed" {
		return true
	}
	return false
}

func acpRawMap(raw any) map[string]any {
	if raw == nil {
		return nil
	}
	if mapped, ok := raw.(map[string]any); ok {
		return cloneAnyMap(mapped)
	}
	return nil
}

func acpToolContentToDisplay(in []schema.ToolCallContent) []acpprojector.ToolContent {
	if len(in) == 0 {
		return nil
	}
	out := make([]acpprojector.ToolContent, 0, len(in))
	for _, item := range in {
		out = append(out, acpprojector.ToolContent{
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

func stringFromPtr(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func metaString(meta map[string]any, path ...string) string {
	if len(meta) == 0 {
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
