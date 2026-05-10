package tuiapp

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
)

func applyUserNarrativeSurface(rows []RenderedRow, ctx BlockRenderContext) []RenderedRow {
	if len(rows) == 0 {
		return rows
	}
	out := append([]RenderedRow(nil), rows...)
	style := ctx.Theme.UserStyle()
	for i := range out {
		if strings.TrimSpace(out[i].Plain) == "" {
			continue
		}
		out[i].Styled = style.Width(maxInt(1, ctx.Width)).Render(out[i].Styled)
		out[i].PreWrapped = true
	}
	return out
}

func renderPlainReasoningRows(blockID, raw, rolePrefix string, width int, theme tuikit.Theme) []RenderedRow {
	raw = strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if width <= 0 {
		width = 1
	}
	prefixWidth := displayColumns(rolePrefix)
	bodyWidth := maxInt(1, width-prefixWidth)
	prefixStyled := ""
	if rolePrefix != "" {
		prefixStyled = tuikit.ColorizeLogLine(rolePrefix, tuikit.LineStyleReasoning, theme)
	}
	rows := make([]RenderedRow, 0, strings.Count(raw, "\n")+1)
	first := true
	for _, line := range strings.Split(raw, "\n") {
		segments := strings.Split(hardWrapDisplayLine(line, bodyWidth), "\n")
		if len(segments) == 0 {
			segments = []string{line}
		}
		for _, segment := range segments {
			plain := segment
			styled := segment
			if first {
				plain = rolePrefix + segment
				styled = prefixStyled + segment
				first = false
			}
			rows = append(rows, RenderedRow{
				Styled:     styled,
				Plain:      plain,
				BlockID:    blockID,
				PreWrapped: true,
			})
		}
	}
	return rows
}
