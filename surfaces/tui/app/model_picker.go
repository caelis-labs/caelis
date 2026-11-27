package tuiapp

import (
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type modelPickerStep uint8

const (
	modelPickerStepModel modelPickerStep = iota
	modelPickerStepSpeed
)

type modelPickerDraft struct {
	effort string
	fast   bool
}

// modelPickerState contains only unconfirmed edits. Capabilities and the
// effective selection come from Control's completion projection.
type modelPickerState struct {
	step           modelPickerStep
	drafts         map[string]modelPickerDraft
	selected       string
	loadFailed     bool
	speedCandidate SlashArgCandidate
	speedIndex     int
	hitStart       int
	effortHits     []pickerEffortHit
}

func (m *Model) isModelPicker() bool {
	if !m.slashArgActive || m.slashArgCommand != "model" || m.isWizardActive() {
		return false
	}
	// Older Hosts omit model_selection. Their candidates keep the existing
	// argument completion flow until they supply the typed picker projection.
	return len(m.slashArgCandidates) == 0 || m.slashArgCandidates[0].ModelSelection != nil
}

func (m *Model) isModelPickerSpeedStep() bool {
	return m.isModelPicker() && m.modelPicker != nil && m.modelPicker.step == modelPickerStepSpeed
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

func (m *Model) adjustModelPickerEffort(candidate SlashArgCandidate, delta int) bool {
	selection := candidate.ModelSelection
	if selection == nil || len(selection.Efforts) < 2 || delta == 0 {
		return false
	}
	if m.modelPicker == nil {
		m.updateModelPickerCandidates(m.slashArgCandidates, nil)
	}
	draft := m.modelPickerDraft(candidate)
	index := slices.Index(selection.Efforts, draft.effort)
	if index < 0 {
		index = 0
	}
	next := min(max(index+delta, 0), len(selection.Efforts)-1)
	draft.effort = selection.Efforts[next]
	m.modelPicker.drafts[candidate.Value] = draft
	return next != index
}

func (m *Model) submitModelSelection(candidate SlashArgCandidate, effort string, fast bool) tea.Cmd {
	line := "/model " + candidate.Value + " " + effort
	if fast && candidate.ModelSelection != nil && candidate.ModelSelection.FastSupported {
		line += " fast"
	}
	m.clearSlashArg()
	_, cmd := m.submitLine(line)
	return cmd
}

func (m *Model) handleModelPickerKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if m.isModelPickerSpeedStep() {
		switch {
		case key.Matches(msg, m.keys.Back):
			m.modelPicker.step = modelPickerStepModel
			return true, nil
		case key.Matches(msg, m.keys.ChoosePrev),
			key.Matches(msg, m.keys.ChooseNext),
			key.Matches(msg, m.keys.Complete),
			msg.String() == "shift+tab",
			msg.String() == "left",
			msg.String() == "right":
			m.modelPicker.speedIndex = 1 - m.modelPicker.speedIndex
			return true, nil
		case key.Matches(msg, m.keys.Accept):
			if m.turnRunning() {
				return true, nil
			}
			candidate := m.modelPicker.speedCandidate
			draft := m.modelPickerDraft(candidate)
			return true, m.submitModelSelection(candidate, draft.effort, m.modelPicker.speedIndex == 1)
		default:
			return true, nil
		}
	}

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
		return true, nil
	case msg.String() == "left", msg.String() == "right":
		delta := 1
		if msg.String() == "left" {
			delta = -1
		}
		m.adjustModelPickerEffort(candidate, delta)
		return true, nil
	case key.Matches(msg, m.keys.Accept):
		if m.turnRunning() || !m.slashArgCompletionSettledForCurrentTarget() {
			return true, nil
		}
		draft := m.modelPickerDraft(candidate)
		if selection.FastSupported {
			m.modelPicker.step = modelPickerStepSpeed
			m.modelPicker.speedCandidate = candidate
			m.modelPicker.speedIndex = 0
			if draft.fast {
				m.modelPicker.speedIndex = 1
			}
			return true, nil
		}
		return true, m.submitModelSelection(candidate, draft.effort, false)
	}
	return false, nil
}

func (m *Model) modelPickerEffortDeltaAt(candidateIndex int, geometry completionOverlayGeometry, mouseX int) int {
	if m.modelPicker == nil || m.modelPicker.step != modelPickerStepModel {
		return 0
	}
	offset := candidateIndex - m.modelPicker.hitStart
	if offset < 0 || offset >= len(m.modelPicker.effortHits) {
		return 0
	}
	hit := m.modelPicker.effortHits[offset]
	if hit.width <= 0 {
		return 0
	}
	contentX := geometry.left
	if geometry.chrome.useBorder {
		contentX += 2
	}
	localX := mouseX - contentX
	if localX < hit.x || localX >= hit.x+hit.width {
		return 0
	}
	if localX >= hit.midpoint {
		return 1
	}
	return -1
}

func modelPickerSourceLabel(candidate SlashArgCandidate, candidates []SlashArgCandidate) string {
	identity := slashArgCandidateIdentity(candidate)
	if !slashArgIdentityDuplicated(identity, candidates) {
		return ""
	}
	source := modelPickerShortSource(candidate)
	detail := strings.Join(strings.Fields(candidate.Detail), " ")
	var useDetail bool
	for _, other := range candidates {
		if other.Value == candidate.Value || !strings.EqualFold(identity, slashArgCandidateIdentity(other)) ||
			!strings.EqualFold(source, modelPickerShortSource(other)) {
			continue
		}
		if detail == "" || strings.EqualFold(detail, strings.Join(strings.Fields(other.Detail), " ")) {
			// A display name and endpoint description can both collide, for
			// example when two endpoints differ only by the URL path.
			return candidate.Value
		}
		useDetail = true
	}
	if useDetail {
		return detail
	}
	return source
}

