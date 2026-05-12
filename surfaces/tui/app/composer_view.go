package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
)

type composerRender struct {
	styledLines []string
	plainLines  []string
	cursor      *tea.Cursor
}

type composerLayout struct {
	rows      []string
	cursorRow int
	cursorCol int
	totalRows int
}

func (r composerRender) styledText() string {
	return strings.Join(r.styledLines, "\n")
}

func (m *Model) renderRegularInputBar() string {
	return insetRenderedBlock(m.composeInputRender().styledText(), inputHorizontalInset)
}

func (m *Model) regularInputPlainLines() []string {
	lines := m.composeInputRender().plainLines
	if len(lines) == 0 {
		return []string{m.inputPromptPrefix()}
	}
	return lines
}

func (m *Model) regularInputCursor() *tea.Cursor {
	if m.activePrompt != nil {
		return nil
	}
	if m.isWizardActive() && m.wizard.hideInput() {
		return nil
	}
	render := m.composeInputRender()
	if render.cursor == nil {
		return nil
	}
	cursor := *render.cursor
	cursor.X += inputHorizontalInset
	return &cursor
}

func (m *Model) composeInputRender() composerRender {
	prompt := m.inputPromptPrefix()
	promptWidth := displayColumns(prompt)
	if promptWidth <= 0 {
		prompt = "> "
		promptWidth = displayColumns(prompt)
	}
	continuation := strings.Repeat(" ", promptWidth)

	value := m.textarea.Value()
	cursorIndex := m.textareaCursorIndex()
	if m.isWizardActive() && m.wizard != nil {
		if visible, visibleCursor, ok := wizardVisibleInputAtCursor(m.wizard.def.Command, []rune(value), cursorIndex); ok {
			value = visible
			cursorIndex = visibleCursor
			if m.wizard.hideInput() {
				value = strings.Repeat("•", len([]rune(visible)))
				cursorIndex = len([]rune(value))
			}
		}
	}
	totalWidth := m.composerContentWidth()
	contentWidth := maxInt(1, totalWidth-promptWidth)
	displayValue, displayCursor := composeInputDisplay(value, cursorIndex, m.inputAttachments)
	layout := layoutComposerDisplay(displayValue, displayCursor, contentWidth)
	rows := layout.rows
	cursorRow := layout.cursorRow
	cursorCol := layout.cursorCol
	if len(rows) == 0 {
		rows = []string{""}
		cursorRow = 0
		cursorCol = 0
	}

	placeholder := ""
	if len(m.inputAttachments) == 0 && value == "" {
		placeholder = m.textarea.Placeholder
	}
	ghost := ""
	if placeholder == "" && cursorIndex == len([]rune(value)) {
		ghost = m.currentInputGhostHint()
	}

	start := composerWindowStart(cursorRow, len(rows), maxInputBarRows)
	end := minInt(len(rows), start+maxInputBarRows)

	styled := make([]string, 0, end-start)
	plain := make([]string, 0, end-start)
	for idx := start; idx < end; idx++ {
		promptPlain := continuation
		promptStyled := continuation
		if idx == 0 {
			promptPlain = prompt
			promptStyled = m.theme.PromptStyle().Render(prompt)
		}

		contentPlain := rows[idx]
		var contentStyled string
		switch {
		case placeholder != "" && idx == 0:
			contentPlain = truncateTailDisplay(placeholder, contentWidth)
			contentStyled = m.theme.HelpHintTextStyle().Render(contentPlain)
		case ghost != "" && idx == cursorRow:
			remaining := maxInt(0, contentWidth-displayColumns(contentPlain))
			ghostPart := truncateTailDisplay(ghost, remaining)
			contentPlain += ghostPart
			contentStyled = m.theme.TextStyle().Render(rows[idx]) + m.theme.HelpHintTextStyle().Render(ghostPart)
		default:
			contentStyled = m.theme.TextStyle().Render(rows[idx])
		}

		styled = append(styled, promptStyled+contentStyled)
		plain = append(plain, promptPlain+contentPlain)
	}

	var cursor *tea.Cursor
	if m.textarea.Focused() && cursorRow >= start && cursorRow < end {
		styles := m.textarea.Styles()
		cursor = tea.NewCursor(promptWidth+cursorCol, cursorRow-start)
		cursor.Blink = styles.Cursor.Blink
		cursor.Color = styles.Cursor.Color
		cursor.Shape = styles.Cursor.Shape
	}

	return composerRender{
		styledLines: styled,
		plainLines:  plain,
		cursor:      cursor,
	}
}

