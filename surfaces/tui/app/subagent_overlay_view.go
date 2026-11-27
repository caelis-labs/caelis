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
	contentWidth := width + 2
	chrome := m.modelPickerChrome()
	chrome.detailRows = 0
	detail := m.subagentOverlayDetail()
	if detail != "" || state.loading || (state.err != "" && len(state.status.Handles) == 0) {
		chrome.detailRows = 2
		if m.height > 0 && m.height < 20 {
			chrome.detailRows = 1
		}
	}
	budget := m.completionOverlayBodyLimitFor(chrome, pickerDrawerVisibleItems)

	var rendered subagentRenderedList
	var body []string
	var ancestors []string
	var title string

	switch state.page {
	case subagentPageBinding:
		ancestors = []string{"/team"}
		title = m.subagentRoleDisplayName(state.bindingHandle)
	case subagentPageClassifier:
		ancestors = []string{"/team", m.subagentRoleDisplayName(state.bindingHandle)}
		title = "Classifier"
	default:
		title = "/team"
	}

	switch {
	case state.loading:
		body = m.pickerDrawerHeaderLines(contentWidth, ancestors, title, completionScrollAffordance{}, chrome.headerRows > 1)
		body = append(body, " "+m.theme.HelpHintTextStyle().Render(truncateTailDisplay("Loading team…", width))+" ")
	case state.err != "" && len(state.status.Handles) == 0:
		body = m.pickerDrawerHeaderLines(contentWidth, ancestors, title, completionScrollAffordance{}, chrome.headerRows > 1)
		body = append(body, " "+m.theme.HelpHintTextStyle().Render(truncateTailDisplay("Could not load team.", width))+" ")
	default:
		rendered = m.renderSubagentList(&state.subagentList, contentWidth, chrome.headerRows, budget)
		body = m.pickerDrawerHeaderLines(contentWidth, ancestors, title, rendered.scroll, chrome.headerRows > 1)
		body = append(body, rendered.lines...)
	}

	geometry := m.completionOverlayFrameGeometry(chrome, len(body)-chrome.headerRows)
	style := m.theme.HelpHintTextStyle()
	if state.err != "" {
		style = m.theme.ErrorStyle()
	}
	if chrome.detailRows > 0 {
		body = m.appendModelPickerDetail(body, chrome, detail, style)
	}
	frame := m.renderCompletionFrame(geometry, body)
	firstLine, _, _ := strings.Cut(frame, "\n")

	contentX := geometry.left
	if chrome.useBorder {
		contentX += 2
	}
	contentY := geometry.top + chrome.topBorderRows + chrome.topInsetRows
	for i, offset := range rendered.rows {
		if offset >= 0 {
			rendered.rows[i] = contentY + offset
		}
	}
	for i := range rendered.efforts {
		rendered.efforts[i].x += contentX
		rendered.efforts[i].midpoint += contentX
	}
	state.geometry = subagentOverlayGeometry{
		x: geometry.left, y: geometry.top,
		width: displayColumns(firstLine), height: geometry.height,
		rows: rendered.rows, efforts: rendered.efforts,
	}
	return m.attachCompletionFooter(frame, m.subagentOverlayFooter())
}

func (m *Model) subagentRoleDisplayName(handle agentbinding.Handle) string {
	state := m.subagentOverlay
	role := string(handle)
	if state == nil {
		return role
	}
	for _, item := range state.status.Handles {
		if item.Definition.Handle == handle && item.Definition.Class == agentbinding.HandleClassSystem {
			return firstNonEmpty(item.Definition.Name, role)
		}
	}
	return role
}

type subagentRenderedList struct {
	lines   []string
	rows    []int
	efforts []pickerEffortHit
	scroll  completionScrollAffordance
}

