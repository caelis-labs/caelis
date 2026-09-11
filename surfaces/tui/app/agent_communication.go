package tuiapp

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/appserver/taskstream"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func agentCommunicationSubagentEvent(event TranscriptEvent) SubagentEvent {
	return SubagentEvent{
		Kind:               SEAgentCommunication,
		Text:               strings.TrimSpace(tuikit.SanitizeLogText(event.Text)),
		StartedAt:          event.OccurredAt,
		EndedAt:            event.OccurredAt,
		SourceName:         agentCommunicationDisplayIdentity(firstNonEmpty(event.AgentSourceName, event.Actor), event.AgentSourceID),
		SourceRole:         agentCommunicationIdentityField(event.AgentSourceRole),
		SourceID:           agentCommunicationIdentityField(event.AgentSourceID),
		SourceEventID:      strings.TrimSpace(event.SourceEventID),
		SourceProjectionID: strings.TrimSpace(event.SourceProjectionID),
		MessageID:          strings.TrimSpace(event.MessageID),
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
	handle, binding := subagentRosterMetadata(view)
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
	handle, binding := subagentRosterMetadata(view)
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

func renderAgentCommunicationRows(blockID string, event SubagentEvent, eventIndex int, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) []RenderedRow {
	text := strings.TrimSpace(event.Text)
	if text == "" {
		return nil
	}
	name := firstNonEmpty(event.SourceName, event.SourceID, "agent")
	if opts.FullAgentMessages {
		return wrapAgentMessageRows(renderAgentMessageRow(blockID, name, text, ctx, ""), width)
	}
	bodyBudget := maxInt(1, compactSingleLineBudget(width)-displayColumns("• "+name+": "))
	displayText, folded := longCommandDisplayPreview(text, bodyBudget)
	if opts.AgentMessageTargetLinks {
		if token := subagentOutputOverlayClickToken(event.SourceCallID); token != "" {
			return []RenderedRow{renderAgentMessageRow(blockID, name, displayText, ctx, token)}
		}
	}
	token := ""
	if folded {
		key := agentCommunicationFoldKey(event, eventIndex)
		token = agentMessageFoldClickToken(key)
		if opts.AgentMessageExpanded != nil && opts.AgentMessageExpanded(key) {
			displayText = text
		}
	}
	return []RenderedRow{renderAgentMessageRow(blockID, name, displayText, ctx, token)}
}

func agentCommunicationFoldKey(event SubagentEvent, eventIndex int) string {
	if value := strings.TrimSpace(event.MessageID); value != "" {
		return "message:" + value
	}
	if value := strings.TrimSpace(event.SourceProjectionID); value != "" {
		return "projection:" + value
	}
	if value := strings.TrimSpace(event.SourceEventID); value != "" {
		return "event:" + value
	}
	return fmt.Sprintf("%d:%d:%s", eventIndex, event.StartedAt.UnixNano(), strings.TrimSpace(event.SourceID))
}

func renderAgentMessageRow(blockID string, name string, text string, ctx BlockRenderContext, token string) RenderedRow {
	name = strings.TrimSpace(name)
	text = sanitizeRenderableText(text)
	plain := "• " + name
	styled := renderACPTranscriptHeaderMark(ctx, acpHeaderMarkDefault, false) + " " + styleAgentMessageTarget(ctx, name, false)
	if text != "" {
		plain += ": " + text
		styled += ctx.Theme.TextStyle().Render(": " + text)
	}
	row := StyledPlainClickableRow(blockID, plain, styled, token)
	row.selectionIndent = 2
	return row
}

// styleAgentMessageTarget styles the display label, never inferring direction
// from message text. Binding annotations retain their secondary emphasis.
func styleAgentMessageTarget(ctx BlockRenderContext, target string, sent bool) string {
	handle, annotation := target, ""
	if before, after, ok := strings.Cut(target, "["); ok && strings.HasSuffix(after, "]") {
		handle, annotation = before, "["+after
	}
	style := ctx.Theme.AgentMessageReceivedStyle()
	if sent {
		style = ctx.Theme.AgentMessageSentStyle()
	}
	styled := style.Render(handle)
	if annotation != "" {
		styled += ctx.Theme.SecondaryTextStyle().Render(annotation)
	}
	return styled
}

// wrapAgentMessageRows keeps complete input readable with a two-column gutter.
func wrapAgentMessageRows(row RenderedRow, width int) []RenderedRow {
	// Wrap the content after the bullet, keeping ANSI styles and explicit newlines.
	mark, body, _ := strings.Cut(row.Styled, " ")
	prefix := mark + " "
	lines := strings.Split(ansi.Wrap(body, maxInt(1, width-2), ""), "\n")
	rows := make([]RenderedRow, 0, len(lines))
	for i, line := range lines {
		indent := "  "
		if i == 0 {
			indent = prefix
		}
		next := StyledPlainClickableRow(row.BlockID, ansi.Strip(indent+line), indent+line, row.ClickToken)
		next.PreWrapped = true
		next.selectionIndent = 2
		rows = append(rows, next)
	}
	return rows
}
