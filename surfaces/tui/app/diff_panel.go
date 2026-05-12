package tuiapp

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

type diffPanelLineKind int

const (
	diffPanelLineMeta diffPanelLineKind = iota
	diffPanelLineHunk
	diffPanelLineContext
	diffPanelLineAdd
	diffPanelLineRemove
)

type diffPanelLine struct {
	Kind   diffPanelLineKind
	OldNo  int
	NewNo  int
	Marker byte
	Text   string
}

type diffPanelModel struct {
	Lines  []diffPanelLine
	MaxOld int
	MaxNew int
}

func renderNumberedACPDiffPanelRows(blockID string, text string, width int, ctx BlockRenderContext) []RenderedRow {
	model := parseDiffPanelText(text)
	if len(model.Lines) == 0 {
		return nil
	}
	rendered := renderNumberedACPDiffPanelBody(model, maxInt(1, width), ctx)
	rows := make([]RenderedRow, 0, len(rendered))
	for _, row := range rendered {
		rows = append(rows, RenderedRow{
			Styled:     row.Styled,
			Plain:      row.Plain,
			BlockID:    blockID,
			PreWrapped: true,
		})
	}
	return rows
}

type renderedDiffPanelRow struct {
	Plain  string
	Styled string
}

func parseDiffPanelText(text string) diffPanelModel {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	rawLines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	model := diffPanelModel{Lines: make([]diffPanelLine, 0, len(rawLines))}
	oldNo, newNo := 0, 0
	inHunk := false
	for _, raw := range rawLines {
		trimmed := strings.TrimSpace(raw)
		switch {
		case trimmed == "":
			model.Lines = append(model.Lines, diffPanelLine{Kind: diffPanelLineMeta})
			continue
		case strings.EqualFold(trimmed, "diff / hunk"):
			model.Lines = append(model.Lines, diffPanelLine{Kind: diffPanelLineMeta, Text: trimmed})
			continue
		case strings.HasPrefix(trimmed, "@@"):
			oldStart, oldCount, newStart, newCount, ok := parseDiffHunkHeader(trimmed)
			if ok {
				oldNo = oldStart
				newNo = newStart
				inHunk = true
				model.MaxOld = maxInt(model.MaxOld, lastDiffRangeLine(oldStart, oldCount))
				model.MaxNew = maxInt(model.MaxNew, lastDiffRangeLine(newStart, newCount))
			}
			model.Lines = append(model.Lines, diffPanelLine{Kind: diffPanelLineHunk, Text: trimmed})
			continue
		}
		if !inHunk || raw == "" {
			model.Lines = append(model.Lines, diffPanelLine{Kind: diffPanelLineContext, Marker: ' ', Text: raw})
			continue
		}
		marker := raw[0]
		body := raw[1:]
		switch marker {
		case '+':
			model.Lines = append(model.Lines, diffPanelLine{Kind: diffPanelLineAdd, NewNo: newNo, Marker: marker, Text: body})
			model.MaxNew = maxInt(model.MaxNew, newNo)
			newNo++
		case '-':
			model.Lines = append(model.Lines, diffPanelLine{Kind: diffPanelLineRemove, OldNo: oldNo, Marker: marker, Text: body})
			model.MaxOld = maxInt(model.MaxOld, oldNo)
			oldNo++
		case ' ':
			model.Lines = append(model.Lines, diffPanelLine{Kind: diffPanelLineContext, OldNo: oldNo, NewNo: newNo, Marker: marker, Text: body})
			model.MaxOld = maxInt(model.MaxOld, oldNo)
			model.MaxNew = maxInt(model.MaxNew, newNo)
			oldNo++
			newNo++
		default:
			model.Lines = append(model.Lines, diffPanelLine{Kind: diffPanelLineContext, OldNo: oldNo, NewNo: newNo, Marker: ' ', Text: raw})
			model.MaxOld = maxInt(model.MaxOld, oldNo)
			model.MaxNew = maxInt(model.MaxNew, newNo)
			oldNo++
			newNo++
		}
	}
	return model
}

func parseDiffHunkHeader(header string) (oldStart, oldCount, newStart, newCount int, ok bool) {
	fields := strings.Fields(header)
	if len(fields) < 3 {
		return 0, 0, 0, 0, false
	}
	oldStart, oldCount, oldOK := parseDiffRange(fields[1], '-')
	newStart, newCount, newOK := parseDiffRange(fields[2], '+')
	return oldStart, oldCount, newStart, newCount, oldOK && newOK
}

func parseDiffRange(token string, prefix byte) (start, count int, ok bool) {
	token = strings.TrimSpace(token)
	if len(token) < 2 || token[0] != prefix {
		return 0, 0, false
	}
	body := token[1:]
	parts := strings.SplitN(body, ",", 2)
	parsedStart, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0, 0, false
	}
	parsedCount := 1
	if len(parts) == 2 {
		parsedCount, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, 0, false
		}
	}
	return parsedStart, parsedCount, true
}

