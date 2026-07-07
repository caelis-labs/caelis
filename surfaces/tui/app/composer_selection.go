package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type promptAwareSelectionStyles struct {
	selection    lipgloss.Style
	text         lipgloss.Style
	prompt       lipgloss.Style
	continuation lipgloss.Style
	hasBg        bool
}

func (m *Model) promptAwareSelectionStyles() promptAwareSelectionStyles {
	styles := promptAwareSelectionStyles{
		selection: m.theme.InputSelectionStyle(),
		text:      m.theme.TextStyle(),
		prompt:    m.theme.PromptStyle(),
	}
	if chrome := m.composerChrome(); chrome.active {
		styles.hasBg = true
		bg := m.theme.UserBg
		styles.prompt = styles.prompt.Background(bg)
		styles.text = styles.text.Background(bg)
		styles.continuation = lipgloss.NewStyle().Background(bg)
	}
	return styles
}

// inputSelectionContentRange returns the selected span within the editable
// content of a composer plain line (excluding the prompt/continuation prefix).
// Highlight rendering and clipboard copy both use this helper so they stay aligned.
func inputSelectionContentRange(
	line string,
	globalLine int,
	start, end textSelectionPoint,
	promptWidth int,
) (contentFrom, contentTo int, selected bool) {
	if globalLine < start.line || globalLine > end.line {
		return 0, 0, false
	}
	width := displayColumns(line)
	selFrom := 0
	selTo := width
	if globalLine == start.line {
		selFrom = start.col
	}
	if globalLine == end.line {
		selTo = end.col
	}
	if selFrom < 0 {
		selFrom = 0
	}
	if selTo > width {
		selTo = width
	}
	if selTo < selFrom {
		selTo = selFrom
	}

	contentPlain := sliceByDisplayColumns(line, promptWidth, width)
	contentFrom = maxInt(0, selFrom-promptWidth)
	contentTo = maxInt(0, selTo-promptWidth)
	contentFrom = alignDisplayColumnToCharBoundary(contentPlain, contentFrom)
	contentTo = alignDisplayColumnToCharBoundary(contentPlain, contentTo)
	contentW := displayColumns(contentPlain)
	if contentTo > contentW {
		contentTo = contentW
	}
	if contentTo < contentFrom {
		contentTo = contentFrom
	}
	return contentFrom, contentTo, contentTo > contentFrom
}

func selectionTextFromInputLines(lines []string, start, end textSelectionPoint, promptWidth int) string {
	if len(lines) == 0 {
		return ""
	}
	multiLine := start.line != end.line
	out := make([]string, 0, end.line-start.line+1)
	for i := start.line; i <= end.line && i < len(lines); i++ {
		line := lines[i]
		width := displayColumns(line)
		contentPlain := sliceByDisplayColumns(line, promptWidth, width)
		from, to, selected := inputSelectionContentRange(line, i, start, end, promptWidth)
		if !selected && !multiLine {
			continue
		}
		out = append(out, sliceByDisplayColumns(contentPlain, from, to))
	}
	return strings.Join(out, "\n")
}

func renderPromptAwareInputSelection(
	lines []string,
	start, end textSelectionPoint,
	rowOffset, promptWidth int,
	prompt string,
	styles promptAwareSelectionStyles,
) string {
	styledRows := make([]string, len(lines))
	for i, line := range lines {
		globalLine := rowOffset + i
		width := displayColumns(line)
		contentFrom, contentTo, selected := inputSelectionContentRange(line, globalLine, start, end, promptWidth)

		promptPlain := sliceByDisplayColumns(line, 0, promptWidth)
		contentPlain := sliceByDisplayColumns(line, promptWidth, width)
		contentW := displayColumns(contentPlain)

		prefix := sliceByDisplayColumns(contentPlain, 0, contentFrom)
		middle := ""
		if selected {
			middle = sliceByDisplayColumns(contentPlain, contentFrom, contentTo)
		}
		suffix := sliceByDisplayColumns(contentPlain, contentTo, contentW)

		var styledPrefix, styledMiddle, styledSuffix string
		if styles.hasBg {
			styledPrefix = styles.text.Render(prefix)
			styledSuffix = styles.text.Render(suffix)
		} else {
			styledPrefix = prefix
			styledSuffix = suffix
		}
		if middle != "" {
			styledMiddle = styles.selection.Render(middle)
		}

		var styledPrompt string
		if globalLine == 0 {
			styledPrompt = styles.prompt.Render(promptPlain)
		} else if styles.hasBg {
			styledPrompt = styles.continuation.Render(promptPlain)
		} else {
			styledPrompt = promptPlain
		}
		styledRows[i] = styledPrompt + styledPrefix + styledMiddle + styledSuffix
	}
	return strings.Join(styledRows, "\n")
}

func (layout composerInputLayout) snapSelectionPoint(point textSelectionPoint) textSelectionPoint {
	if len(layout.layout.rows) == 0 {
		return textSelectionPoint{line: 0, col: layout.promptWidth}
	}
	point = layout.clampPoint(point)
	line := layout.promptForRow(point.line) + layout.layout.rows[point.line]
	width := displayColumns(line)
	contentPlain := sliceByDisplayColumns(line, layout.promptWidth, width)
	contentCol := maxInt(0, point.col-layout.promptWidth)
	contentCol = alignDisplayColumnToCharBoundary(contentPlain, contentCol)
	point.col = layout.promptWidth + contentCol
	return point
}

func (layout composerInputLayout) snapInputSelectionEndpoints(start, end textSelectionPoint) (textSelectionPoint, textSelectionPoint) {
	start = layout.snapSelectionPoint(start)
	end = layout.snapSelectionPoint(end)
	return start, end
}

func (m *Model) inputGlobalPointFromMouse(mouse tea.Mouse, clamp bool) (textSelectionPoint, bool) {
	snapshot := m.buildComposeInputLayout()
	lines := snapshot.visiblePlainLines()
	if len(lines) == 0 {
		return textSelectionPoint{}, false
	}
	localPoint, ok := m.mousePointToInputPoint(mouse.X, mouse.Y, clamp, lines)
	if !ok {
		return textSelectionPoint{}, false
	}
	return snapshot.snapSelectionPoint(snapshot.globalPointFromVisible(localPoint)), true
}
