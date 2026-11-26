package tuiapp

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type modelPickerDraft struct {
	effort string
	fast   bool
}

// modelPickerState contains only unconfirmed edits. Capabilities and the
// effective selection come from Control's completion projection.
type modelPickerState struct {
	drafts     map[string]modelPickerDraft
	selected   string
	fastFocus  bool
	loadFailed bool
}

func (m *Model) isModelPicker() bool {
	if !m.slashArgActive || m.slashArgCommand != "model" || m.isWizardActive() {
		return false
	}
	// Older Hosts omit model_selection. Their candidates keep the existing
	// argument completion flow until they supply the typed picker projection.
	return len(m.slashArgCandidates) == 0 || m.slashArgCandidates[0].ModelSelection != nil
}

func (m *Model) updateModelPickerCandidates(candidates []SlashArgCandidate, err error) {
	if m.modelPicker == nil {
		m.modelPicker = &modelPickerState{drafts: make(map[string]modelPickerDraft)}
	}
	m.modelPicker.loadFailed = err != nil
	if m.modelPicker.selected == "" {
		for _, candidate := range candidates {
			if candidate.ModelSelection != nil && candidate.ModelSelection.Current {
				m.modelPicker.selected = candidate.Value
				break
			}
		}
	}
}

func (m *Model) restoreModelPickerSelection(candidates []SlashArgCandidate) {
	for i, candidate := range candidates {
		if candidate.Value == m.modelPicker.selected {
			m.slashArgIndex = i
			return
		}
	}
	if len(candidates) > 0 {
		m.slashArgIndex = 0
		m.modelPicker.selected = candidates[0].Value
	}
}

func (m *Model) modelPickerDraft(candidate SlashArgCandidate) modelPickerDraft {
	if m.modelPicker != nil {
		if draft, ok := m.modelPicker.drafts[candidate.Value]; ok {
			return draft
		}
	}
	if selection := candidate.ModelSelection; selection != nil {
		return modelPickerDraft{effort: selection.Effort, fast: selection.Fast}
	}
	return modelPickerDraft{}
}

func (m *Model) handleModelPickerKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	candidate, ok := m.currentSlashArgCandidate()
	if !ok || candidate.ModelSelection == nil {
		switch msg.String() {
		case "enter", "tab", "shift+tab", "left", "right":
			if msg.String() == "enter" && m.modelPicker != nil && m.modelPicker.loadFailed {
				return true, m.requestCurrentSlashArgCompletion()
			}
			return true, nil
		}
		return false, nil
	}
	selection := candidate.ModelSelection
	if m.modelPicker == nil {
		m.updateModelPickerCandidates(m.slashArgCandidates, nil)
	}
	switch {
	case key.Matches(msg, m.keys.Complete), msg.String() == "shift+tab":
		m.modelPicker.fastFocus = selection.FastSupported && !m.modelPicker.fastFocus
		return true, nil
	case msg.String() == "left", msg.String() == "right":
		draft := m.modelPickerDraft(candidate)
		if m.modelPicker.fastFocus && selection.FastSupported {
			draft.fast = !draft.fast
		} else if index := slices.Index(selection.Efforts, draft.effort); index >= 0 {
			delta := 1
			if msg.String() == "left" {
				delta = -1
			}
			draft.effort = selection.Efforts[min(max(index+delta, 0), len(selection.Efforts)-1)]
		}
		m.modelPicker.drafts[candidate.Value] = draft
		return true, nil
	case key.Matches(msg, m.keys.Accept):
		if m.turnRunning() || !m.slashArgCompletionSettledForCurrentTarget() {
			return true, nil
		}
		draft := m.modelPickerDraft(candidate)
		line := "/model " + candidate.Value + " " + draft.effort
		if draft.fast && selection.FastSupported {
			line += " fast"
		}
		m.clearSlashArg()
		_, cmd := m.submitLine(line)
		return true, cmd
	}
	return false, nil
}

