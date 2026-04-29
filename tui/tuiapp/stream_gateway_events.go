package tuiapp

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
	"github.com/OnslaughtSnail/caelis/tui/tuikit"
)

func (m *Model) handleGatewayEventEnvelope(env appgateway.EventEnvelope) (tea.Model, tea.Cmd) {
	if env.Err != nil {
		if isUserInterruptError(env.Err) {
			model, cmd := m.handleTaskResultMsg(TaskResultMsg{Err: env.Err, Interrupted: true})
			return model, cmd
		}
		model, cmd := m.handleTaskResultMsg(TaskResultMsg{Err: env.Err})
		return model, cmd
	}
	return m.handleTranscriptEventsMsg(TranscriptEventsMsg{Events: ProjectGatewayEventToTranscriptEvents(env.Event)})
}

func isUserInterruptError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	text := strings.ToLower(strings.TrimSpace(err.Error()))
	return text == "context canceled" || strings.Contains(text, "context canceled")
}

func (m *Model) appendGatewayTranscript(text string) (tea.Model, tea.Cmd) {
	text = strings.TrimSpace(text)
	if text == "" {
		return m, nil
	}
	m.finalizeAssistantBlock()
	m.finalizeReasoningBlock()
	block := NewTranscriptBlock(text, tuikit.DetectLineStyle(text))
	m.doc.Append(block)
	m.hasCommittedLine = true
	m.lastCommittedStyle = block.Style
	m.syncViewportContent()
	return m, nil
}

func gatewayEventScope(ev appgateway.Event) ACPProjectionScope {
	if ev.Origin == nil {
		return ACPProjectionMain
	}
	return gatewayProjectionScope(ev.Origin.Scope)
}

func gatewayProjectionScope(scope appgateway.EventScope) ACPProjectionScope {
	switch scope {
	case appgateway.EventScopeParticipant:
		return ACPProjectionParticipant
	case appgateway.EventScopeSubagent:
		return ACPProjectionSubagent
	default:
		return ACPProjectionMain
	}
}

func gatewayEventScopeID(ev appgateway.Event) string {
	if ev.Origin != nil && strings.TrimSpace(ev.Origin.ScopeID) != "" {
		return strings.TrimSpace(ev.Origin.ScopeID)
	}
	if sessionID := strings.TrimSpace(ev.SessionRef.SessionID); sessionID != "" {
		return sessionID
	}
	return strings.TrimSpace(ev.TurnID)
}

func gatewayParticipantID(ev appgateway.Event) string {
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

func gatewayUserText(ev appgateway.Event) string {
	if ev.Narrative != nil {
		return strings.TrimSpace(ev.Narrative.Text)
	}
	return ""
}

func gatewayNoticeText(ev appgateway.Event) string {
	if ev.Narrative != nil {
		return strings.TrimSpace(ev.Narrative.Text)
	}
	return ""
}

func gatewayApprovalSummary(ev appgateway.Event) (string, string) {
	if ev.ApprovalPayload != nil {
		return strings.TrimSpace(ev.ApprovalPayload.ToolName), strings.TrimSpace(ev.ApprovalPayload.CommandPreview)
	}
	return "", ""
}

func gatewayToolArgsMap(commandPreview string, argsText string) map[string]any {
	display := strings.TrimSpace(firstNonEmpty(commandPreview, argsText))
	if display == "" {
		return nil
	}
	return map[string]any{"_display": display}
}

func gatewayToolResultMap(output string, isErr bool) map[string]any {
	output = strings.TrimSpace(output)
	if output == "" {
		if isErr {
			return map[string]any{"summary": string(appgateway.ToolStatusFailed)}
		}
		return map[string]any{"summary": string(appgateway.ToolStatusCompleted)}
	}
	if isErr {
		return map[string]any{"error": output, "summary": output}
	}
	return map[string]any{"summary": output}
}