func modelPickerShortSource(candidate SlashArgCandidate) string {
	if source := shortModelAliasDisambiguator(candidate.Detail); source != "" {
		return source
	}
	if source := pickerProviderFromConfigID(candidate.ModelConfigID); source != "" {
		return source
	}
	return strings.Join(strings.Fields(strings.TrimSpace(candidate.Detail)), " ")
}

func (m *Model) renderModelPicker(geometry completionOverlayGeometry, candidates []SlashArgCandidate) string {
	contentWidth := m.completionRowInnerWidth() + 2
	showRule := geometry.chrome.headerRows > 1
	if m.isModelPickerSpeedStep() {
		candidate := m.modelPicker.speedCandidate
		draft := m.modelPickerDraft(candidate)
		title := firstNonEmpty(candidate.Display, candidate.Value)
		if effort := strings.TrimSpace(draft.effort); effort != "" && effort != "none" {
			title += " [" + effort + "]"
		}
		lines := m.pickerDrawerHeaderLines(contentWidth, []string{"/model"}, title, geometry.scroll, showRule)
		speeds := [...]struct{ label, detail string }{
			{"Standard", "Normal priority"},
			{"Fast", "Priority speed"},
		}
		for i := geometry.windowStart; i < geometry.windowEnd; i++ {
			speed := speeds[i]
			lines = append(lines, m.renderPickerSimpleRow(speed.label, speed.detail, i == geometry.selected, contentWidth, 10))
		}
		return m.renderCompletionOverlay(geometry, lines)
	}

	lines := m.pickerDrawerHeaderLines(contentWidth, nil, "/model", geometry.scroll, showRule)
	specs := make([]pickerModelRowSpec, len(candidates))
	for i, candidate := range candidates {
		draft := m.modelPickerDraft(candidate)
		var efforts []string
		if candidate.ModelSelection != nil {
			efforts = candidate.ModelSelection.Efforts
		}
		specs[i] = pickerModelRowSpec{
			name:        firstNonEmpty(candidate.Display, candidate.Value),
			source:      modelPickerSourceLabel(candidate, candidates),
			efforts:     efforts,
			effort:      draft.effort,
			effortIndex: slices.Index(efforts, draft.effort),
		}
	}
	layout := newPickerModelTableLayout(contentWidth, specs)
	hits := make([]pickerEffortHit, 0, max(0, geometry.windowEnd-geometry.windowStart))
	for i := geometry.windowStart; i < geometry.windowEnd; i++ {
		line, hit := m.renderPickerModelRow(layout, specs[i], i == geometry.selected)
		lines = append(lines, line)
		hits = append(hits, hit)
	}
	if m.modelPicker != nil {
		m.modelPicker.hitStart = geometry.windowStart
		m.modelPicker.effortHits = hits
	}

	if geometry.chrome.detailRows > 0 {
		detail := ""
		switch {
		case m.slashArgRequestPending:
			detail = "Loading models…"
		case m.modelPicker != nil && m.modelPicker.loadFailed:
			detail = "Could not load models. Enter to retry."
		case len(candidates) == 0:
			detail = "No matching models. Edit the search or Esc to cancel."
		}
		lines = m.appendModelPickerDetail(lines, geometry.chrome, detail, m.theme.HelpHintTextStyle())
	}
	return m.renderCompletionOverlay(geometry, lines)
}

func (m *Model) appendModelPickerDetail(lines []string, chrome completionOverlayChrome, detail string, style lipgloss.Style) []string {
	width := m.completionRowInnerWidth()
	if chrome.detailRows > 1 {
		lines = append(lines, " "+m.theme.DiffPanelBorderStyle().Render(strings.Repeat("─", width))+" ")
	}
	if chrome.detailRows > 0 {
		lines = append(lines, " "+style.Render(truncateTailDisplay(detail, width))+" ")
	}
	return lines
}

func (m *Model) renderModelPickerFooter() string {
	width := m.completionOverlayInnerWidth() - m.completionOverlayFooterIndent()
	if m.isModelPickerSpeedStep() {
		return m.renderPickerKeycapFooter(
			[]pickerKeycapItem{{"↑↓", "select"}, {"enter", "apply"}, {"esc", "back"}},
			[]pickerKeycapItem{{"↑↓", "select"}, {"enter", "apply"}, {"esc", "back"}},
			[]pickerKeycapItem{{"↑↓", ""}, {"enter", ""}, {"esc", ""}},
			width,
		)
	}
	applyLabel := "apply"
	if candidate, ok := m.currentSlashArgCandidate(); ok && candidate.ModelSelection != nil && candidate.ModelSelection.FastSupported {
		applyLabel = "next"
	}
	return m.renderPickerKeycapFooter(
		[]pickerKeycapItem{{"type", "search"}, {"↑↓", "select"}, {"←→", "effort"}, {"enter", applyLabel}, {"esc", "cancel"}},
		[]pickerKeycapItem{{"↑↓", "select"}, {"←→", "effort"}, {"enter", applyLabel}, {"esc", ""}},
		[]pickerKeycapItem{{"↑↓", ""}, {"←→", ""}, {"enter", ""}, {"esc", ""}},
		width,
	)
}
