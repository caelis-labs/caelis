package tuiapp

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func (m *Model) paneDivider(rect paneRect) string {
	style := m.theme.SeparatorStyle()

	if rect.width == 1 {
		return style.Render("│")
	}
	return style.Render(strings.Repeat("─", rect.width))
}

func (m *Model) renderPaneHint(view *subagentOutputView, state *subagentOutputOverlayState, width int) string {
	notice := state.inputStatus
	label := ""
	if view != nil {
		switch m.subagentOutputCurrentStatus(view) {
		case subagentOutputRunning:
			remaining := width - 2
			if notice != "" {
				remaining -= displayColumns(notice) + 3
			}
			label = m.renderActivityHint(view.activity.visible(true), time.Now(), maxInt(1, remaining), 0)
		case subagentOutputFailed:
			label = m.theme.WarnStyle().Render("! Failed")
		}
	}
	if notice != "" {
		if label != "" {
			label += m.theme.HelpHintTextStyle().Render(" · ")
		}
		label += m.theme.HelpHintTextStyle().Render(notice)
	}
	return truncateTailDisplay(label, width)
}

func (m *Model) subagentPaneSurface() lipgloss.Style {
	if m.workspaceLayout().split {
		return lipgloss.NewStyle().Background(m.theme.AppBg)
	}
	return m.theme.Tokens().OverlayBg
}

// Hide and the context gauge stay right-aligned, independent of composer focus
// and optional hints. Editor padding and text never contain workspace controls.
func (m *Model) renderPaneFooter(state *subagentOutputOverlayState, width int) string {
	state.footerHideX, state.footerHideWidth = 0, 0
	if width <= 0 {
		return ""
	}
	model, usage := m.paneStatusParts(state)
	hide := truncateTailDisplay(firstNonEmptyString(m.paneKeyHint(m.keys.PaneToggle), "Hide"), width)
	hideWidth := displayColumns(hide)
	if usageWidth := width - hideWidth - 2; usageWidth > 0 {
		usage = truncateTailDisplay(usage, usageWidth)
	} else {
		usage = ""
	}
	rightWidth := hideWidth
	if usage != "" {
		rightWidth += 2 + displayColumns(usage)
	}
	state.footerHideX, state.footerHideWidth = width-rightWidth, hideWidth
	leftWidth := maxInt(0, width-rightWidth-2)
	hints := m.paneFooterHints(state)
	left := ""
	if leftWidth > 0 && (m.workspace.resizing || m.workspace.dragging || state.menu != "") && displayColumns(model)+2+displayColumns(hints) > leftWidth {
		left = m.theme.HelpHintTextStyle().Render(truncateTailDisplay(hints, leftWidth))
	} else if leftWidth > 0 {
		model = truncateTailDisplay(model, leftWidth)
		for displayColumns(model)+2+displayColumns(hints) > leftWidth && hints != "" {
			if i := strings.LastIndex(hints, "  "); i >= 0 {
				hints = hints[:i]
			} else {
				hints = ""
			}
		}
		left = lipgloss.NewStyle().Foreground(m.theme.TextSecondary).Render(model)
		if hints != "" {
			left += m.theme.HelpHintTextStyle().Render("  " + hints)
		}
	}
	hideStyle := m.theme.HelpHintTextStyle()
	if state.footerHideHovered {
		hideStyle = m.theme.SelectionStyle()
	}
	right := hideStyle.Render(hide)
	if usage != "" {
		right += "  " + m.theme.TranscriptMetaStyle().Render(usage)
	}
	return left + strings.Repeat(" ", width-rightWidth-displayColumns(left)) + right
}

func (m *Model) paneFooterHints(state *subagentOutputOverlayState) string {
	if m.workspace.dragging {
		return "Release to apply  Esc Cancel"
	}
	if m.workspace.resizing {
		if m.workspaceLayout().divider.width == 1 {
			return "←→ Resize  ↵ Apply  Esc Cancel"
		}
		return "↑↓ Resize  ↵ Apply  Esc Cancel"
	}
	if state.menu != "" {
		return "↑↓ Select  ↵ Open  Esc Back"
	}
	if !m.workspace.childFocused {
		if m.theme.NoColor {
			return "Main focused  " + m.paneKeyHint(m.keys.PaneFocus)
		}
		return m.paneKeyHint(m.keys.PaneFocus)
	}
	hints := ""
	if state.editor.Value() != "" {
		hints = "↵ Send  " + m.paneKeyHint(m.keys.InsertNewline) + "  "
	}
	if m.workspaceLayout().split {
		hints += m.paneKeyHint(m.keys.PaneFocus) + "  "
	}
	if m.theme.NoColor {
		hints = "▸ Focused  " + hints
	}
	return strings.TrimSpace(hints)
}

func (m *Model) paneKeyHint(binding key.Binding) string {
	if !binding.Enabled() {
		return ""
	}
	hint := binding.Help()
	return hint.Key + " " + hint.Desc
}

func (m *Model) renderPaneResizePreview(base string) string {
	if (!m.workspace.dragging && !m.workspace.resizing) || !m.workspaceLayout().split {
		return base
	}
	rect := m.workspaceLayoutForPreferences(m.paneResizePreferences()).divider
	style := m.theme.PromptStyle()
	line := style.Render(strings.Repeat("─", rect.width))
	if rect.width == 1 {
		line = strings.TrimSuffix(strings.Repeat(style.Render("│")+"\n", rect.height), "\n")
	}
	return tuikit.OverlayAt(base, line, m.width, m.height, rect.x, rect.y)
}
