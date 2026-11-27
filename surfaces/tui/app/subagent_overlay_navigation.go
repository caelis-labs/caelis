package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
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
	state.stagedBinding = agentbinding.Binding{}
	state.hasClassifiers = handle == agentbinding.HandleGuardian && len(m.subagentBindingRows(agentbinding.HandleGuardianScreen)) > 1
	m.restoreSubagentPage(subagentPageBinding, subagentOverlayNav{})
	m.selectCurrentSubagentBinding(handle)
}

func (m *Model) selectCurrentSubagentBinding(handle agentbinding.Handle) {
	state := m.subagentOverlay
	if state == nil {
		return
	}
	currentID := modelprofile.NormalizeID(state.currentBinding(handle).ProfileID)
	for i, row := range state.rows {
		if currentID == "" && row.reset {
			state.index = i
			return
		}
		if currentID != "" && modelprofile.NormalizeID(row.binding.ProfileID) == currentID {
			state.index = i
			return
		}
	}
}

func (m *Model) restoreSubagentPage(page subagentOverlayPage, nav subagentOverlayNav) {
	state := m.subagentOverlay
	state.page, state.index, state.query = page, nav.index, nav.query
	state.windowStart = 0
	state.err, state.notice, state.pressedKey = "", "", ""
	m.refreshSubagentRows(nav.key)
}

func (m *Model) returnToSubagentMain() {
	state := m.subagentOverlay
	if state == nil {
		return
	}
	state.selectedEffortByProfile = nil
	state.stagedBinding = agentbinding.Binding{}
	state.hasClassifiers = false
	state.bindingParent = subagentOverlayNav{}
	m.restoreSubagentPage(subagentPageMain, state.parent)
}

func (m *Model) backSubagentOverlay() {
	state := m.subagentOverlay
	if state == nil || state.pending {
		return
	}
	switch state.page {
	case subagentPageMain:
		m.subagentOverlay = nil
	case subagentPageClassifier:
		m.restoreSubagentPage(subagentPageBinding, state.bindingParent)
	case subagentPageBinding:
		m.returnToSubagentMain()
	}
}
