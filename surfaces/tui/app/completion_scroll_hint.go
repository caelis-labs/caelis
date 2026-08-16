package tuiapp

import "strings"

type completionScrollAffordance struct {
	Show    bool
	CanUp   bool
	CanDown bool
}

func (m *Model) completionOverlayFooterIndent() int {
	indent := 1
	if m.overlayUsesBorder() {
		indent += 2
	}
	return indent
}

func (m *Model) renderCompletionOverlayFooter(geometry completionOverlayGeometry) string {
	affordance := geometry.scroll
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
	if geometry.kind == completionSlashArg && m.wizard != nil {
		if step := m.wizard.currentStep(); step != nil && step.MultiSelect {
			return upStyle.Render("↑") + downStyle.Render("↓") +
				descStyle.Render("/hover select ") +
				keyStyle.Render("click") + slash + keyStyle.Render("space") + slash + keyStyle.Render("tab") + descStyle.Render(" toggle") +
				sep + keyStyle.Render("enter") + descStyle.Render(" confirm")
		}
	}
	return upStyle.Render("↑") + downStyle.Render("↓") +
		descStyle.Render("/hover select ") +
		keyStyle.Render("click") + slash + keyStyle.Render("enter") + descStyle.Render(" ") + descStyle.Render("apply") +
		sep + keyStyle.Render("tab") + descStyle.Render(" ") + descStyle.Render("fill")
}

func (m *Model) attachCompletionOverlayFooter(frame string, geometry completionOverlayGeometry) string {
	footer := m.renderCompletionOverlayFooter(geometry)
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
	show := start > 0 || end < total || (atBottom && canLoadMore)
	if !show {
		return completionScrollAffordance{}
	}
	return completionScrollAffordance{
		Show:    true,
		CanUp:   start > 0,
		CanDown: end < total || (atBottom && canLoadMore),
	}
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
