package tuiapp

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

func renderParticipantTurnHeader(b *ParticipantTurnBlock, ctx BlockRenderContext) string {
	if b == nil {
		return ""
	}
	icon := "▾"
	if !b.Expanded {
		icon = "▸"
	}
	iconText := ctx.Theme.PromptStyle().Bold(true).Render(icon)
	actor := renderParticipantActorLabel(ctx.Theme, b.Actor)
	left := iconText + " " + actor
	switch strings.ToLower(strings.TrimSpace(b.Status)) {
	case "waiting_approval":
		left = ctx.Theme.WarnStyle().Bold(true).Render(icon) + " " + actor
	case "failed":
		left = ctx.Theme.ErrorStyle().Bold(true).Render(icon) + " " + actor
	case "interrupted":
		left = ctx.Theme.WarnStyle().Bold(true).Render(icon) + " " + actor
	}
	metaParts := make([]string, 0, 1)
	if label := participantTurnStatusLabel(b.Status); label != "" {
		metaParts = append(metaParts, label)
	}
	if len(metaParts) == 0 {
		return left
	}
	return left + " " + ctx.Theme.TranscriptMetaStyle().Render("· "+strings.Join(metaParts, " · "))
}

func participantTurnStatusLabel(state string) string {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "", "running", "initializing", "prompting", "completed":
		return ""
	case "waiting_approval":
		return "waiting approval"
	case "failed":
		return "failed"
	case "interrupted":
		return "interrupted"
	default:
		return strings.TrimSpace(state)
	}
}

func participantNarrativeEventActive(events []SubagentEvent, idx int, status string) bool {
	return narrativeEventActive(events, idx, participantTurnIsTerminal(status))
}

func renderParticipantTurnNarrativeRows(blockID string, raw string, lineStyle tuikit.LineStyle, width int, ctx BlockRenderContext, active bool) []RenderedRow {
	rolePrefix, _ := narrativeLinePrefixes(lineStyle)
	mode := RenderFinal
	policy := MarkdownFull
	if active {
		mode = RenderStream
		policy = MarkdownStableTail
	}
	if lineStyle == tuikit.LineStyleReasoning {
		policy = MarkdownNone
	}
	return RenderTextWithContext(ctx, TextRenderRequest{
		Kind:           textKindForLineStyle(lineStyle),
		Mode:           mode,
		MarkdownPolicy: policy,
		Raw:            raw,
		Prefix:         rolePrefix,
		Width:          width,
		BlockID:        blockID,
		LineStyle:      lineStyle,
	}).Rows
}

func renderParticipantTurnToolRows(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext) []RenderedRow {
	rows := renderToolEventViewModelLines(blockID, buildToolEventViewModel(ev), width, ctx.Theme)
	return appendTranscriptActionRows(rows, blockID, ev.Actions, width, ctx)
}

func collapseRepeatedNarrativeText(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	if strings.TrimSpace(text) == "" {
		return text
	}
	parts := strings.Split(text, "\n\n")
	filteredParts := make([]string, 0, len(parts))
	lastPart := ""
	for _, part := range parts {
		part = collapseAdjacentDuplicateLines(part)
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		if trimmed == lastPart && len([]rune(trimmed)) >= 16 {
			continue
		}
		filteredParts = append(filteredParts, part)
		lastPart = trimmed
	}
	if len(filteredParts) == 0 {
		return ""
	}
	return strings.Join(filteredParts, "\n\n")
}

func collapseAdjacentDuplicateLines(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	last := ""
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && trimmed == last && len([]rune(trimmed)) >= 16 {
			continue
		}
		out = append(out, line)
		if trimmed != "" {
			last = trimmed
		}
	}
	return strings.Join(out, "\n")
}

func latestNarrativeAppendTargetIndex(events []SubagentEvent, kind SubagentEventKind) int {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind == kind {
			return i
		}
		if narrativeStreamBarrier(ev) {
			return -1
		}
	}
	return -1
}