func (m *Model) renderSubagentList(list *subagentList, contentWidth, offset, budget int) subagentRenderedList {
	if len(list.rows) == 0 {
		text := "No configurable roles."
		if list.query != "" {
			text = "No matches. Ctrl+W clears."
		}
		return subagentRenderedList{lines: []string{" " + m.theme.HelpHintTextStyle().Render(truncateTailDisplay(text, max(1, contentWidth-2))) + " "}}
	}
	start, end, rows := subagentViewport(list, budget)
	rendered := subagentRenderedList{
		rows:   rows,
		lines:  make([]string, 0, end-start),
		scroll: completionScrollFromWindow(start, end, len(rows), list.index == len(rows)-1, false),
	}

	switch m.subagentOverlay.page {
	case subagentPageMain:
		cols := subagentOverviewColumns(contentWidth, list.rows)
		classifierName := m.subagentOverlay.boundClassifierName()
		for i := start; i < end; i++ {
			rows[i] = offset + len(rendered.lines)
			rendered.lines = append(rendered.lines, m.renderSubagentOverviewRow(list.rows[i], i == list.index, contentWidth, cols, classifierName))
		}
		return rendered
	case subagentPageClassifier:
		for i := start; i < end; i++ {
			row := list.rows[i]
			rows[i] = offset + len(rendered.lines)
			rendered.lines = append(rendered.lines, m.renderPickerSimpleRow(row.label, row.detail, i == list.index, contentWidth, 0))
		}
		return rendered
	}

	specs := make([]pickerModelRowSpec, len(list.rows))
	for i, row := range list.rows {
		specs[i] = pickerModelRowSpec{
			name:        row.label,
			source:      row.detail,
			efforts:     row.efforts,
			effort:      row.binding.Effort,
			effortIndex: row.effortIndex,
		}
	}
	layout := newPickerModelTableLayout(contentWidth, specs)
	rendered.efforts = make([]pickerEffortHit, len(list.rows))
	for i := start; i < end; i++ {
		rows[i] = offset + len(rendered.lines)
		line, hit := m.renderPickerModelRow(layout, specs[i], i == list.index)
		rendered.lines = append(rendered.lines, line)
		rendered.efforts[i] = hit
	}
	return rendered
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
	case state.page == subagentPageBinding && len(state.rows) == 0 && state.query == "":
		return "Use /connect to add a model."
	case len(state.rows) == 0 && state.query != "":
		return "No matches. Ctrl+W clears."
	}
	return ""
}

func (m *Model) subagentOverlayFooter() string {
	state := m.subagentOverlay
	width := m.completionOverlayInnerWidth() - m.completionOverlayFooterIndent()
	switch {
	case state.loading:
		return m.renderPickerKeycapFooter([]pickerKeycapItem{{"esc", "close"}}, nil, nil, width)
	case state.pending:
		return m.theme.Tokens().ListMeta.Render(truncateTailDisplay("Saving…", max(1, width)))
	case state.reloadRequired:
		return m.renderPickerKeycapFooter(
			[]pickerKeycapItem{{"enter", "reload"}, {"esc", "close"}},
			[]pickerKeycapItem{{"enter", "reload"}, {"esc", ""}},
			[]pickerKeycapItem{{"enter", "reload"}},
			width,
		)
	case state.err != "" && len(state.status.Handles) == 0:
		return m.renderPickerKeycapFooter(
			[]pickerKeycapItem{{"enter", "retry"}, {"esc", "close"}},
			[]pickerKeycapItem{{"enter", "retry"}, {"esc", ""}},
			[]pickerKeycapItem{{"enter", "retry"}},
			width,
		)
	}

	switch state.page {
	case subagentPageBinding:
		apply := "apply"
		if state.hasClassifiers {
			apply = "next"
		}
		return m.renderPickerKeycapFooter(
			[]pickerKeycapItem{{"type", "search"}, {"↑↓", "select"}, {"←→", "effort"}, {"enter", apply}, {"esc", "back"}},
			[]pickerKeycapItem{{"↑↓", "select"}, {"←→", "effort"}, {"enter", apply}, {"esc", ""}},
			[]pickerKeycapItem{{"↑↓", ""}, {"←→", ""}, {"enter", ""}, {"esc", ""}},
			width,
		)
	case subagentPageClassifier:
		return m.renderPickerKeycapFooter(
			[]pickerKeycapItem{{"type", "search"}, {"↑↓", "select"}, {"enter", "apply"}, {"esc", "back"}},
			[]pickerKeycapItem{{"↑↓", "select"}, {"enter", "apply"}, {"esc", ""}},
			[]pickerKeycapItem{{"↑↓", ""}, {"enter", ""}, {"esc", ""}},
			width,
		)
	default:
		return m.renderPickerKeycapFooter(
			[]pickerKeycapItem{{"type", "search"}, {"↑↓", "select"}, {"enter", "edit"}, {"esc", "close"}},
			[]pickerKeycapItem{{"↑↓", "select"}, {"enter", "edit"}, {"esc", ""}},
			[]pickerKeycapItem{{"↑↓", ""}, {"enter", ""}, {"esc", ""}},
			width,
		)
	}
}
