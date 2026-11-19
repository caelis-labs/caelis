package tuiapp

import (
	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

const overlayCloseLabel = "[x]"

type overlayCloseButton struct{ hovered, pressed bool }

func (m *Model) overlayHeader(width int, ancestors []string, title string, close overlayCloseButton, enabled bool) string {
	buttonWidth := len(overlayCloseLabel)
	crumb := tuikit.RenderBreadcrumb(m.theme, width-buttonWidth-1, ancestors, title)
	return padRightDisplay(crumb, width-buttonWidth) + m.overlayButtonStyle(enabled, close.hovered || close.pressed).Render(overlayCloseLabel)
}

func overlayCloseX(contentX, width int) int { return contentX + width - len(overlayCloseLabel) }

func (b *overlayCloseButton) mouse(msg tea.MouseMsg, x, y int) (handled, activate bool) {
	mouse := msg.Mouse()
	hit := mouse.Y == y && mouse.X >= x && mouse.X < x+len(overlayCloseLabel)
	b.hovered = hit
	switch msg.(type) {
	case tea.MouseMotionMsg:
		if !hit {
			b.pressed = false
		}
	case tea.MouseClickMsg:
		b.pressed = hit && mouse.Button == tea.MouseLeft
	case tea.MouseReleaseMsg:
		activate = b.pressed && hit && (mouse.Button == tea.MouseLeft || mouse.Button == tea.MouseNone)
		b.pressed = false
	default:
		b.pressed = false
		return false, false
	}
	return hit, activate
}
