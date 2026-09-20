package tuiapp

import (
	"slices"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/control/modelprofile"
)

type subagentField uint8

const (
	subagentFieldModel subagentField = iota
	subagentFieldAuxiliary
	subagentFieldEffort
	subagentFieldFast
)

type subagentCompanion struct {
	handle agentbinding.Handle
	label  string
	value  string
}

func (c *subagentCompanion) searchText() string {
	if c == nil {
		return ""
	}
	return c.label + " " + c.value + " " + string(c.handle)
}

func (s *subagentOverlayState) subagentCompanion(handle agentbinding.Handle) *subagentCompanion {
	c := &subagentCompanion{value: "Disabled"}
	switch handle {
	case agentbinding.HandleGuardian:
		c.handle, c.label = agentbinding.HandleGuardianScreen, "Classifier"
		if !slices.ContainsFunc(s.status.Targets, func(p modelprofile.ModelProfile) bool {
			return agentbinding.SupportsProfile(c.handle, p)
		}) {
			return nil
		}
	case agentbinding.HandleSteward:
		c.handle, c.label = agentbinding.HandleMemoryVerifier, "Verifier"
	default:
		return nil
	}
	for _, item := range s.status.Handles {
		if item.Definition.Handle == c.handle && agentbinding.IsBound(item) {
			c.value = subagentProfileDisplayName(item.Profile)
		}
	}
	return c
}

// Cell offsets are relative to row content. Rendering and mouse hit testing use
// the same layout, including clipped cells in narrow terminals.
type subagentCell struct {
	field    subagentField
	x, width int
	text     string
	muted    bool
}

func subagentLabelWidth(width int) int {
	return min(20, max(6, width/4), max(1, width-10))
}

func (m *Model) subagentCells(row subagentOverlayRow, width int) []subagentCell {
	state := m.subagentOverlay
	if state.page == subagentPageBinding {
		label := row.label
		if row.reset {
			label += " · " + row.detail
		}
		if row.binding.ProfileID != "" && row.detail != "" {
			label += " (" + row.detail + ")"
		}
		marker := "  "
		if row.current {
			marker = "● "
		}
		modelWidth, effortWidth, fastWidth := m.subagentBindingColumns(width)
		if row.reset {
			modelWidth = width
		}

		cells := []subagentCell{{field: subagentFieldModel, width: modelWidth, text: marker + label}}
		if effortWidth > 0 && row.binding.ProfileID != "" && (row.binding.Effort != "none" || len(row.efforts) > 1) {
			value := pickerEffortControl(row.efforts, row.binding.Effort, len(row.efforts) > 1, width)
			cells = append(cells, subagentCell{field: subagentFieldEffort, x: modelWidth + 1, width: effortWidth, text: value, muted: true})
		}
		if fastWidth > 0 && row.fastSupported {
			cells = append(cells, subagentCell{field: subagentFieldFast, x: modelWidth + effortWidth + 1 + min(1, effortWidth), width: fastWidth, text: subagentFastLabel(row.fastMode), muted: true})
		}
		return cells
	}
	start := subagentLabelWidth(width) + 2
	available := max(1, width-start)
	mainWidth, auxiliaryWidth := 1, 0
	for _, item := range state.rows {
		mainWidth = max(mainWidth, displayColumns(item.detail))
		if item.companion != nil {
			auxiliaryWidth = max(auxiliaryWidth, displayColumns(item.companion.label)+displayColumns(item.companion.value)+3)
		}
	}
	mainWidth = min(56, mainWidth, available)
	if auxiliaryWidth > 0 {
		auxiliaryWidth = min(40, auxiliaryWidth, max(1, available/2))
		mainWidth = min(mainWidth, max(1, available-auxiliaryWidth-2))
	}
	if row.companion != nil {
		return []subagentCell{
			{field: subagentFieldModel, x: start, width: mainWidth, text: row.detail},
			{field: subagentFieldAuxiliary, x: start + mainWidth + 2, width: auxiliaryWidth, text: row.companion.label + " " + row.companion.value + " ▾"},
		}
	}
	return []subagentCell{{field: subagentFieldModel, x: start, width: mainWidth, text: row.detail}}
}