func (m *Model) composerContentWidth() int {
	if width := m.readableContentWidth() - (inputHorizontalInset * 2); width > 0 {
		return maxInt(20, width)
	}
	if width := m.textarea.Width(); width > 0 {
		return width
	}
	return 20
}

func composerWindowStart(cursorRow int, totalRows int, maxRows int) int {
	if maxRows <= 0 || totalRows <= maxRows {
		return 0
	}
	start := max(cursorRow-maxRows+1, 0)
	maxStart := totalRows - maxRows
	if start > maxStart {
		start = maxStart
	}
	return start
}

func layoutComposerDisplay(value string, cursor int, width int) composerLayout {
	if width <= 0 {
		width = 1
	}
	if cursor < 0 {
		cursor = 0
	}
	valueRunes := []rune(value)
	if cursor > len(valueRunes) {
		cursor = len(valueRunes)
	}

	rows := make([]string, 0, 4)
	cursorRow := 0
	cursorCol := 0
	cursorAssigned := false
	globalRune := 0
	rowStartRune := 0
	rowWidth := 0
	var row strings.Builder

	appendRow := func(rowText string, rowEndRune int) {
		rows = append(rows, rowText)
		if cursorAssigned {
			return
		}
		if cursor < rowStartRune || cursor > rowEndRune {
			return
		}
		cursorRow = len(rows) - 1
		cursorCol = displayColumns(composerCursorPrefix(rowText, cursor-rowStartRune))
		cursorAssigned = true
	}

	for _, cluster := range splitGraphemeClusters(value) {
		clusterRuneLen := len([]rune(cluster))
		clusterEndRune := globalRune + clusterRuneLen
		if cluster == "\n" {
			appendRow(row.String(), globalRune)
			row.Reset()
			rowWidth = 0
			globalRune = clusterEndRune
			rowStartRune = globalRune
			continue
		}

		clusterWidth := maxInt(0, graphemeWidth(cluster))
		if rowWidth > 0 && rowWidth+clusterWidth > width {
			appendRow(row.String(), globalRune)
			row.Reset()
			rowWidth = 0
			rowStartRune = globalRune
		}
		row.WriteString(cluster)
		rowWidth += clusterWidth
		globalRune = clusterEndRune
	}

	appendRow(row.String(), globalRune)

	if len(rows) == 0 {
		rows = []string{""}
	}
	if !cursorAssigned {
		cursorRow = len(rows) - 1
		cursorCol = displayColumns(rows[len(rows)-1])
	}

	return composerLayout{
		rows:      rows,
		cursorRow: cursorRow,
		cursorCol: cursorCol,
		totalRows: len(rows),
	}
}

func composerCursorPrefix(row string, cursor int) string {
	if cursor <= 0 || row == "" {
		return ""
	}
	rowRunes := []rune(row)
	if cursor >= len(rowRunes) {
		return row
	}
	consumedRunes := 0
	var out strings.Builder
	for _, cluster := range splitGraphemeClusters(row) {
		clusterRunes := len([]rune(cluster))
		next := consumedRunes + clusterRunes
		if cursor < next {
			break
		}
		out.WriteString(cluster)
		consumedRunes = next
	}
	return out.String()
}

func desiredComposerRows(value string, _ string, width int, maxRows int) int {
	layout := layoutComposerDisplay(value, len([]rune(value)), width)
	if layout.totalRows < 1 {
		return 1
	}
	if maxRows > 0 && layout.totalRows > maxRows {
		return maxRows
	}
	return layout.totalRows
}

func (m *Model) textareaCursorIndex() int {
	value := m.textarea.Value()
	if value == "" {
		return 0
	}
	lines := strings.Split(value, "\n")
	row := max(m.textarea.Line(), 0)
	if row >= len(lines) {
		row = len(lines) - 1
	}
	lineInfo := m.textarea.LineInfo()
	col := lineInfo.StartColumn + lineInfo.ColumnOffset
	lineRunes := []rune(lines[row])
	if col < 0 {
		col = 0
	}
	if col > len(lineRunes) {
		col = len(lineRunes)
	}
	index := 0
	for i := 0; i < row; i++ {
		index += len([]rune(lines[i])) + 1
	}
	return index + col
}

func (m *Model) moveTextareaCursorToIndex(target int) {
	valueRunes := []rune(m.textarea.Value())
	if target < 0 {
		target = 0
	}
	if target > len(valueRunes) {
		target = len(valueRunes)
	}
	m.textarea.CursorEnd()
	for i := len(valueRunes); i > target; i-- {
		var cmd tea.Cmd
		m.textarea, cmd = m.textarea.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
		_ = cmd
	}
}
