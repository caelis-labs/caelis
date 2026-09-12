package tuiapp

import (
	"image"

	tea "charm.land/bubbletea/v2"
)

type themePickerGeometry struct {
	choices image.Rectangle
	start   int
}

func (g themePickerGeometry) indexAt(mouse tea.Mouse) int {
	if image.Pt(mouse.X, mouse.Y).In(g.choices) {
		return g.start + mouse.Y - g.choices.Min.Y
	}
	return -1
}

// Hover is presentation-only. Preview requires a complete click on the same
// choice; dragging away or releasing over another row never switches themes.
func (m *Model) handleThemePickerMouse(msg tea.MouseMsg) tea.Cmd {
	picker := m.themePicker
	mouse := msg.Mouse()
	index := picker.geometry.indexAt(mouse)
	switch msg.(type) {
	case tea.MouseMotionMsg:
		picker.hoverIndex = index
	case tea.MouseClickMsg:
		picker.pressedName = ""
		if mouse.Button == tea.MouseLeft && index >= 0 {
			picker.hoverIndex = index
			picker.pressedName = picker.prompt.choices[index].value
		}
	case tea.MouseReleaseMsg:
		pressed := picker.pressedName
		picker.pressedName = ""
		if index < 0 || (mouse.Button != tea.MouseLeft && mouse.Button != tea.MouseNone) {
			return nil
		}
		if pressed == picker.prompt.choices[index].value {
			picker.prompt.choiceIndex = index
			picker.hoverIndex = index
			return m.scheduleThemePreview()
		}
	}
	return nil
}
