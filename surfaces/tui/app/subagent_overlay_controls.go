package tuiapp

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/agentbinding"
)

func (m *Model) focusSubagentCell(index, x int) (subagentField, int) {
	state := m.subagentOverlay
	field, delta := subagentFieldModel, 0
	if index >= 0 && index < len(state.geometry.cells) {
		for _, cell := range state.geometry.cells[index] {
			if x >= cell.x && x < cell.x+cell.width {
				field = cell.field
				if field == subagentFieldEffort {
					delta = -1
					if x >= cell.x+cell.width/2 {
						delta = 1
					}
				}
				break
			}
		}
	}
	state.field = field
	m.normalizeSubagentField()
	return field, delta
}

// Picker edits remain a draft until applied. Main-row shortcuts update the
// explicit binding through the same Control mutation used by the picker.
func (m *Model) adjustSubagentControl(delta int, toggleFast bool) tea.Cmd {
	state := m.subagentOverlay
	if state == nil || state.pending || state.loading || state.field == subagentFieldAuxiliary ||
		(state.page != subagentPageMain && state.page != subagentPageBinding) {
		return nil
	}
	row := m.currentSubagentRow()
	if !row.enabled || row.binding.ProfileID == "" {
		return nil
	}
	changeFast := toggleFast || state.field == subagentFieldFast
	if changeFast {
		if !row.fastSupported {
			return nil
		}
		fast := delta > 0
		if toggleFast {
			fast = !row.fastMode
		}
		row.binding.Speed = "standard"
		if fast {
			row.binding.Speed = "fast"
		}
		row.fastMode = fast
	} else {
		if len(row.efforts) < 2 || delta == 0 {
			return nil
		}
		index := clampInt(row.effortIndex+delta, 0, len(row.efforts)-1)
		if index == row.effortIndex {
			return nil
		}
		row.effortIndex, row.binding.Effort = index, row.efforts[index]
	}
	state.pressedKey = ""
	if state.page == subagentPageMain {
		state.afterMutation = &subagentOverlayNav{page: state.page, index: state.index, key: row.key, query: state.query, depth: len(state.parents), field: state.field}
		return m.runSubagentMutation(func(ctx context.Context, service agentbinding.ConfigurationService) (agentbinding.Status, error) {
			return service.BindAgentBinding(ctx, row.binding)
		})
	}
	if changeFast {
		if state.selectedSpeedByProfile == nil {
			state.selectedSpeedByProfile = map[string]string{}
		}
		state.selectedSpeedByProfile[row.binding.ProfileID] = row.binding.Speed
	} else {
		if state.selectedEffortByProfile == nil {
			state.selectedEffortByProfile = map[string]string{}
		}
		state.selectedEffortByProfile[row.binding.ProfileID] = row.binding.Effort
	}
	state.rows[state.index] = row
	return nil
}
