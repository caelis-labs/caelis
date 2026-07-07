package tuiapp

import "strings"

// completionScrollAffordance describes whether a completion overlay can scroll
// and which directions are currently available.
type completionScrollAffordance struct {
	Show    bool
	CanUp   bool
	CanDown bool
}

func (m *Model) completionOverlayActive() bool {
	if m == nil {
		return false
	}
	return len(m.mentionCandidates) > 0 ||
		len(m.skillCandidates) > 0 ||
		len(m.resumeCandidates) > 0 ||
		len(m.slashArgCandidates) > 0 ||
		len(m.slashCandidates) > 0
}

func (m *Model) completionScrollState() (completionScrollAffordance, bool) {
	if !m.completionOverlayActive() {
		return completionScrollAffordance{}, false
	}
	affordance, ok := m.activeCompletionScroll()
	if !ok {
		return completionScrollAffordance{}, true
	}
	return affordance, true
}

func (m *Model) activeCompletionScroll() (completionScrollAffordance, bool) {
	if m == nil {
		return completionScrollAffordance{}, false
	}
	switch {
	case len(m.mentionCandidates) > 0:
		return m.mentionScrollAffordance(), true
	case len(m.skillCandidates) > 0:
		return m.skillScrollAffordance(), true
	case len(m.resumeCandidates) > 0:
		return m.resumeScrollAffordance(), true
	case len(m.slashArgCandidates) > 0:
		return m.slashArgScrollAffordance(), true
	case len(m.slashCandidates) > 0:
		return m.slashCommandScrollAffordance(), true
	default:
		return completionScrollAffordance{}, false
	}
}

func (m *Model) completionOverlayFooterIndent() int {
	indent := 1
	if m.overlayUsesBorder() {
		indent += 2
	}
	return indent
}

func (m *Model) renderCompletionOverlayFooter() string {
	affordance, active := m.completionScrollState()
	if !active {
		return ""
	}
	sepStyle := m.theme.HelpHintTextStyle()
	descStyle := m.theme.HelpHintTextStyle()
	keyStyle := m.theme.KeyLabelStyle().Bold(true)

	upStyle := descStyle
	downStyle := descStyle
	if affordance.CanUp {
		upStyle = keyStyle
	}
	if affordance.CanDown {
		downStyle = keyStyle
	}

	sep := sepStyle.Render("  ")
	slash := sepStyle.Render("/")
	return upStyle.Render("↑") + slash + downStyle.Render("↓") +
		sep + descStyle.Render("select") +
		sep + keyStyle.Render("enter") + descStyle.Render(" ") + descStyle.Render("apply") +
		sep + keyStyle.Render("tab") + descStyle.Render(" ") + descStyle.Render("fill")
}

func (m *Model) attachCompletionOverlayFooter(frame string) string {
	footer := m.renderCompletionOverlayFooter()
	if footer == "" {
		return frame
	}
	indent := strings.Repeat(" ", m.completionOverlayFooterIndent())
	return frame + "\n" + indent + footer
}

func (m *Model) renderPromptChoiceFooter() string {
	if m == nil || m.activePrompt == nil || len(m.activePrompt.choices) == 0 {
		return ""
	}
	affordance := m.promptChoiceScrollAffordance()
	descStyle := m.theme.HelpHintTextStyle()
	keyStyle := m.theme.KeyLabelStyle().Bold(true)
	upStyle := descStyle
	downStyle := descStyle
	if affordance.CanUp {
		upStyle = keyStyle
	}
	if affordance.CanDown {
		downStyle = keyStyle
	}

	sep := descStyle.Render("  ")
	slash := descStyle.Render("/")
	parts := []string{}
	if m.activePrompt.filterable {
		parts = append(parts, descStyle.Render("type filter"))
	}
	parts = append(parts, upStyle.Render("↑")+slash+downStyle.Render("↓")+descStyle.Render(" select"))
	if m.activePrompt.multiSelect {
		parts = append(parts, keyStyle.Render("space")+descStyle.Render(" toggle"))
	}
	parts = append(parts,
		keyStyle.Render("enter")+descStyle.Render(" confirm"),
		keyStyle.Render("esc")+descStyle.Render(" cancel"),
	)
	return strings.Join(parts, sep)
}

func (m *Model) attachPromptChoiceFooter(frame string) string {
	footer := m.renderPromptChoiceFooter()
	if footer == "" {
		return frame
	}
	indent := strings.Repeat(" ", m.completionOverlayFooterIndent())
	return frame + "\n" + indent + footer
}

func completionScrollFromWindow(start, end, total int, atBottom, canLoadMore bool) completionScrollAffordance {
	show := total > completionOverlayVisibleItems || (atBottom && canLoadMore)
	if !show {
		return completionScrollAffordance{}
	}
	return completionScrollAffordance{
		Show:    true,
		CanUp:   start > 0,
		CanDown: end < total || (atBottom && canLoadMore),
	}
}

func (m *Model) mentionScrollAffordance() completionScrollAffordance {
	total := len(m.mentionCandidates)
	maxItems := minInt(completionOverlayVisibleItems, total)
	start, end := completionWindowRange(m.mentionIndex, total, maxItems)
	atBottom := m.mentionIndex >= total-1
	canLoadMore := shouldLoadMoreCompletionCandidates(total, m.mentionLimit)
	return completionScrollFromWindow(start, end, total, atBottom, canLoadMore)
}

func (m *Model) skillScrollAffordance() completionScrollAffordance {
	total := len(m.skillCandidates)
	maxItems := minInt(completionOverlayVisibleItems, total)
	start, end := completionWindowRange(m.skillIndex, total, maxItems)
	atBottom := m.skillIndex >= total-1
	canLoadMore := shouldLoadMoreCompletionCandidates(total, m.skillLimit)
	return completionScrollFromWindow(start, end, total, atBottom, canLoadMore)
}

func (m *Model) resumeScrollAffordance() completionScrollAffordance {
	total := len(m.resumeCandidates)
	maxItems := minInt(completionOverlayVisibleItems, total)
	start, end := completionWindowRange(m.resumeIndex, total, maxItems)
	return completionScrollFromWindow(start, end, total, m.resumeIndex >= total-1, false)
}

func (m *Model) slashCommandScrollAffordance() completionScrollAffordance {
	total := len(m.slashCandidates)
	maxItems := minInt(completionOverlayVisibleItems, total)
	start, end := completionWindowRange(m.slashIndex, total, maxItems)
	return completionScrollFromWindow(start, end, total, m.slashIndex >= total-1, false)
}

func (m *Model) slashArgScrollAffordance() completionScrollAffordance {
	candidates := m.visibleSlashArgCandidates()
	total := len(candidates)
	index := m.currentSlashArgIndex(candidates)
	maxItems := minInt(completionOverlayVisibleItems, total)
	start, end := completionWindowRange(index, total, maxItems)
	return completionScrollFromWindow(start, end, total, index >= total-1, false)
}

func (m *Model) promptChoiceScrollAffordance() completionScrollAffordance {
	const maxVisiblePromptChoices = 8
	m.syncPromptChoiceWindow()
	visible := m.visiblePromptChoices()
	total := len(visible)
	if total <= maxVisiblePromptChoices {
		return completionScrollAffordance{}
	}
	start := max(m.activePrompt.scrollOffset, 0)
	if start > total {
		start = total
	}
	end := minInt(total, start+maxVisiblePromptChoices)
	return completionScrollAffordance{
		Show:    true,
		CanUp:   start > 0,
		CanDown: total > end,
	}
}
