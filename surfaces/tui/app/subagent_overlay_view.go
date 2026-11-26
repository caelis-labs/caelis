package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
)

func (m *Model) renderSubagentOverlay() string {
	state := m.subagentOverlay
	if state == nil {
		return ""
	}
	m.refreshSubagentRows("")
	width := m.completionRowInnerWidth()
	chrome := m.modelPickerChrome()
	detail := m.subagentOverlayDetail()
	if state.page == subagentPageMain && detail == "" {
		chrome.detailRows = 0
	}
	limit := completionOverlayVisibleItems
	if state.guardianPicker() {
		limit += 5 // Two headings, a separator, and two classifier choices.
	}
	budget := m.completionOverlayBodyLimitFor(chrome, limit)
	var primary, classifier subagentRenderedList
	body := []string{""}

	switch {
	case state.loading:
		body = append(body, " "+m.theme.HelpHintTextStyle().Render("Loading team…"))
	case state.err != "" && len(state.status.Handles) == 0:
		body = append(body, " "+m.theme.HelpHintTextStyle().Render("Could not load team."))
	case state.guardianPicker() && budget >= 4:
		review, screening := &state.subagentList, &state.other
		if state.classifierFocused {
			review, screening = screening, review
		}
		gap := 0
		if budget >= 5 {
			gap = 1
		}
		rowsBudget := budget - 2 - gap
		classifierBudget := min(max(1, len(screening.rows)), max(1, rowsBudget/3))
		reviewBudget := min(max(1, len(review.rows)), rowsBudget-classifierBudget)
		classifierBudget = min(max(1, len(screening.rows)), rowsBudget-reviewBudget)

		body = append(body, m.subagentPaneTitle("Review model", width))
		primary = m.renderSubagentList(review, !state.classifierFocused, width, len(body), reviewBudget)
		body = append(body, primary.lines...)
		if gap > 0 {
			body = append(body, "")
		}
		body = append(body, m.subagentPaneTitle("Classifier", width))
		classifier = m.renderSubagentList(screening, state.classifierFocused, width, len(body), classifierBudget)
		body = append(body, classifier.lines...)
	default:
		if state.guardianPicker() && budget > 1 {
			title := "Review model"
			if state.classifierFocused {
				title = "Classifier"
			}
			body = append(body, m.subagentPaneTitle(title, width))
			budget--
		}
		list := m.renderSubagentList(&state.subagentList, true, width, len(body), budget)
		body = append(body, list.lines...)
		if state.classifierFocused {
			classifier = list
		} else {
			primary = list
		}
	}

	scroll := primary.scroll
	if state.classifierFocused {
		scroll = classifier.scroll
	}
	body[0] = m.renderSubagentTitle(width+2, scroll)

	geometry := m.completionOverlayFrameGeometry(chrome, len(body)-chrome.headerRows)
	style := m.theme.HelpHintTextStyle()
	if state.err != "" {
		style = m.theme.ErrorStyle()
	}
	body = m.appendModelPickerDetail(body, chrome, detail, style)
	frame := m.renderCompletionFrame(geometry, body)
	firstLine, _, _ := strings.Cut(frame, "\n")

	contentX := geometry.left
	if chrome.useBorder {
		contentX += 2
	}
	contentY := geometry.top + chrome.topBorderRows + chrome.topInsetRows
	for _, list := range [][]int{primary.rows, classifier.rows} {
		for i, offset := range list {
			if offset >= 0 {
				list[i] = contentY + offset
			}
		}
	}
	for i := range primary.efforts {
		primary.efforts[i].x += contentX
		primary.efforts[i].midpoint += contentX
	}
	state.geometry = subagentOverlayGeometry{
		x: geometry.left, y: geometry.top, closeX: overlayCloseX(contentX, width+2),
		closeY: contentY, width: displayColumns(firstLine), height: geometry.height,
		rows: primary.rows, classifierRows: classifier.rows, efforts: primary.efforts,
	}
	return m.attachCompletionFooter(frame, m.subagentOverlayFooter())
}

func (m *Model) renderSubagentTitle(width int, scroll completionScrollAffordance) string {
	state := m.subagentOverlay
	label := "/team"
	if state.page == subagentPageBinding {
		role := string(state.bindingHandle)
		for _, item := range state.status.Handles {
			if item.Definition.Handle == state.bindingHandle && item.Definition.Class == agentbinding.HandleClassSystem {
				role = firstNonEmpty(item.Definition.Name, role)
				break
			}
		}
		count := 0
		for _, list := range [][]subagentOverlayRow{state.rows, state.other.rows} {
			for _, row := range list {
				if !row.reset {
					count++
				}
			}
		}
		label = modelPickerCountLabel(label+" › "+role, count, scroll)
	}
	close := m.overlayButtonStyle(!state.pending && !state.loading, state.closeButton.hovered || state.closeButton.pressed).Render(overlayCloseLabel)
	return padRightDisplay(" "+m.theme.HelpHintTextStyle().Render(truncateTailDisplay(label, width-len(overlayCloseLabel)-2)), width-len(overlayCloseLabel)) + close
}