func lastDiffRangeLine(start, count int) int {
	if start <= 0 || count <= 0 {
		return 0
	}
	return start + count - 1
}

func renderNumberedACPDiffPanelBody(model diffPanelModel, width int, ctx BlockRenderContext) []renderedDiffPanelRow {
	oldWidth := maxInt(1, decimalWidth(model.MaxOld))
	newWidth := maxInt(1, decimalWidth(model.MaxNew))
	rows := make([]renderedDiffPanelRow, 0, len(model.Lines))
	for _, line := range model.Lines {
		rows = append(rows, renderNumberedACPDiffPanelLine(line, oldWidth, newWidth, width, ctx)...)
	}
	return rows
}

func renderNumberedACPDiffPanelLine(line diffPanelLine, oldWidth, newWidth, width int, ctx BlockRenderContext) []renderedDiffPanelRow {
	switch line.Kind {
	case diffPanelLineMeta:
		plain := "  " + line.Text
		if strings.TrimSpace(line.Text) == "" {
			plain = ""
		}
		return []renderedDiffPanelRow{{
			Plain:  plain,
			Styled: ctx.Theme.TranscriptMetaStyle().Width(width).Render(plain),
		}}
	case diffPanelLineHunk:
		plain := diffMetaPrefix(oldWidth, newWidth) + line.Text
		return []renderedDiffPanelRow{{
			Plain:  plain,
			Styled: ctx.Theme.DiffHunkStyle().Width(width).Render(plain),
		}}
	}

	lineStyle, markerStyle, contentStyle := diffPanelStyles(line.Kind, ctx)
	gutterPlain := diffGutterPlain(line, oldWidth, newWidth)
	marker := string(line.Marker)
	if marker == "" || line.Marker == 0 {
		marker = " "
	}
	firstPrefixPlain := gutterPlain + marker
	continuationPrefixPlain := strings.Repeat(" ", displayColumns(firstPrefixPlain))
	available := maxInt(1, width-displayColumns(firstPrefixPlain))
	wrapped := strings.Split(hardWrapDisplayLine(line.Text, available), "\n")
	if len(wrapped) == 0 {
		wrapped = []string{""}
	}
	out := make([]renderedDiffPanelRow, 0, len(wrapped))
	for idx, segment := range wrapped {
		prefixPlain := firstPrefixPlain
		prefixStyled := diffGutterStyled(gutterPlain, ctx) + markerStyle.Render(marker)
		if idx > 0 {
			prefixPlain = continuationPrefixPlain
			prefixStyled = ctx.Theme.DiffLineNoStyle().Render(continuationPrefixPlain)
		}
		plain := prefixPlain + segment
		styled := lineStyle.Width(width).Render(prefixStyled + contentStyle.Render(tuikit.LinkifyText(segment, ctx.Theme.LinkStyle())))
		out = append(out, renderedDiffPanelRow{Plain: plain, Styled: styled})
	}
	return out
}

func diffPanelStyles(kind diffPanelLineKind, ctx BlockRenderContext) (line lipgloss.Style, marker lipgloss.Style, content lipgloss.Style) {
	switch kind {
	case diffPanelLineAdd:
		style := ctx.Theme.DiffAddStyle().Background(ctx.Theme.DiffAddBg)
		return style, ctx.Theme.DiffAddStyle().Bold(true).Background(ctx.Theme.DiffAddBg), style
	case diffPanelLineRemove:
		style := ctx.Theme.DiffRemoveStyle().Background(ctx.Theme.DiffRemoveBg)
		return style, ctx.Theme.DiffRemoveStyle().Bold(true).Background(ctx.Theme.DiffRemoveBg), style
	default:
		return ctx.Theme.ToolOutputStyle(), ctx.Theme.DiffGutterStyle(), ctx.Theme.ToolOutputStyle()
	}
}

func diffGutterPlain(line diffPanelLine, oldWidth, newWidth int) string {
	oldNo := formatDiffLineNo(line.OldNo, oldWidth)
	newNo := formatDiffLineNo(line.NewNo, newWidth)
	return "  " + oldNo + " " + newNo + " "
}

func diffGutterStyled(gutter string, ctx BlockRenderContext) string {
	return ctx.Theme.DiffLineNoStyle().Render(gutter)
}

func diffMetaPrefix(oldWidth, newWidth int) string {
	return "  " + strings.Repeat(" ", oldWidth) + " " + strings.Repeat(" ", newWidth) + " "
}

func formatDiffLineNo(value int, width int) string {
	if value <= 0 {
		return strings.Repeat(" ", maxInt(1, width))
	}
	return leftPadString(strconv.Itoa(value), maxInt(1, width))
}

func leftPadString(value string, width int) string {
	if displayColumns(value) >= width {
		return value
	}
	return strings.Repeat(" ", width-displayColumns(value)) + value
}

func decimalWidth(value int) int {
	if value <= 0 {
		return 1
	}
	return len(strconv.Itoa(value))
}