// Every model shares the same column boundaries, including models without
// optional controls. Spare terminal width stays outside the cells.
func (m *Model) subagentBindingColumns(width int) (int, int, int) {
	modelWidth, effortWidth, fastWidth := 24, 0, 0
	for _, row := range m.subagentOverlay.rows {
		if row.binding.ProfileID == "" {
			continue
		}
		labelWidth := displayColumns(row.label) + 4
		if row.detail != "" {
			labelWidth += displayColumns(row.detail) + 3
		}
		modelWidth = max(modelWidth, labelWidth)
		if row.binding.Effort != "none" || len(row.efforts) > 1 {
			effortWidth = 12
			if width >= 90 {
				effortWidth = 22
			}
		}
		if row.fastSupported {
			fastWidth = 8
		}
	}
	// Reserve visible controls and at least a short model label on small screens.
	minimumModel := min(10, max(3, width/3))
	fastWidth = min(fastWidth, max(0, width/3))
	gaps := min(1, effortWidth) + min(1, fastWidth)
	effortWidth = min(effortWidth, max(0, width-minimumModel-fastWidth-gaps))
	gaps = min(1, effortWidth) + min(1, fastWidth)
	modelWidth = min(52, modelWidth, max(1, width-effortWidth-fastWidth-gaps))
	return modelWidth, effortWidth, fastWidth
}

func subagentFastLabel(fast bool) string {
	if fast {
		return "Fast on"
	}
	return "Fast off"
}

func (m *Model) subagentFields() []subagentField {
	row := m.currentSubagentRow()
	fields := []subagentField{subagentFieldModel}
	if m.subagentOverlay.page == subagentPageMain && row.companion != nil {
		fields = append(fields, subagentFieldAuxiliary)
	}
	if m.subagentOverlay.page == subagentPageBinding {
		if len(row.efforts) > 1 {
			fields = append(fields, subagentFieldEffort)
		}
		if row.fastSupported {
			fields = append(fields, subagentFieldFast)
		}
	}
	return fields
}

func (m *Model) normalizeSubagentField() {
	if !slices.Contains(m.subagentFields(), m.subagentOverlay.field) {
		m.subagentOverlay.field = subagentFieldModel
	}
}

func (m *Model) moveSubagentField(delta int) {
	fields := m.subagentFields()
	index := max(0, slices.Index(fields, m.subagentOverlay.field))
	m.subagentOverlay.field = fields[(index+delta+len(fields))%len(fields)]
	m.subagentOverlay.pressedKey = ""
}

func (m *Model) renderSubagentRow(row subagentOverlayRow, selected bool, width int) string {
	state := m.subagentOverlay
	innerWidth := max(1, width-2)
	line := ""
	if state.page != subagentPageBinding {
		marker := "· "
		if selected {
			marker = "› "
		}
		style := m.theme.TextStyle()
		if !row.enabled {
			style = m.theme.HelpHintTextStyle()
		}
		line = style.Render(padRightDisplay(truncateTailDisplay(marker+row.label, subagentLabelWidth(innerWidth)), subagentLabelWidth(innerWidth)))
	}
	for _, cell := range m.subagentCells(row, innerWidth) {
		line += strings.Repeat(" ", max(0, cell.x-displayColumns(line)))
		value := cell.text
		if selected && m.editingSubagentField() {
			value = truncateDisplayCellsFromEnd(value, max(1, cell.width-1)) + "▏"
		}
		value = padRightDisplay(ansi.Truncate(value, cell.width, "…"), cell.width)
		style := m.theme.TextStyle()
		if cell.muted || !row.enabled {
			style = m.theme.HelpHintTextStyle()
		} else if cell.field == subagentFieldAuxiliary {
			style = m.theme.KeyLabelStyle()
		}
		if selected && state.field == cell.field {
			style = m.theme.SelectionStyle()
		}
		if !selected || state.field != cell.field {
			if suffix := strings.Index(value, " ["); suffix > 0 {
				line += style.Render(value[:suffix]) + m.theme.HelpHintTextStyle().Render(value[suffix:])
				continue
			}
		}
		line += style.Render(value)
	}
	return m.renderCompletionUnselectedLine(innerWidth, line)
}
