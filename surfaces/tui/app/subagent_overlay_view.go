package tuiapp

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
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

	body = append(body, muted.Render(strings.Repeat("─", innerWidth)))
	if state.searchable() && state.query != "" {
		body = append(body, muted.Render("/ "+truncateDisplayCellsFromEnd(state.query, innerWidth-3)+"▏"))
	}
	if state.page == subagentPageBinding && len(state.rows) > 0 && !state.loading {
		modelWidth, effortWidth, fastWidth := m.subagentBindingColumns(innerWidth - 2)
		header := padRightDisplay(sliceByDisplayColumns("  Model", 0, modelWidth), modelWidth)
		if effortWidth > 0 {
			header += " " + padRightDisplay(sliceByDisplayColumns("Effort", 0, effortWidth), effortWidth)
		}
		if fastWidth > 0 {
			header += " " + sliceByDisplayColumns("Fast", 0, fastWidth)
		}
		body = append(body, " "+muted.Render(header))
	}
	footer := m.subagentFooterLines(innerWidth)
	borderInset, contentInset := 0, 0
	if m.overlayUsesBorder() {
		borderInset, contentInset = 1, 2
	}
	maxHeight := max(8, m.height-4)
	rowBudget := max(1, maxHeight-2*borderInset-len(body)-len(footer))
	rowLines, rowOffsets := m.renderSubagentRows(state.rows, innerWidth, len(body), rowBudget)
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
		x: startX, y: startY, closeX: overlayCloseX(startX+contentInset, innerWidth),
		closeY: startY + borderInset, width: renderedWidth, height: renderedHeight, rows: screenRows,
	}
	state.geometry.cells = make([][]subagentCell, len(state.rows))
	for i, row := range state.rows {
		cells := m.subagentCells(row, innerWidth-2)
		for j := range cells {
			cells[j].x += startX + contentInset + 1
		}
		state.geometry.cells[i] = cells
	}
	return frame
}

func (m *Model) renderSubagentTitle(width int) string {
	state := m.subagentOverlay
	path := []string{"/team"}
	title := "Team Configuration"
	switch state.page {
	case subagentPageBinding:
		role := string(state.bindingHandle)
		title = "Choose model"
		switch state.bindingHandle {
		case agentbinding.HandleGuardian:
			role = "Guardian"
		case agentbinding.HandleGuardianScreen:
			role = "Guardian"
			title = "Choose classifier"
		}
		path = append(path, role)
	case subagentPageSets:
		title = "Binding sets"
	case subagentPageNewRole:
		title = "New role"
	case subagentPageSaveSet:
		path = append(path, "Binding sets")
		title = "Save binding set"
	case subagentPageConfirm:
		title = "Delete " + state.confirmLabel + "?"
	}
	return m.overlayHeader(width, path, title, state.closeButton, !state.pending && !state.loading)
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

func (m *Model) subagentFooterLines(width int) []string {
	state := m.subagentOverlay
	row := m.currentSubagentRow()
	muted := m.theme.HelpHintTextStyle()
	detail := ""
	if state.page == subagentPageBinding && state.query == "" && len(state.rows) == 1 && row.reset {
		detail = "Use /connect to add a compatible model."
	}
	if state.searchable() && state.query != "" {
		detail = fmt.Sprintf("%d results", len(state.rows))
	}
	if row.nameConflict {
		detail = row.detail
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
	lines := []string{muted.Render(strings.Repeat("─", width))}
	if detail != "" {
		lines = append(lines, style.Render(truncateTailDisplay(detail, width)))
	}
	help := "↑↓ select  enter choose  esc back"
	compact := "↑↓  enter  esc"
	switch state.page {
	case subagentPageMain:
		help = "↑↓ select  enter edit  esc close"
		if row.companion != nil {
			help, compact = "↑↓ row  tab model/"+strings.ToLower(row.companion.label)+"  enter edit  esc close", "↑↓  tab  enter  esc"
		}
		if state.field == subagentFieldModel && (len(row.efforts) > 1 || row.fastSupported) {
			help = "↑↓ row  tab field  ←→ effort"
			if row.fastSupported {
				help += "  f fast"
			}
			help += "  enter edit  esc close"
		}
	case subagentPageBinding:
		help, compact = "↑↓ model", "↑↓  tab  ←→  enter  esc"
		if len(m.subagentFields()) > 1 {
			help += "  tab field"
		}
		if len(row.efforts) > 1 {
			help += "  ←→ effort"
		}
		if row.fastSupported {
			help += "  f fast"
		}
		if state.creatingRole {
			help += "  enter choose"
		} else {
			help += "  enter apply"
		}
		help += "  esc back"
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