type subagentRenderedList struct {
	lines   []string
	rows    []int
	efforts []subagentEffortSpan
	scroll  completionScrollAffordance
}

func (m *Model) renderSubagentList(list *subagentList, focused bool, width, offset, budget int) subagentRenderedList {
	if len(list.rows) == 0 {
		text := "No configurable roles."
		if list.query != "" {
			text = "No matches. Ctrl+W clears."
		}
		return subagentRenderedList{lines: []string{" " + m.theme.HelpHintTextStyle().Render(truncateTailDisplay(text, width))}}
	}
	start, end, rows := subagentViewport(list, budget)
	rendered := subagentRenderedList{rows: rows, lines: make([]string, 0, end-start),
		scroll: completionScrollFromWindow(start, end, len(rows), list.index == len(rows)-1, false)}
	if m.subagentOverlay.page == subagentPageMain {
		cols := subagentOverviewColumns(width+2, list.rows)
		for i := start; i < end; i++ {
			row := list.rows[i]
			if i > start && budget > end-start && agentbinding.IsSystem(row.handle) && !agentbinding.IsSystem(list.rows[i-1].handle) {
				rendered.lines = append(rendered.lines, "")
			}
			rows[i] = offset + len(rendered.lines)
			rendered.lines = append(rendered.lines, m.renderSubagentOverviewRow(row, i == list.index, width+2, cols))
		}
		return rendered
	}

	table := make([]completionTableRow, 0, end-start)
	for i := start; i < end; i++ {
		row := list.rows[i]
		selected := focused && i == list.index
		mark := ""
		if row.current {
			mark = "●"
		} else if !focused && i == list.index {
			mark = "○"
		}
		table = append(table, m.modelPickerRow(row.label, row.detail, subagentPickerEffort(row, selected, width), mark, selected))
	}
	identityWidth := completionTableIdentityWidth(table, width)
	rendered.efforts = make([]subagentEffortSpan, len(list.rows))
	for i, row := range table {
		index := start + i
		rows[index] = offset + len(rendered.lines)
		rendered.lines = append(rendered.lines, m.renderCompletionColumnsLine(row.identity, row.hint, identityWidth, focused && index == list.index))
		model := list.rows[index]
		if len(model.efforts) > 1 {
			hintStart := identityWidth + 2
			unfocused := subagentPickerEffort(model, false, width)
			focusedWidth := displayColumns(subagentPickerEffort(model, true, width))
			if available := width - hintStart; available > 0 {
				rendered.efforts[index] = subagentEffortSpan{
					x: hintStart + 1, width: min(available, focusedWidth),
					midpoint: hintStart + 1 + displayColumns(unfocused) - (displayColumns(model.binding.Effort)+1)/2,
				}
			}
		}
	}
	return rendered
}

func subagentPickerEffort(row subagentOverlayRow, selected bool, width int) string {
	if row.reset || row.handle == agentbinding.HandleGuardianScreen {
		return ""
	}
	return pickerEffortControl(row.efforts, row.binding.Effort, selected, width)
}

func (m *Model) subagentOverlayDetail() string {
	state := m.subagentOverlay
	row := m.currentSubagentRow()
	switch {
	case state.err != "":
		return state.err
	case state.notice != "":
		return state.notice
	case state.page == subagentPageMain:
		if subagentOverviewColumns(m.completionRowInnerWidth()+2, state.rows).purpose == 0 {
			return subagentPurpose(row.handle)
		}
	case state.query == "" && len(state.rows) == 1 && row.reset:
		return "Use /connect to add a model."
	case row.key == "":
		return "Type to search models"
	default:
		parts := []string{row.label}
		if row.detail != "" {
			parts = append(parts, row.detail)
		}
		if row.current {
			parts = append(parts, "current")
		}
		return strings.Join(parts, "  ·  ")
	}
	return ""
}

func (m *Model) subagentOverlayFooter() string {
	state := m.subagentOverlay
	full, compact, minimal := "type search  ↑↓ select  enter edit  esc close", "↑↓ select  enter edit  esc", "↑↓  enter  esc"
	if state.page == subagentPageBinding {
		full, compact, minimal = "type search  ↑↓ select  ←→ effort  enter apply  esc back", "↑↓ select  ←→ effort  enter apply  esc", "↑↓  ←→  enter  esc"
		if state.guardianPicker() {
			full = "type search  ↑↓ select  ←→ effort  enter apply both  esc back"
		}
	}
	switch {
	case state.loading:
		full = "esc close"
	case state.pending:
		full = "Saving…"
	case state.err != "" && len(state.status.Handles) == 0:
		full = "enter retry  esc close"
	}
	return m.renderPickerFooter(full, compact, minimal)
}
