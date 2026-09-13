package tuiapp

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

type diffPanelLineKind int

const (
	diffPanelLineMeta diffPanelLineKind = iota
	diffPanelLineContext
	diffPanelLineAdd
	diffPanelLineRemove
)

type diffPanelLine struct {
	Kind       diffPanelLineKind
	OldNo      int
	NewNo      int
	Marker     byte
	Text       string
	Path       string
	StyledText string
	Changed    []diffTextSpan
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
	model := diffPanelModel{}
	oldNo, newNo := 0, 0
	oldRemaining, newRemaining := 0, 0
	inHunk, seenHunk := false, false
	path, title := "", ""
	for _, raw := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if strings.HasPrefix(raw, "@@") {
			oldStart, oldCount, newStart, newCount, ok := parseDiffHunkHeader(raw)
			if ok {
				if !seenHunk && title != "" {
					model.Lines = append(model.Lines, diffPanelLine{Kind: diffPanelLineMeta, Text: title, Path: path})
				} else if seenHunk {
					model.Lines = append(model.Lines, diffPanelLine{Kind: diffPanelLineMeta})
				}
				oldNo, newNo = oldStart, newStart
				oldRemaining, newRemaining = oldCount, newCount
				model.MaxOld = max(model.MaxOld, lastDiffRangeLine(oldStart, oldCount))
				model.MaxNew = max(model.MaxNew, lastDiffRangeLine(newStart, newCount))
				inHunk, seenHunk = true, true
				continue
			}
		}
		if strings.HasPrefix(raw, "diff --git ") {
			inHunk, seenHunk = false, false
			path, title = "", ""
			continue
		}
		if strings.HasPrefix(raw, "\\") {
			model.Lines = append(model.Lines, diffPanelLine{Kind: diffPanelLineMeta, Text: strings.TrimPrefix(raw, "\\ ")})
			continue
		}
		if !inHunk {
			if strings.TrimSpace(raw) == "" || strings.EqualFold(strings.TrimSpace(raw), "diff / hunk") {
				continue
			}
			if strings.HasPrefix(raw, "--- ") {
				path = diffPanelFilePath(strings.TrimPrefix(raw, "--- "))
				title, seenHunk = path, false
				continue
			}
			if strings.HasPrefix(raw, "+++ ") {
				if nextPath := diffPanelFilePath(strings.TrimPrefix(raw, "+++ ")); nextPath != "/dev/null" {
					path = nextPath
				}
				title = path
			} else if candidate, _, _, ok := tuikit.SplitDiffCountTokens(raw); ok {
				path, title, seenHunk = candidate, strings.TrimSpace(raw), false
			} else {
				path, title, seenHunk = strings.TrimSpace(raw), strings.TrimSpace(raw), false
			}
			continue
		}
		if raw == "" {
			continue
		}
		line := diffPanelLine{Marker: raw[0], Text: raw[1:], Path: path}
		switch raw[0] {
		case '+':
			line.Kind, line.NewNo = diffPanelLineAdd, newNo
			newNo++
			newRemaining--
		case '-':
			line.Kind, line.OldNo = diffPanelLineRemove, oldNo
			oldNo++
			oldRemaining--
		case ' ':
			line.Kind, line.OldNo, line.NewNo = diffPanelLineContext, oldNo, newNo
			oldNo++
			newNo++
			oldRemaining--
			newRemaining--
		default:
			model.Lines = append(model.Lines, diffPanelLine{Kind: diffPanelLineMeta, Text: raw})
			continue
		}
		// Only complete hunks admit new file headers. Source lines can contain
		// strings such as "second.py +1 -1" or "+++ b/name" verbatim.
		inHunk = oldRemaining > 0 || newRemaining > 0
		model.MaxOld = max(model.MaxOld, line.OldNo)
		model.MaxNew = max(model.MaxNew, line.NewNo)
		model.Lines = append(model.Lines, line)
	}
	return model
}

func diffPanelFilePath(raw string) string {
	path, _, _ := strings.Cut(raw, "\t")
	if unquoted, err := strconv.Unquote(path); err == nil {
		path = unquoted
	}
	if strings.HasPrefix(path, "a/") || strings.HasPrefix(path, "b/") {
		return path[2:]
	}
	return path
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
	return parsedStart, parsedCount, parsedStart >= 0 && parsedCount >= 0
}

func lastDiffRangeLine(start, count int) int {
	if start <= 0 || count <= 0 {
		return 0
	}
	return start + count - 1
}

// Each side needs at least 48 source columns, in addition to its line gutter.
// The available panel width (not the terminal width) also handles split workspaces.
const diffSideMinContentWidth = 48

