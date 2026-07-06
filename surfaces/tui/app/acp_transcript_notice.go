package tuiapp

import (
	"strings"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/surfaces/transcript"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func renderACPNoticeRows(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext) []RenderedRow {
	text := strings.TrimSpace(ev.Text)
	if text == "" {
		return nil
	}
	if ev.NoticeKind == transcript.NoticeKindModelRetry {
		return renderACPModelRetryNoticeRows(blockID, text, ctx)
	}
	if isCompactNoticeText(text) {
		styled := ctx.Theme.TranscriptMetaStyle().Width(width).Render(text)
		return []RenderedRow{StyledPlainRow(blockID, text, styled)}
	}
	styleKind := tuikit.DetectLineStyle(text)
	styled := tuikit.ColorizeLogLine(text, styleKind, ctx.Theme)
	if styleKind == tuikit.LineStyleDefault {
		styled = ctx.Theme.TranscriptMetaStyle().Width(width).Render(text)
	}
	return []RenderedRow{StyledPlainRow(blockID, text, styled)}
}

func renderACPModelRetryNoticeRows(blockID string, text string, ctx BlockRenderContext) []RenderedRow {
	content := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "!"))
	if content == "" {
		return nil
	}
	plain := "! " + content
	styled := ctx.Theme.WarnStyle().Bold(true).Render("! ") + renderNoticeTextWithNumberContrast(content, ctx)
	return []RenderedRow{StyledPlainRow(blockID, plain, styled)}
}

func renderNoticeTextWithNumberContrast(text string, ctx BlockRenderContext) string {
	var out strings.Builder
	textStyle := ctx.Theme.TranscriptMetaStyle()
	numberStyle := ctx.Theme.TextStyle().Bold(true)
	for i := 0; i < len(text); {
		r, size := utf8.DecodeRuneInString(text[i:])
		if isASCIIDigit(r) {
			start := i
			i += size
			for i < len(text) {
				next, nextSize := utf8.DecodeRuneInString(text[i:])
				if !isASCIIDigit(next) {
					break
				}
				i += nextSize
			}
			out.WriteString(numberStyle.Render(text[start:i]))
			continue
		}
		start := i
		i += size
		for i < len(text) {
			next, nextSize := utf8.DecodeRuneInString(text[i:])
			if isASCIIDigit(next) {
				break
			}
			i += nextSize
		}
		out.WriteString(textStyle.Render(text[start:i]))
	}
	return out.String()
}

func isASCIIDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

func isCompactNoticeText(text string) bool {
	return strings.TrimSpace(text) == "• "+transcript.CompactNoticeLabel
}
