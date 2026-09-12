package tuiapp

import (
	"image"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// Theme layout is shared by the centered frame and its mouse hit targets.
type themePickerLayout struct {
	width, innerWidth int
	border            int
	start, count      int
	hints             []string
}

func (l themePickerLayout) height() int {
	return 2*l.border + 3 + l.count + len(l.hints)
}

func (m *Model) themePickerLayout() themePickerLayout {
	width := minInt(96, maxInt(20, m.width-4))
	inner := maxInt(1, width-m.overlayBorderChromeWidth())
	hints := []string{"↑/↓ / click preview", "Enter apply · Esc restore"}
	if combined := strings.Join(hints, " · "); displayColumns(combined) <= inner {
		hints = []string{combined}
	}
	border := 0
	if m.overlayUsesBorder() {
		border = 1
	}
	p := m.themePicker.prompt
	count := minInt(len(p.choices), maxInt(1, m.height-2*border-5-len(hints)))
	start := minInt(maxInt(0, p.scrollOffset), maxInt(0, len(p.choices)-count))
	if p.choiceIndex < start {
		start = p.choiceIndex
	} else if p.choiceIndex >= start+count {
		start = p.choiceIndex - count + 1
	}
	return themePickerLayout{width: width, innerWidth: inner, border: border, start: start, count: count, hints: hints}
}

func (m *Model) renderThemePicker() string {
	picker := m.themePicker
	p := picker.prompt
	layout := m.themePickerLayout()
	p.scrollOffset = layout.start
	body := []string{m.theme.TitleStyle().Render(p.title), ""}
	for i := layout.start; i < layout.start+layout.count; i++ {
		gutter := "  "
		style := m.theme.TextStyle()
		if i == p.choiceIndex {
			gutter = "▎ "
		}
		if i == picker.hoverIndex || (picker.hoverIndex < 0 && i == p.choiceIndex) {
			style = m.theme.SelectionStyle()
		}
		label := gutter + truncateTailDisplay(p.choices[i].label, maxInt(1, layout.innerWidth-displayColumns(gutter)))
		label = padLineToDisplayWidth(label, layout.innerWidth)
		body = append(body, style.Render(label))
	}
	body = append(body, "")
	for _, hint := range layout.hints {
		body = append(body, m.theme.HelpHintTextStyle().Render(truncateTailDisplay(hint, layout.innerWidth)))
	}
	return tuikit.RenderResponsiveOverlayFrame(m.theme, tuikit.ResponsiveOverlayFrameModel{
		Body: body, Width: layout.width, UseBorder: layout.border != 0,
	})
}

// Save hit targets only after the picker is placed in the physical frame.
func (m *Model) positionThemePicker(frame string) {
	picker := m.themePicker
	if picker == nil || picker.prompt != m.activePrompt {
		return
	}
	layout := m.themePickerLayout()
	x := maxInt(0, (m.width-lipgloss.Width(frame))/2) + 2*layout.border
	y := maxInt(0, (m.height-lipgloss.Height(frame))/2) + layout.border + 2
	picker.geometry = themePickerGeometry{
		choices: image.Rect(x, y, minInt(m.width, x+layout.innerWidth), minInt(m.height, y+layout.count)),
		start:   layout.start,
	}
}
