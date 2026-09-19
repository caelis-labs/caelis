package tuiapp

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func (m *Model) renderSubagentOverlay() string {
	if m == nil || m.subagentOverlay == nil {
		return ""
	}
	state := m.subagentOverlay
	width := min(max(48, m.fixedRowWidth()-4), 120, max(20, m.width-4))
	innerWidth := max(16, width-m.overlayBorderChromeWidth())
	m.refreshSubagentRows("")
	muted := m.theme.HelpHintTextStyle()
	body := []string{m.renderSubagentTitle(innerWidth)}
	if state.searchable() {
		body = append(body, "")
	}
	body = append(body, muted.Render(strings.Repeat("─", innerWidth)))
	footer := m.subagentFooterLines(innerWidth)
	borderInset, contentInset := 0, 0
	if m.overlayUsesBorder() {
		borderInset, contentInset = 1, 2
	}
	maxHeight := max(8, m.height-4)
	rowBudget := max(1, maxHeight-2*borderInset-len(body)-len(footer))
	rowLines, rowOffsets := m.renderSubagentRows(state.rows, innerWidth, len(body), rowBudget)
	if state.searchable() {
		body[1] = m.renderSubagentSearch(innerWidth, rowOffsets)
	}
	body = append(body, rowLines...)
	body = append(body, footer...)
	frame := tuikit.RenderResponsiveOverlayFrame(m.theme, tuikit.ResponsiveOverlayFrameModel{
		Body: body, Width: width, UseBorder: m.overlayUsesBorder(),
	})
	renderedWidth, renderedHeight := lipgloss.Width(frame), lipgloss.Height(frame)
	startX, startY := max(0, (m.width-renderedWidth)/2), max(0, (m.height-renderedHeight)/2)
	screenRows := make([]int, len(rowOffsets))
	for i, offset := range rowOffsets {
		screenRows[i] = -1
		if offset >= 0 {
			screenRows[i] = startY + borderInset + offset
		}
	}
	state.geometry = subagentOverlayGeometry{
		x: startX, y: startY, closeX: startX + contentInset + innerWidth - 1,
		closeY: startY + borderInset, width: renderedWidth, height: renderedHeight, rows: screenRows,
	}
	return frame
}

func (m *Model) renderSubagentSearch(width int, offsets []int) string {
	state := m.subagentOverlay
	hint := ""
	if len(offsets) > 0 && !state.loading && state.err == "" {
		if offsets[0] < 0 {
			hint = "↑ more"
		}
		if offsets[len(offsets)-1] < 0 {
			hint = strings.TrimSpace(hint + "  ↓ more")
		}
	}
	searchWidth := width
	if hint != "" {
		searchWidth = max(3, width-displayColumns(hint)-2)
	}
	search := "/ " + truncateDisplayCellsFromEnd(state.query, searchWidth-3) + "▏"
	if state.query == "" {
		search += "Type to search"
	}
	search = padRightDisplay(truncateTailDisplay(search, searchWidth), searchWidth)
	if hint != "" {
		search += "  " + hint
	}
	return m.theme.HelpHintTextStyle().Render(search)
}

func (m *Model) renderSubagentTitle(width int) string {
	state := m.subagentOverlay
	title := "/team · Team Configuration"
	switch state.page {
	case subagentPageBinding:
		title = "/team › " + string(state.bindingHandle) + " · Choose model"
	case subagentPageSets:
		title = "/team › Binding sets"
	case subagentPageNewRole:
		title = "/team › New role"
	case subagentPageSaveSet:
		title = "/team › Save binding set"
	case subagentPageConfirm:
		title = "/team › Delete " + state.confirmLabel + "?"
	}
	title = truncateTailDisplay(title, max(1, width-2))
	return m.theme.TitleStyle().Render(padRightDisplay(title, width-1)) + m.theme.HelpHintTextStyle().Render("×")
}

// A row's section heading consumes the same viewport budget as its cells.
// Keep the window fixed while its selection remains visible, including hover.
func subagentWindowEnd(rows []subagentOverlayRow, start, budget int) int {
	used := 0
	section := ""
	for i := start; i < len(rows); i++ {
		cost := 1
		if budget >= 4 && rows[i].section != "" && rows[i].section != section {
			cost++
		}
		if used+cost > budget {
			return i
		}
		used += cost
		section = rows[i].section
	}
	return len(rows)
}

