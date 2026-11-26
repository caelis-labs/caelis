package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
)

func (m *Model) subagentRows() []subagentOverlayRow {
	rows := m.allSubagentRows()
	state := m.subagentOverlay
	if state == nil || strings.TrimSpace(state.query) == "" {
		return rows
	}
	query := strings.ToLower(strings.TrimSpace(state.query))
	filtered := make([]subagentOverlayRow, 0, len(rows))
	for _, row := range rows {
		text := strings.ToLower(row.label + " " + row.detail + " " + row.binding.Effort + " " + row.search + " " + string(row.handle))
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

func (m *Model) openSubagentBinding(handle agentbinding.Handle) {
	state := m.subagentOverlay
	state.parent = subagentOverlayNav{
		index: state.index, key: m.currentSubagentRow().key, query: state.query,
	}
	state.bindingHandle = handle
	m.restoreSubagentPage(subagentPageBinding, subagentOverlayNav{})
	state.selectCurrent()
	if handle == agentbinding.HandleGuardian {
		rows := m.subagentBindingRows(agentbinding.HandleGuardianScreen)
		if len(rows) > 1 {
			state.classifierAvailable = true
			state.draft = m.currentSubagentRow().binding
			state.other = subagentList{rows: rows}
			state.other.selectCurrent()
			state.other.draft = state.other.rows[state.other.index].binding
		}
	}
}

func (list *subagentList) selectCurrent() {
	for i, row := range list.rows {
		if row.current {
			list.index = i
			return
		}
	}
}

func (m *Model) focusSubagentClassifier(focus bool) {
	state := m.subagentOverlay
	if !state.guardianPicker() || state.classifierFocused == focus {
		return
	}
	// The filter belongs to the focused section. Restore the previous
	// section's draft to view before moving focus to the other section.
	state.query = ""
	m.refreshSubagentRows("")
	for i, row := range state.rows {
		if row.binding == state.draft {
			state.index = i
			break
		}
	}
	state.subagentList, state.other = state.other, state.subagentList
	state.classifierFocused = focus
	state.pressedKey = ""
}

func (m *Model) restoreSubagentPage(page subagentOverlayPage, nav subagentOverlayNav) {
	state := m.subagentOverlay
	state.page, state.index, state.query = page, nav.index, nav.query
	state.windowStart = 0
	state.err, state.notice, state.pressedKey = "", "", ""
	m.refreshSubagentRows(nav.key)
}

func (m *Model) backSubagentOverlay() {
	state := m.subagentOverlay
	if state == nil || state.pending {
		return
	}
	if state.page == subagentPageMain {
		m.subagentOverlay = nil
		return
	}
	state.selectedEffortByProfile = nil
	state.draft = agentbinding.Binding{}
	state.other = subagentList{}
	state.classifierFocused = false
	state.classifierAvailable = false
	m.restoreSubagentPage(subagentPageMain, state.parent)
}
