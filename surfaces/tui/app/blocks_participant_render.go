package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

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

func renderParticipantTurnNarrativeEventRows(blockID string, ev SubagentEvent, lineStyle tuikit.LineStyle, width int, ctx BlockRenderContext, active bool) []RenderedRow {
	return renderParticipantTurnNarrativeRowsWithBuffer(blockID, ev.Text, ev.ActiveBuffer, lineStyle, width, ctx, active)
}

func renderParticipantTurnNarrativeRowsWithBuffer(blockID string, raw string, activeBuffer *activeNarrativeBuffer, lineStyle tuikit.LineStyle, width int, ctx BlockRenderContext, active bool) []RenderedRow {
	rolePrefix, continuationPrefix := narrativeLinePrefixes(lineStyle)
	if active && activeBuffer != nil && !activeBuffer.Empty() {
		rows := activeBuffer.RenderRowsAtWidth(blockID, rolePrefix, lineStyle, width, ctx)
		return alignParticipantNarrativeContinuationRows(rows, continuationPrefix)
	}
	mode := RenderFinal
	policy := MarkdownFull
	if active {
		mode = RenderStream
		policy = MarkdownStableTail
	}
	if lineStyle == tuikit.LineStyleReasoning {
		policy = MarkdownNone
	}
	rows := RenderTextWithContext(ctx, TextRenderRequest{
		Kind:           textKindForLineStyle(lineStyle),
		Mode:           mode,
		MarkdownPolicy: policy,
		Raw:            raw,
		Prefix:         rolePrefix,
		Width:          width,
		BlockID:        blockID,
		LineStyle:      lineStyle,
	}).Rows
	return alignParticipantNarrativeContinuationRows(rows, continuationPrefix)
}

func alignParticipantNarrativeContinuationRows(rows []RenderedRow, continuationPrefix string) []RenderedRow {
	if len(rows) <= 1 || continuationPrefix == "" {
		return rows
	}
	out := append([]RenderedRow(nil), rows...)
	seenContent := false
	for i := range out {
		if strings.TrimSpace(out[i].Plain) == "" {
			continue
		}
		if !seenContent {
			seenContent = true
			continue
		}
		if strings.HasPrefix(out[i].Plain, continuationPrefix) {
			continue
		}
		out[i].Plain = continuationPrefix + out[i].Plain
		out[i].Styled = continuationPrefix + out[i].Styled
	}
	return out
}

func normalizeNarrativeLineEndings(text string) string {
	return strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
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
	label := participantTurnFooterLabel(b)
	if label == "" {
		return ""
	}
	width := maxInt(12, ctx.Width)
	return ctx.Theme.HelpHintTextStyle().Render(centeredDivider(width, label))
}

func participantTurnHasFooter(b *ParticipantTurnBlock) bool {
	if participantTurnFooterLabel(b) == "" {
		return false
	}
	return strings.TrimSpace(b.Actor) != "" || len(b.Events) > 0
}

func participantTurnFooterLabel(b *ParticipantTurnBlock) string {
	if b == nil || !participantTurnIsTerminal(b.Status) || b.StartedAt.IsZero() || b.EndedAt.IsZero() || !b.EndedAt.After(b.StartedAt) {
		return ""
	}
	return formatTurnDuration(b.EndedAt.Sub(b.StartedAt))
}
