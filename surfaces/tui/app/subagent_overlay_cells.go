package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/control/agentbinding"
)

const subagentLabelStart = 4

func subagentPurpose(handle agentbinding.Handle) string {
	switch handle {
	case agentbinding.HandleBreeze:
		return "Quick checks and edits"
	case agentbinding.HandleOrbit:
		return "General work and analysis"
	case agentbinding.HandleZenith:
		return "Design and hard problems"
	case agentbinding.HandleGuardian:
		return "Tool approvals"
	case agentbinding.HandleReviewer:
		return "Review workspace changes"
	}
	return ""
}

type subagentOverviewWidths struct {
	name, model, purpose int
}

func subagentOverviewColumns(width int, rows []subagentOverlayRow) subagentOverviewWidths {
	name := subagentLabelStart + 8
	model := max(1, width-name-2)
	if width < 46 {
		return subagentOverviewWidths{name: name, model: model}
	}
	longest := 0
	for _, row := range rows {
		longest = max(longest, displayColumns(row.detail+subagentOverviewEffort(row)))
	}
	model = min(max(23, longest), model-14, 41)
	return subagentOverviewWidths{name: name, model: model, purpose: width - name - model - 4}
}

func subagentOverviewEffort(row subagentOverlayRow) string {
	if effort := subagentEffort(row); effort != "" {
		return " [" + effort + "]"
	}
	return ""
}

func subagentEffort(row subagentOverlayRow) string {
	effort := strings.TrimSpace(row.binding.Effort)
	if row.binding.ProfileID == "" || effort == "none" {
		return ""
	}
	return effort
}

func (m *Model) renderSubagentOverviewRow(row subagentOverlayRow, selected bool, width int, cols subagentOverviewWidths) string {
	style, label := m.theme.TextStyle(), m.theme.TextStyle()
	purpose, effort, accent := m.theme.MutedTextStyle(), m.theme.SecondaryTextStyle(), m.theme.Tokens().Accent
	if selected {
		style = m.theme.SelectionStyle()
		label = style.Bold(true)
		purpose, effort, accent = m.subagentSelectedDetail(purpose), m.subagentSelectedDetail(effort), m.subagentSelectedDetail(accent)
	}
	model, effortValue := subagentFitModel(row.detail, subagentOverviewEffort(row), cols.model)
	modelPad := cols.model - displayColumns(model+effortValue)
	line := subagentRowPrefix(style, accent, selected, " ") +
		label.Render(subagentCellText(row.label, cols.name-subagentLabelStart)) +
		style.Render("  "+model) + effort.Render(effortValue) + style.Render(strings.Repeat(" ", modelPad))
	if cols.purpose > 0 {
		line += style.Render("  ") + purpose.Render(subagentCellText(subagentPurpose(row.handle), cols.purpose))
	}
	return subagentPadRow(line, width, style)
}

func (m *Model) subagentSelectedDetail(detail lipgloss.Style) lipgloss.Style {
	style := m.theme.SelectionStyle()
	if style.GetReverse() || m.theme.NoColor {
		return style
	}
	return style.Foreground(m.theme.ReadableTextColor(detail.GetForeground(), style.GetBackground()))
}

type subagentEffortSpan struct {
	x, width, midpoint int
}

func subagentFitModel(label, effort string, width int) (string, string) {
	budget := width - displayColumns(effort)
	if budget < 7 {
		effort, budget = "", width
	}
	if displayColumns(label) > budget && budget < 22 {
		if _, short, ok := strings.Cut(label, "/"); ok {
			label = short
		}
	}
	return ansi.Truncate(label, budget, "…"), effort
}

func subagentRowPrefix(style, accent lipgloss.Style, focused bool, mark string) string {
	cursor := " "
	if focused {
		cursor = "›"
	}
	return style.Render(" ") + accent.Render(cursor+mark) + style.Render(" ")
}

func subagentPadRow(line string, width int, style lipgloss.Style) string {
	return line + style.Render(strings.Repeat(" ", max(0, width-displayColumns(line))))
}

func subagentCellText(text string, width int) string {
	if width <= 0 {
		return ""
	}
	return padRightDisplay(ansi.Truncate(strings.TrimSpace(text), width, "…"), width)
}

func (m *Model) subagentPaneTitle(title string, width int, focused bool) string {
	prefix, style := "  ", m.theme.SecondaryTextStyle()
	if focused {
		prefix, style = "▎ ", m.theme.TextStyle().Bold(true)
	}
	return " " + style.Render(prefix+truncateTailDisplay(title, max(1, width-2)))
}

func subagentViewport(list *subagentList, budget int) (int, int, []int) {
	offsets := make([]int, len(list.rows))
	for i := range offsets {
		offsets[i] = -1
	}
	// Keep the window fixed until the selected row leaves it.
	start := clampInt(list.windowStart, max(0, list.index-budget+1), list.index)
	list.windowStart = start
	return start, min(start+budget, len(list.rows)), offsets
}