func latestNarrativeFinalTargetIndex(events []SubagentEvent, kind SubagentEventKind) int {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind == kind {
			return i
		}
		if narrativeFinalBarrier(ev) {
			return -1
		}
	}
	return -1
}

func pruneNarrativeEventsCoveredByFinal(events []SubagentEvent, finalIdx int, kind SubagentEventKind) []SubagentEvent {
	if finalIdx <= 0 || finalIdx >= len(events) {
		return events
	}
	finalText := narrativeCoverageText(events[finalIdx].Text)
	if finalText == "" {
		return events
	}
	remove := make(map[int]struct{})
	cursor := 0
	for i := 0; i < finalIdx; i++ {
		if events[i].Kind != kind {
			continue
		}
		text := narrativeCoverageText(events[i].Text)
		if text == "" {
			continue
		}
		pos := strings.Index(finalText[cursor:], text)
		if pos < 0 {
			return events
		}
		cursor += pos + len(text)
		remove[i] = struct{}{}
	}
	if len(remove) == 0 {
		return events
	}
	out := events[:0]
	for i, ev := range events {
		if _, ok := remove[i]; ok {
			continue
		}
		out = append(out, ev)
	}
	return out
}

func cumulativeFinalNarrativeAlreadyRendered(events []SubagentEvent, kind SubagentEventKind, finalText string) bool {
	if strings.TrimSpace(finalText) == "" {
		return false
	}
	cursor := 0
	matched := false
	for _, ev := range events {
		if ev.Kind != kind {
			continue
		}
		text := ev.Text
		if strings.TrimSpace(text) == "" {
			continue
		}
		pos := strings.Index(finalText[cursor:], text)
		if pos < 0 {
			return false
		}
		if strings.TrimSpace(finalText[cursor:cursor+pos]) != "" {
			return false
		}
		cursor += pos + len(text)
		matched = true
	}
	if !matched {
		return false
	}
	return strings.TrimSpace(finalText[cursor:]) == ""
}

func cumulativeFinalNarrativeTimelineText(events []SubagentEvent, kind SubagentEventKind, finalText string, targetIdx int) string {
	if strings.TrimSpace(finalText) == "" || targetIdx <= 0 {
		return finalText
	}
	if targetIdx > len(events) {
		targetIdx = len(events)
	}
	cursor := 0
	matched := false
	for i := 0; i < targetIdx; i++ {
		ev := events[i]
		if ev.Kind != kind {
			continue
		}
		text := ev.Text
		if strings.TrimSpace(text) == "" {
			continue
		}
		pos := strings.Index(finalText[cursor:], text)
		if pos < 0 {
			return finalText
		}
		if strings.TrimSpace(finalText[cursor:cursor+pos]) != "" {
			return finalText
		}
		cursor += pos + len(text)
		matched = true
	}
	if !matched {
		return finalText
	}
	return strings.TrimLeft(finalText[cursor:], " \t\r\n")
}

func narrativeCoverageText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func narrativeStreamBarrier(ev SubagentEvent) bool {
	switch ev.Kind {
	case SEApproval, SEAssistant, SEReasoning:
		return false
	default:
		return true
	}
}

func narrativeFinalBarrier(ev SubagentEvent) bool {
	switch ev.Kind {
	case SEApproval, SEAssistant, SEReasoning:
		return false
	default:
		return true
	}
}

func participantTurnIsTerminal(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "completed", "failed", "interrupted", "cancelled", "canceled", "terminated":
		return true
	default:
		return false
	}
}

func renderParticipantTurnFooter(b *ParticipantTurnBlock, ctx BlockRenderContext) string {
	label := ""
	if b != nil && !b.StartedAt.IsZero() && !b.EndedAt.IsZero() && b.EndedAt.After(b.StartedAt) {
		label = formatTurnDuration(b.EndedAt.Sub(b.StartedAt))
	}
	if label == "" {
		return ""
	}
	width := maxInt(12, ctx.Width)
	return ctx.Theme.HelpHintTextStyle().Render(centeredDivider(width, label))
}
