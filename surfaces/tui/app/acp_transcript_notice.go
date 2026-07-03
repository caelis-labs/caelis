package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/surfaces/transcript"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func renderACPNoticeRows(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext) []RenderedRow {
	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return nil
	}
	styleKind := tuikit.DetectLineStyle(text)
	styled := tuikit.ColorizeLogLine(text, styleKind, ctx.Theme)
	if styleKind == tuikit.LineStyleDefault {
		styled = ctx.Theme.TranscriptMetaStyle().Width(width).Render(text)
	}
	return []RenderedRow{StyledPlainRow(blockID, text, styled)}
}

func isCompactNoticeText(text string) bool {
	return strings.TrimSpace(text) == "• "+transcript.CompactNoticeLabel
}
