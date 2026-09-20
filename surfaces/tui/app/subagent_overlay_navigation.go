package tuiapp

import "strings"

func (s *subagentOverlayState) searchable() bool {
	return s.page == subagentPageMain || s.page == subagentPageBinding || s.page == subagentPageSets
}

func (m *Model) subagentRows() []subagentOverlayRow {
	rows := m.allSubagentRows()
	state := m.subagentOverlay
	if state == nil || !state.searchable() || strings.TrimSpace(state.query) == "" {
		return rows
	}
	query := strings.ToLower(strings.TrimSpace(state.query))
	filtered := make([]subagentOverlayRow, 0, len(rows))
	for _, row := range rows {
		text := strings.ToLower(row.label + " " + row.detail + " " + row.companion.searchText() + " " + row.search + " " + string(row.handle))
		if strings.Contains(text, query) {
			filtered = append(filtered, row)
		}
	}
	return filtered
}

func (m *Model) refreshSubagentRows(key string) {
	state := m.subagentOverlay
	state.rows = m.subagentRows()
	state.index = clampInt(state.index, 0, max(0, len(state.rows)-1))
	for i, row := range state.rows {
		if key != "" && row.key == key {
			state.index = i
			break
		}
	}
	m.normalizeSubagentField()
}

func (m *Model) searchSubagentRows(query string) {
	state := m.subagentOverlay
	key := m.currentSubagentRow().key
	state.query = truncateRunes(query, 160)
	state.index = 0
	state.windowStart = 0
	state.pressedKey = ""
	state.notice = ""
	m.refreshSubagentRows(key)
}

func (m *Model) openSubagentPage(page subagentOverlayPage, index int) {
	state := m.subagentOverlay
	if state == nil {
		return
	}
	state.parents = append(state.parents, subagentOverlayNav{
		page: state.page, index: state.index, query: state.query,
		key: m.currentSubagentRow().key, depth: len(state.parents), field: state.field,
	})
	m.restoreSubagentPage(subagentOverlayNav{page: page, index: index, depth: len(state.parents)})
	if page == subagentPageBinding {
		for i, row := range state.rows {
			if row.current {
				state.index = i
				break
			}
		}
	}
}

func (m *Model) restoreSubagentPage(nav subagentOverlayNav) {
	state := m.subagentOverlay
	state.parents = state.parents[:min(nav.depth, len(state.parents))]
	state.page, state.index, state.query, state.field = nav.page, nav.index, nav.query, nav.field
	state.windowStart = 0
	state.err, state.notice, state.pressedKey = "", "", ""
	m.refreshSubagentRows(nav.key)
}

func (m *Model) backSubagentOverlay() {
	state := m.subagentOverlay
	if state == nil || state.pending {
		return
	}
	if len(state.parents) == 0 {
		m.subagentOverlay = nil
		return
	}
	if state.page == subagentPageBinding {
		state.creatingRole = false
		state.selectedEffortByProfile = nil
	}
	m.restoreSubagentPage(state.parents[len(state.parents)-1])
}

func (s *subagentOverlayState) navigateAfterMutation(page subagentOverlayPage, index int) {
	s.afterMutation = &subagentOverlayNav{page: page, index: index, depth: len(s.parents)}
	for i := len(s.parents) - 1; i >= 0; i-- {
		if s.parents[i].page == page {
			nav := s.parents[i]
			s.afterMutation = &nav
			return
		}
	}
}
