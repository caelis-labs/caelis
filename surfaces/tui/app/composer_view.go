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
	rows          []string
	rowStartRunes []int
	cursorRow     int
	cursorCol     int
	totalRows     int
}

type composerInputLayout struct {
	prompt         string
	promptWidth    int
	continuation   string
	value          string
	cursorIndex    int
	contentWidth   int
	layout         composerLayout
	displayToValue []int
	rowOffset      int
	rowEnd         int
}

func (r composerRender) styledText() string {
	return strings.Join(r.styledLines, "\n")
}

func (m *Model) regularInputPlainLines() []string {
	lines := m.composeInputLayout().visiblePlainLines()
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
	render := m.composeInputRenderFrom(m.composeInputLayout())
	if render.cursor == nil {
		return nil
	}
	cursor := *render.cursor
	cursor.X += m.composerInputColumnOffset()
	return &cursor
}

func (m *Model) composeInputRender() composerRender {
	return m.composeInputRenderFrom(m.composeInputLayout())
}

func (m *Model) composeInputRenderFrom(snapshot composerInputLayout) composerRender {
	rows := snapshot.layout.rows
	cursorRow := snapshot.layout.cursorRow
	cursorCol := snapshot.layout.cursorCol
	start := snapshot.rowOffset
	end := snapshot.rowEnd
	prompt := snapshot.prompt
	continuation := snapshot.continuation
	contentWidth := snapshot.contentWidth
	value := snapshot.value
	cursorIndex := snapshot.cursorIndex

	placeholder := ""
	if len(m.inputAttachments) == 0 && value == "" {
		placeholder = m.textarea.Placeholder
	}
	ghost := ""
	if placeholder == "" && cursorIndex == len([]rune(value)) {
		ghost = m.currentInputGhostHint()
	}

	chrome := m.composerChrome()
	promptStyle := m.theme.PromptStyle()
	helpStyle := m.theme.HelpHintTextStyle()
	textStyle := m.theme.TextStyle()
	if chrome.active {
		bg := m.theme.UserBg
		promptStyle = promptStyle.Background(bg)
		helpStyle = helpStyle.Background(bg)
		textStyle = textStyle.Background(bg)
	}

	styled := make([]string, 0, end-start)
	plain := make([]string, 0, end-start)
	for idx := start; idx < end; idx++ {
		promptPlain := continuation
		promptStyled := continuation
		if idx == 0 {
			promptPlain = prompt
			promptStyled = promptStyle.Render(prompt)
		} else if chrome.active {
			promptStyled = m.composerBgStyle().Render(continuation)
		}

		contentPlain := rows[idx]
		var contentStyled string
		switch {
		case placeholder != "" && idx == 0:
			contentPlain = truncateTailDisplay(placeholder, contentWidth)
			contentStyled = helpStyle.Render(contentPlain)
		case ghost != "" && idx == cursorRow:
			remaining := maxInt(0, contentWidth-displayColumns(contentPlain))
			ghostPart := truncateTailDisplay(ghost, remaining)
			contentPlain += ghostPart
			contentStyled = textStyle.Render(rows[idx]) + helpStyle.Render(ghostPart)
		default:
			contentStyled = textStyle.Render(rows[idx])
		}

		styled = append(styled, promptStyled+contentStyled)
		plain = append(plain, promptPlain+contentPlain)
	}

	var cursor *tea.Cursor
	if m.textarea.Focused() && cursorRow >= start && cursorRow < end {
		styles := m.textarea.Styles()
		cursor = tea.NewCursor(snapshot.promptWidth+cursorCol, cursorRow-start)
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

func (m *Model) composeInputLayout() composerInputLayout {
	if m.composerViewSnapshot != nil {
		return *m.composerViewSnapshot
	}
	return m.buildComposeInputLayout()
}

func (m *Model) buildComposeInputLayout() composerInputLayout {
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
	displayValue, displayCursor, displayToValue := composeInputDisplayWithMap(value, cursorIndex, m.inputAttachments)
	layout := layoutComposerDisplay(displayValue, displayCursor, contentWidth)
	rows := layout.rows
	if len(rows) == 0 {
		rows = []string{""}
		layout.rows = rows
		layout.rowStartRunes = []int{0}
		layout.cursorRow = 0
		layout.cursorCol = 0
		layout.totalRows = 1
	}

	start := clampComposerRowOffset(m.composerRowOffset, len(rows), maxInputBarRows)
	m.composerRowOffset = start
	end := minInt(len(rows), start+maxInputBarRows)

	return composerInputLayout{
		prompt:         prompt,
		promptWidth:    promptWidth,
		continuation:   continuation,
		value:          value,
		cursorIndex:    cursorIndex,
		contentWidth:   contentWidth,
		layout:         layout,
		displayToValue: displayToValue,
		rowOffset:      start,
		rowEnd:         end,
	}
}

func (layout composerInputLayout) plainLines(start int, end int) []string {
	if start < 0 {
		start = 0
	}
	if end > len(layout.layout.rows) {
		end = len(layout.layout.rows)
	}
	if end < start {
		end = start
	}
	lines := make([]string, 0, end-start)
	for idx := start; idx < end; idx++ {
		prefix := layout.continuation
		if idx == 0 {
			prefix = layout.prompt
		}
		lines = append(lines, prefix+layout.layout.rows[idx])
	}
	return lines
}

func (layout composerInputLayout) allPlainLines() []string {
	return layout.plainLines(0, len(layout.layout.rows))
}

func (layout composerInputLayout) visiblePlainLines() []string {
	return layout.plainLines(layout.rowOffset, layout.rowEnd)
}

func (layout composerInputLayout) globalPointFromVisible(point textSelectionPoint) textSelectionPoint {
	point.line += layout.rowOffset
	return layout.clampPoint(point)
}

func (layout composerInputLayout) clampPoint(point textSelectionPoint) textSelectionPoint {
	if len(layout.layout.rows) == 0 {
		return textSelectionPoint{line: 0, col: layout.promptWidth}
	}
	if point.line < 0 {
		point.line = 0
	}
	if point.line >= len(layout.layout.rows) {
		point.line = len(layout.layout.rows) - 1
	}
	width := displayColumns(layout.promptForRow(point.line) + layout.layout.rows[point.line])
	if point.col < 0 {
		point.col = 0
	}
	if point.col > width {
		point.col = width
	}
	return point
}

func (layout composerInputLayout) promptForRow(row int) string {
	if row == 0 {
		return layout.prompt
	}
	return layout.continuation
}

func (layout composerInputLayout) textareaIndexFromPoint(point textSelectionPoint) int {
	if len(layout.layout.rows) == 0 {
		return 0
	}
	point = layout.clampPoint(point)
	row := layout.layout.rows[point.line]
	contentCol := maxInt(0, point.col-layout.promptWidth)
	contentCol = alignDisplayColumnToCharBoundary(row, contentCol)
	rowWidth := displayColumns(row)
	if contentCol > rowWidth {
		contentCol = rowWidth
	}
	prefix := sliceByDisplayColumns(row, 0, contentCol)
	displayIndex := layout.layout.rowStartRunes[point.line] + len([]rune(prefix))
	if displayIndex < 0 {
		return 0
	}
	if displayIndex >= len(layout.displayToValue) {
		if len(layout.displayToValue) == 0 {
			return 0
		}
		return layout.displayToValue[len(layout.displayToValue)-1]
	}
	return layout.displayToValue[displayIndex]
}

func (m *Model) composerContentWidth() int {
	if width := m.readableContentWidth() - (inputHorizontalInset * 2) - (m.composerChrome().horizontalInset() * 2); width > 0 {
		return maxInt(20, width)
	}
	if width := m.textarea.Width(); width > 0 {
		return width
	}
	return 20
}

func clampComposerRowOffset(offset int, totalRows int, maxRows int) int {
	if maxRows <= 0 || totalRows <= maxRows {
		return 0
	}
	maxStart := totalRows - maxRows
	if offset < 0 {
		return 0
	}
	if offset > maxStart {
		return maxStart
	}
	return offset
}

// ensureComposerRowOffsetForCursor scrolls the composer window only enough to
// keep the cursor row visible. Used during drag-selection and wheel navigation,
// not on a simple click-to-place-cursor.
func (m *Model) ensureComposerRowOffsetForCursor(cursorRow int, totalRows int) {
	if m == nil {
		return
	}
	if totalRows <= maxInputBarRows {
		m.composerRowOffset = 0
		return
	}
	offset := m.composerRowOffset
	if cursorRow < offset {
		offset = cursorRow
	}
	if cursorRow >= offset+maxInputBarRows {
		offset = cursorRow - maxInputBarRows + 1
	}
	m.composerRowOffset = clampComposerRowOffset(offset, totalRows, maxInputBarRows)
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
	rowStartRunes := make([]int, 0, 4)
	cursorRow := 0
	cursorCol := 0
	cursorAssigned := false
	globalRune := 0
	rowStartRune := 0
	rowWidth := 0
	var row strings.Builder

	appendRow := func(rowText string, rowEndRune int) {
		rows = append(rows, rowText)
		rowStartRunes = append(rowStartRunes, rowStartRune)
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
		rowStartRunes = []int{0}
	}
	if !cursorAssigned {
		cursorRow = len(rows) - 1
		cursorCol = displayColumns(rows[len(rows)-1])
	}

	return composerLayout{
		rows:          rows,
		rowStartRunes: rowStartRunes,
		cursorRow:     cursorRow,
		cursorCol:     cursorCol,
		totalRows:     len(rows),
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
	current := m.textareaCursorIndex()
	if current == target {
		return
	}

	if target > current {
		diff := target - current
		for i := 0; i < diff; i++ {
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
			_ = cmd
		}
	} else {
		diff := current - target
		for i := 0; i < diff; i++ {
			var cmd tea.Cmd
			m.textarea, cmd = m.textarea.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
			_ = cmd
		}
	}
}