func renderNumberedACPDiffPanelBody(model diffPanelModel, width int, ctx BlockRenderContext) []renderedDiffPanelRow {
	oldWidth := maxInt(1, decimalWidth(model.MaxOld))
	newWidth := maxInt(1, decimalWidth(model.MaxNew))
	pairs := alignDiffPanelLines(model.Lines)
	highlightDiffPanelLines(model.Lines, ctx)
	split := width >= max(120, 2*diffSideMinContentWidth+oldWidth+newWidth+9)
	rows := make([]renderedDiffPanelRow, 0, len(model.Lines))
	for _, pair := range pairs {
		if pair.meta != nil {
			plain := truncateTailDisplay(pair.meta.Text, width)
			rows = append(rows, renderedDiffPanelRow{Plain: plain, Styled: ctx.Theme.TranscriptMetaStyle().Render(plain)})
			continue
		}
		if split {
			leftWidth := (width - 3) / 2
			rightWidth := width - 3 - leftWidth
			left := renderDiffPanelCell(pair.old, oldWidth, 0, leftWidth, ctx)
			right := renderDiffPanelCell(pair.new, 0, newWidth, rightWidth, ctx)
			for i := range max(len(left), len(right)) {
				l, r := paddedDiffCell(left, i, leftWidth), paddedDiffCell(right, i, rightWidth)
				rows = append(rows, renderedDiffPanelRow{
					Plain:  l.Plain + " │ " + r.Plain,
					Styled: l.Styled + ctx.Theme.DiffGutterStyle().Render(" │ ") + r.Styled,
				})
			}
		} else {
			if pair.old != nil {
				rows = append(rows, renderDiffPanelCell(pair.old, oldWidth, newWidth, width, ctx)...)
			}
			if pair.new != nil && pair.new != pair.old {
				rows = append(rows, renderDiffPanelCell(pair.new, oldWidth, newWidth, width, ctx)...)
			}
		}
	}
	return rows
}

type diffPanelPair struct{ old, new, meta *diffPanelLine }

func alignDiffPanelLines(lines []diffPanelLine) []diffPanelPair {
	pairs := make([]diffPanelPair, 0, len(lines))
	for i := 0; i < len(lines); {
		line := &lines[i]
		if line.Kind == diffPanelLineMeta {
			pairs = append(pairs, diffPanelPair{meta: line})
			i++
			continue
		}
		if line.Kind == diffPanelLineContext {
			pairs = append(pairs, diffPanelPair{old: line, new: line})
			i++
			continue
		}
		var old, next []*diffPanelLine
		for i < len(lines) && (lines[i].Kind == diffPanelLineAdd || lines[i].Kind == diffPanelLineRemove) {
			if lines[i].Kind == diffPanelLineRemove {
				old = append(old, &lines[i])
			} else {
				next = append(next, &lines[i])
			}
			i++
		}
		for _, pair := range alignDiffChanges(old, next) {
			if pair.old != nil && pair.new != nil {
				markDiffTextChanges(pair.old, pair.new)
			}
			pairs = append(pairs, pair)
		}
	}
	return pairs
}

func paddedDiffCell(rows []renderedDiffPanelRow, i, width int) renderedDiffPanelRow {
	if i >= len(rows) {
		blank := strings.Repeat(" ", width)
		return renderedDiffPanelRow{Plain: blank, Styled: blank}
	}
	row := rows[i]
	row.Plain += strings.Repeat(" ", max(0, width-displayColumns(row.Plain)))
	row.Styled += strings.Repeat(" ", max(0, width-displayColumns(row.Styled)))
	return row
}

func renderDiffPanelCell(line *diffPanelLine, oldWidth, newWidth, width int, ctx BlockRenderContext) []renderedDiffPanelRow {
	if line == nil {
		return nil
	}
	gutter := " "
	if oldWidth > 0 {
		gutter += formatDiffLineNo(line.OldNo, oldWidth) + " "
	}
	if newWidth > 0 {
		gutter += formatDiffLineNo(line.NewNo, newWidth) + " "
	}
	marker := string(line.Marker)
	prefix := gutter + marker + " "
	// Very narrow terminals preserve source characters before optional line numbers.
	if displayColumns(prefix) >= width {
		prefix, gutter = marker, ""
	}
	available := max(1, width-displayColumns(prefix))
	styledText := line.StyledText
	if styledText == "" {
		styledText = ctx.Theme.TextStyle().Render(line.Text)
	}
	wrapped := wrapDiffPanelText(styledText, available)
	background, markerStyle := diffPanelLineStyle(line.Kind, ctx)
	lineNoStyle := ctx.Theme.DiffLineNoStyle().Foreground(ctx.Theme.ReadableTextColor(ctx.Theme.DiffLineNoFg, background.GetBackground()))
	rows := make([]renderedDiffPanelRow, 0, len(wrapped))
	for i, segment := range wrapped {
		plainPrefix := prefix
		styledPrefix := lineNoStyle.Render(gutter) + markerStyle.Render(marker+" ")
		if gutter == "" {
			styledPrefix = markerStyle.Render(marker)
		}
		if i > 0 {
			plainPrefix = strings.Repeat(" ", displayColumns(prefix))
			styledPrefix = plainPrefix
		}
		plain := plainPrefix + ansi.Strip(segment)
		styled := styledPrefix + segment
		styled = tuikit.PaintLineBackground(styled, width, background.GetBackground())
		rows = append(rows, renderedDiffPanelRow{Plain: plain, Styled: styled})
	}
	return rows
}

func wrapDiffPanelText(text string, width int) []string {
	return splitStyledPhysicalLines(hardWrapDisplayLine(text, width))
}

func diffPanelLineStyle(kind diffPanelLineKind, ctx BlockRenderContext) (lipgloss.Style, lipgloss.Style) {
	switch kind {
	case diffPanelLineAdd:
		return lipgloss.NewStyle().Background(ctx.Theme.DiffAddBg), ctx.Theme.DiffAddStyle().Bold(true)
	case diffPanelLineRemove:
		return lipgloss.NewStyle().Background(ctx.Theme.DiffRemoveBg), ctx.Theme.DiffRemoveStyle().Bold(true)
	default:
		return lipgloss.NewStyle(), ctx.Theme.DiffGutterStyle()
	}
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
