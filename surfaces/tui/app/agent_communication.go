package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func agentCommunicationSubagentEvent(event TranscriptEvent) SubagentEvent {
	return SubagentEvent{
		Kind:       SEAgentCommunication,
		Text:       strings.TrimSpace(tuikit.SanitizeLogText(event.Text)),
		StartedAt:  event.OccurredAt,
		EndedAt:    event.OccurredAt,
		SourceName: agentCommunicationDisplayIdentity(firstNonEmpty(event.AgentSourceName, event.Actor), event.AgentSourceID),
		SourceRole: agentCommunicationIdentityField(event.AgentSourceRole),
		SourceID:   agentCommunicationIdentityField(event.AgentSourceID),
	}
}

func (m *Model) mainAgentCommunicationEvent(event TranscriptEvent) SubagentEvent {
	projected := agentCommunicationSubagentEvent(event)
	callID, label := m.resolveAgentCommunicationSource(event)
	if label != "" {
		projected.SourceName = label
	}
	projected.SourceCallID = callID
	return projected
}

func agentCommunicationIdentityField(value string) string {
	return strings.Join(strings.Fields(tuikit.SanitizeLogText(value)), " ")
}

func agentCommunicationDisplayIdentity(name string, fallbackID string) string {
	name = agentCommunicationIdentityField(name)
	if open := strings.LastIndex(name, "("); open > 0 && strings.HasSuffix(name, ")") {
		agent := strings.TrimSpace(name[:open])
		handle := strings.TrimSpace(name[open+1 : len(name)-1])
		if agent != "" && handle != "" && !strings.ContainsAny(handle, " \t\r\n") {
			return handle + "[" + agent + "]"
		}
	}
	return firstNonEmpty(name, agentCommunicationIdentityField(fallbackID), "agent")
}

func (m *Model) resolveAgentCommunicationSource(event TranscriptEvent) (string, string) {
	if m == nil {
		return "", ""
	}
	sourceID := strings.TrimSpace(event.AgentSourceID)
	sourceName := agentCommunicationIdentityField(firstNonEmpty(event.AgentSourceName, event.Actor))
	matchedCallID := ""
	matchedLabel := ""
	for callID, view := range m.subagentOutputViews {
		if view == nil || !agentCommunicationSourceMatchesView(sourceID, sourceName, m.subagentRosterTasks[callID], view) {
			continue
		}
		callID = strings.TrimSpace(callID)
		if matchedCallID != "" && matchedCallID != callID {
			return "", ""
		}
		matchedCallID = callID
		matchedLabel = firstNonEmpty(
			agentCommunicationViewIdentity(view),
			agentCommunicationDisplayIdentity(sourceName, sourceID),
		)
	}
	return matchedCallID, matchedLabel
}

func agentCommunicationViewIdentity(view *subagentOutputView) string {
	if view == nil {
		return ""
	}
	handle, binding, _ := subagentRosterMetadata(view)
	actor := agentCommunicationIdentityField(subagentOutputActor("", view.title, view.taskHandle))
	if binding == "" && actor != "" && !strings.EqualFold(actor, handle) &&
		!strings.ContainsAny(actor, "[] \t\r\n") {
		binding = actor
	}
	switch {
	case handle != "" && binding != "":
		return handle + "[" + binding + "]"
	case actor != "":
		return actor
	default:
		return handle
	}
}

func agentCommunicationSourceMatchesView(sourceID string, sourceName string, descriptor taskstream.TaskDescriptor, view *subagentOutputView) bool {
	if view == nil {
		return false
	}
	if sourceID != "" {
		knownID := false
		for _, candidate := range []string{view.participantID, descriptor.ParticipantID} {
			candidate = strings.TrimSpace(candidate)
			if candidate == "" {
				continue
			}
			knownID = true
			if candidate == sourceID {
				return true
			}
		}
		if knownID {
			return false
		}
	}
	handle, binding, _ := subagentRosterMetadata(view)
	actor := agentCommunicationIdentityField(subagentOutputActor("", view.title, view.taskHandle))
	if binding == "" && actor != "" && !strings.EqualFold(actor, handle) &&
		!strings.ContainsAny(actor, "[] \t\r\n") {
		binding = actor
	}
	for _, candidate := range []string{
		view.actor,
		actor,
		binding + "(" + handle + ")",
		handle,
		"@" + handle,
	} {
		if sourceName != "" && strings.EqualFold(agentCommunicationIdentityField(candidate), sourceName) {
			return true
		}
	}
	return false
}

func renderAgentCommunicationRows(blockID string, event SubagentEvent, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []RenderedRow {
	text := strings.TrimSpace(event.Text)
	if text == "" {
		return nil
	}
	name := firstNonEmpty(event.SourceName, event.SourceID, "agent")
	detail, _ := longCommandDisplayPreview(name + ": " + text)
	header := "• Received " + detail
	if opts.AgentMessageTargetLinks {
		if token := subagentOutputOverlayClickToken(event.SourceCallID); token != "" {
			return []RenderedRow{renderACPTranscriptLinkedHeaderRow(blockID, header, ctx, token)}
		}
	}
	return []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, "")}
}
