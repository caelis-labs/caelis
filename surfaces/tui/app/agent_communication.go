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
	foldToken := ""
	if folded {
		key := agentCommunicationFoldKey(event, eventIndex)
		foldToken = agentMessageFoldClickToken(key)
		if opts.AgentMessageExpanded != nil && opts.AgentMessageExpanded(key) {
			displayText = text
		}
	}
	linkToken := ""
	if opts.AgentMessageTargetLinks {
		linkToken = subagentOutputOverlayClickToken(event.SourceCallID)
	}
	// The source label opens the Agent workspace; every other column keeps the
	// row's own expansion, so a long incoming message carries both actions even
	// when a narrow pane splits the label across lines.
	return wrapAgentMessageRows(
		bindAgentMessageTargets(renderAgentMessageRow(blockID, name, displayText, ctx, foldToken), agentMessageGutterColumns+displayColumns(name), linkToken),
		width,
	)
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

// agentMessageGutterColumns is the two-column bullet/indent gutter that precedes
// every Agent message line, on its label line and on each continuation line.
const agentMessageGutterColumns = 2

// wrapAgentMessageRows keeps complete input readable with a two-column gutter.
// A bounded label span is projected onto every physical line the label covers,
// so a label that wraps in a narrow pane keeps its target on each of its lines;
// lines past the label keep only the row's own body action.
func wrapAgentMessageRows(row RenderedRow, width int) []RenderedRow {
	// Wrap the content after the bullet, keeping ANSI styles and explicit newlines.
	mark, body, _ := strings.Cut(row.Styled, " ")
	prefix := mark + " "
	indentColumns := agentMessageGutterColumns
	lines := splitStyledPhysicalLines(ansi.Wrap(body, maxInt(1, width-2), ""))
	labelEnd := -1
	if row.boundedClick() {
		labelEnd = maxInt(0, row.ClickEndCol-indentColumns)
	}
	rows := make([]RenderedRow, 0, len(lines))
	remaining := ansi.Strip(body)
	offset := 0
	for i, line := range lines {
		indent := "  "
		if i == 0 {
			indent = prefix
		}
		text := ansi.Strip(line)
		if i > 0 {
			// ansi.Wrap removes the character it breaks on; skipping it keeps the
			// projected source offset aligned with the body columns.
			if skip := strings.Index(remaining, text); skip > 0 {
				offset += displayColumns(remaining[:skip])
				remaining = remaining[skip:]
			}
		}
		lineStart := offset
		lineEnd := offset + displayColumns(text)
		remaining = remaining[minInt(len(text), len(remaining)):]
		offset = lineEnd
		next := StyledPlainClickableRow(row.BlockID, ansi.Strip(indent+line), indent+line, row.ClickToken)
		if start, end := maxInt(lineStart, 0), minInt(lineEnd, labelEnd); labelEnd >= 0 && end > start {
			next.ClickStartCol = indentColumns + start - lineStart
			next.ClickEndCol = indentColumns + end - lineStart
			next.ClickTokenAlt = row.ClickTokenAlt
		} else if labelEnd >= 0 {
			// Past the label: only the row's own body action applies.
			next.ClickToken = firstNonEmpty(strings.TrimSpace(row.ClickTokenAlt), row.ClickToken)
		}
		next.PreWrapped = true
		next.selectionIndent = 2
		rows = append(rows, next)
	}
	return rows
}

// bindAgentMessageTargets gives one Agent-message row its click targets: the
// peer label in [0, labelEnd) opens the peer's workspace while the row's own
// body action owns the rest of the line. A row that only has navigation keeps
// it as the whole-row target.
func bindAgentMessageTargets(row RenderedRow, labelEnd int, linkToken string) RenderedRow {
	bodyToken := strings.TrimSpace(row.ClickToken)
	linkToken = strings.TrimSpace(linkToken)
	if linkToken == "" {
		return row
	}
	row.ClickToken = linkToken
	if bodyToken == "" || labelEnd <= 0 {
		return row
	}
	row.ClickStartCol = agentMessageGutterColumns
	row.ClickEndCol = labelEnd
	row.ClickTokenAlt = bodyToken
	return row
}