func (m *Model) renderSubagentRows(rows []subagentOverlayRow, width, bodyOffset, budget int) ([]string, []int) {
	state := m.subagentOverlay
	offsets := make([]int, len(rows))
	for i := range offsets {
		offsets[i] = -1
	}
	if state.loading || (state.err != "" && len(state.status.Handles) == 0) {
		text := "Loading team configuration…"
		if !state.loading {
			text = "Could not load team configuration."
		}
		return []string{m.theme.HelpHintTextStyle().Render(truncateTailDisplay(text, width))}, offsets
	}
	if len(rows) == 0 {
		text := "No matches. Edit the search or ctrl+w to clear."
		if state.page == subagentPageBinding && state.query == "" {
			text = "No compatible models. Use /connect to add one."
		}
		return []string{m.theme.HelpHintTextStyle().Render(truncateTailDisplay(text, width))}, offsets
	}
	start := clampInt(state.windowStart, 0, len(rows)-1)
	if state.index < start {
		start = state.index
	}
	for state.index >= subagentWindowEnd(rows, start, budget) && start < state.index {
		start++
	}
	end := subagentWindowEnd(rows, start, budget)
	state.windowStart = start
	lines := make([]string, 0, budget)
	section := ""
	for i := start; i < end; i++ {
		row := rows[i]
		if budget >= 4 && row.section != "" && row.section != section {
			lines = append(lines, m.theme.HelpHintTextStyle().Render(truncateTailDisplay("  "+row.section, width)))
		}
		section = row.section
		offsets[i] = bodyOffset + len(lines)
		lines = append(lines, m.renderSubagentRow(row, i == state.index, width))
	}
	return lines, offsets
}

func (m *Model) renderSubagentRow(row subagentOverlayRow, selected bool, width int) string {
	marker := "· "
	if selected {
		marker = "› "
	}
	if row.current {
		marker += "● "
	}
	identity, detail := marker+row.label, row.detail
	innerWidth := max(1, width-2)
	labelWidth := min(26, max(12, innerWidth/3))
	if m.subagentOverlay.page == subagentPageBinding {
		if row.binding.ProfileID != "" {
			if detail != "" {
				identity += " (" + detail + ")"
			}
			detail = pickerEffortControl(row.efforts, row.binding.Effort, selected, innerWidth)
		}
		labelWidth = min(52, max(6, innerWidth*3/5), max(1, innerWidth-14))
	}
	labelWidth = min(labelWidth, max(1, innerWidth-10))
	if selected && m.editingSubagentField() && row.key == m.currentSubagentRow().key {
		detail = truncateDisplayCellsFromEnd(detail, max(1, innerWidth-labelWidth-3)) + "▏"
	}
	line := m.renderPickerColumnsLine(identity, detail, labelWidth, innerWidth, selected)
	if !row.enabled && !selected {
		line = m.theme.HelpHintTextStyle().Render(padRightDisplay(" "+padRightDisplay(truncateTailDisplay(identity, labelWidth), labelWidth)+"  "+truncateTailDisplay(detail, max(0, innerWidth-labelWidth-2)), width))
	}
	return line
}

func (m *Model) subagentFooterLines(width int) []string {
	state := m.subagentOverlay
	row := m.currentSubagentRow()
	muted := m.theme.HelpHintTextStyle()
	detail := firstNonEmpty(row.search, row.detail)
	if state.page == subagentPageBinding {
		detail = row.label
		if row.reset {
			detail = row.detail
		} else if row.binding.ProfileID != "" {
			detail += " [" + row.binding.Effort + "]"
			if row.detail != "" {
				detail += " · " + row.detail
			}
			if row.current {
				detail += " · current model"
			}
		}
	}
	if row.nameConflict {
		detail = row.detail
	}
	if state.searchable() && len(state.rows) > 0 {
		detail = fmt.Sprintf("%d/%d · ", state.index+1, len(state.rows)) + detail
	}
	if state.notice != "" {
		detail = state.notice
	}
	if state.pending {
		detail = "Saving configuration…"
	}
	if state.loading {
		detail = "Loading configuration…"
	}
	style := muted
	if state.err != "" {
		detail, style = state.err, m.theme.ErrorStyle()
	} else if row.nameConflict {
		style = m.theme.ErrorStyle()
	}
	lines := []string{muted.Render(strings.Repeat("─", width)), style.Render(truncateTailDisplay(detail, width))}
	help := "↑↓ select  enter choose  esc back"
	compact := "↑↓  enter  esc"
	switch state.page {
	case subagentPageMain:
		help = "↑↓ select  enter edit  esc close"
	case subagentPageBinding:
		help, compact = "↑↓ select  ←→ effort  enter apply  esc back", "↑↓  ←→  enter  esc"
		if state.creatingRole {
			help = "↑↓ select  ←→ effort  enter choose  esc back"
		}
	case subagentPageNewRole, subagentPageSaveSet:
		help = "tab field  type to edit  enter next  esc back"
		if !m.editingSubagentField() {
			help = "tab field  enter choose  esc back"
		}
	}
	if state.err != "" && len(state.status.Handles) == 0 {
		help = "enter retry  esc close"
	}
	if state.pending {
		help = "Saving…"
	}
	if displayColumns(help) > width {
		help = compact
	}
	lines = append(lines, muted.Render(truncateTailDisplay(help, width)))
	if (state.page == subagentPageMain || state.page == subagentPageSets) && m.height >= 20 {
		shortcuts := "ctrl+n new  ctrl+p sets  ctrl+s save  del delete"
		if state.page == subagentPageSets {
			shortcuts = "ctrl+s save  del delete"
		}
		if displayColumns(shortcuts) > width {
			shortcuts = "ctrl+n new  ctrl+p sets"
		}
		if row.custom || strings.HasPrefix(row.key, "set:") {
			if width < 48 {
				shortcuts = "del delete  ctrl+s save"
			}
		}
		lines = append(lines, muted.Render(truncateTailDisplay(shortcuts, width)))
	}
	return lines
}