func (m *Model) renderModelPicker(geometry completionOverlayGeometry, candidates []SlashArgCandidate) string {
	width := m.completionRowInnerWidth()
	label := modelPickerCountLabel("/model", len(candidates), geometry.scroll)
	lines := []string{" " + m.theme.HelpHintTextStyle().Render(truncateTailDisplay(label, width))}
	rows := make([]completionTableRow, 0, geometry.candidateCount)
	for i := geometry.windowStart; i < geometry.windowEnd; i++ {
		candidate := candidates[i]
		mark := ""
		if candidate.ModelSelection != nil && candidate.ModelSelection.Current {
			mark = "●"
		}
		identity := firstNonEmpty(candidate.Display, candidate.Value)
		note := firstNonEmpty(candidate.Detail, slashArgPickerHint("model", candidates, i))
		selected := i == geometry.selected
		rows = append(rows, m.modelPickerRow(identity, note, m.modelPickerControls(candidate, selected, width), mark, selected))
	}
	identityWidth := completionTableIdentityWidth(rows, width)
	for i, row := range rows {
		lines = append(lines, m.renderCompletionColumnsLine(row.identity, row.hint, identityWidth, geometry.windowStart+i == geometry.selected))
	}
	detail := "Type to search models"
	if m.slashArgRequestPending {
		detail = "Loading models…"
	} else if m.modelPicker != nil && m.modelPicker.loadFailed {
		detail = "Could not load models. Enter to retry."
	} else if len(candidates) == 0 {
		detail = "No matching models. Edit the search or Esc to cancel."
	} else if geometry.selected < len(candidates) {
		candidate := candidates[geometry.selected]
		detail = firstNonEmpty(candidate.Display, candidate.Value)
		if candidate.Detail != "" {
			detail += "  ·  " + candidate.Detail
		}
		if selection := candidate.ModelSelection; selection != nil {
			if selection.Current {
				detail += "  ·  current"
			}
			if selection.ContextWindowTokens > 0 {
				detail += fmt.Sprintf("  ·  %gk context", float64(selection.ContextWindowTokens)/1000)
			}
		}
	}
	lines = m.appendModelPickerDetail(lines, geometry.chrome, detail, m.theme.HelpHintTextStyle())
	return m.renderCompletionOverlay(geometry, lines)
}

func modelPickerCountLabel(path string, count int, scroll completionScrollAffordance) string {
	noun := "models"
	if count == 1 {
		noun = "model"
	}
	label := fmt.Sprintf("%s  ·  %d %s", path, count, noun)
	if scroll.CanUp {
		label += "  ↑ more"
	}
	if scroll.CanDown {
		label += "  ↓ more"
	}
	return label
}

func (m *Model) modelPickerRow(label, note, controls, mark string, selected bool) completionTableRow {
	marker := "· "
	if selected {
		marker = "› "
	}
	if mark != "" {
		marker += mark + " "
	}
	if note != "" {
		label += "  " + m.theme.HelpHintTextStyle().Bold(false).Render(note)
	}
	return completionTableRow{identity: marker + label, hint: controls}
}

func (m *Model) appendModelPickerDetail(lines []string, chrome completionOverlayChrome, detail string, style lipgloss.Style) []string {
	width := m.completionRowInnerWidth()
	if chrome.detailRows > 1 {
		lines = append(lines, " "+m.theme.HelpHintTextStyle().Render(strings.Repeat("─", width)))
	}
	if chrome.detailRows > 0 {
		lines = append(lines, " "+style.Render(truncateTailDisplay(detail, width)))
	}
	return lines
}

func (m *Model) modelPickerControls(candidate SlashArgCandidate, selected bool, width int) string {
	selection := candidate.ModelSelection
	if selection == nil {
		return ""
	}
	draft := m.modelPickerDraft(candidate)
	fastFocus := selected && m.modelPicker != nil && m.modelPicker.fastFocus && selection.FastSupported
	effort := pickerEffortControl(selection.Efforts, draft.effort, selected && !fastFocus, width)
	if selection.FastSupported {
		fast := "Fast off"
		if draft.fast {
			fast = "Fast on"
		}
		if fastFocus {
			fast = "‹ " + fast + " ›"
		}
		effort = padRightDisplay(effort, 18) + "  " + fast
	}
	return effort
}

func (m *Model) renderModelPickerFooter() string {
	label := "type search  ↑↓ select  ←→ effort  enter apply  esc cancel"
	compact := "↑↓ select  ←→ effort  enter apply  esc"
	if candidate, ok := m.currentSlashArgCandidate(); ok && candidate.ModelSelection != nil && candidate.ModelSelection.FastSupported {
		label = "type search  ↑↓ select  tab field  ←→ change  enter apply  esc cancel"
		compact = "↑↓ select  tab field  ←→ change  enter apply  esc"
	}
	return m.renderPickerFooter(label, compact, "↑↓  ←→  tab  enter  esc")
}

func (m *Model) renderPickerFooter(label, compact, minimal string) string {
	width := m.completionOverlayInnerWidth() - m.completionOverlayFooterIndent()
	if displayColumns(label) > width {
		label = compact
	}
	if displayColumns(label) > width {
		label = minimal
	}
	return m.theme.HelpHintTextStyle().Render(truncateTailDisplay(label, max(1, width)))
}

func pickerEffortControl(efforts []string, effort string, focused bool, width int) string {
	value := effort
	if focused && len(efforts) > 1 {
		value = "‹ " + value + " ›"
	}
	if width >= 90 && len(efforts) > 1 {
		index := slices.Index(efforts, effort)
		value = strings.Repeat("■", max(0, index+1)) + strings.Repeat("□", max(0, len(efforts)-index-1)) + "  " + value
	}
	return value
}
