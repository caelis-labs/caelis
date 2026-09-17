package tuikit

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"
)

// ---------------------------------------------------------------------------
// Overlay primitives — unified frame, z-order, and ESC-layer-close helpers.
//
// Every overlay/modal in the TUI (prompt, palette, completion list, BTW)
// renders through RenderResponsiveOverlayFrame. The frame provides:
//
//   - Consistent rounded-border chrome with token-driven colors
//   - Width/height constraints
//   - Positioning helpers (center, above-bottom, bottom-anchored)
//
// Z-order is managed by the caller (overlay_state.go); these primitives
// only handle rendering of a single overlay layer.
// ---------------------------------------------------------------------------

// ResponsiveOverlayFrameModel renders overlay body lines with optional border chrome.
type ResponsiveOverlayFrameModel struct {
	Body      []string
	Width     int
	UseBorder bool
}

// RenderResponsiveOverlayFrame renders overlay content with border chrome only when requested.
func RenderResponsiveOverlayFrame(theme Theme, m ResponsiveOverlayFrameModel) string {
	width := maxInt(20, m.Width)
	body := strings.Join(m.Body, "\n")
	tok := theme.Tokens()
	box := tok.OverlayBg
	if m.UseBorder {
		box = box.BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(tok.OverlayBorder.GetForeground()).
			BorderBackground(tok.OverlayBg.GetBackground()).
			Padding(0, 1).
			Width(width)
	} else {
		box = box.Width(width)
	}
	frame := box.Render(body)
	return paintBlockBackground(frame, lipgloss.Width(frame), tok.OverlayBg.GetBackground())
}

// PaintLineBackground makes background paint survive nested ANSI resets. It
// fills only transparent cells, preserving explicit backgrounds such as text
// selection and inline code.
func PaintLineBackground(line string, width int, background color.Color) string {
	if width <= 0 || background == nil {
		return line
	}
	return paintBlockBackground(line, width, background)
}

// paintBlockBackground canonicalizes the final physical cells rather than
// relying on an outer ANSI background surviving resets from nested styles.
// Explicit cell backgrounds remain authoritative.
func paintBlockBackground(block string, width int, background color.Color) string {
	if width <= 0 || background == nil {
		return block
	}
	// Lipgloss returns NoColor for an inherited terminal background. Painting
	// that sentinel into physical cells would convert transparency to black.
	if _, ok := background.(lipgloss.NoColor); ok {
		return block
	}
	lines := strings.Split(block, "\n")
	screen := uv.NewScreenBuffer(width, len(lines))
	screen.Method = ansi.GraphemeWidth
	uv.NewStyledString(block).Draw(screen, screen.Bounds())
	for y := range len(lines) {
		for x := 0; x < width; x++ {
			cell := screen.CellAt(x, y)
			if cell != nil && cell.Style.Bg == nil {
				cell.Style.Bg = background
			}
		}
	}
	for y := range lines {
		lines[y] = screen.Line(y).Render()
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// Overlay positioning helpers
// ---------------------------------------------------------------------------

// OverlayCenter places an overlay centered on the screen. The base is the
// full-screen content, overlay is the rendered modal.
func OverlayCenter(base string, overlay string, screenWidth, screenHeight int) string {
	return OverlayAt(base, overlay, screenWidth, screenHeight, maxInt(0, (screenWidth-lipgloss.Width(overlay))/2), maxInt(0, (screenHeight-lipgloss.Height(overlay))/2))
}

// OverlayAt composes a bounded child surface at explicit terminal coordinates.
func OverlayAt(base, overlay string, screenWidth, screenHeight, startX, startY int) string {
	if overlay == "" || screenWidth <= 0 || screenHeight <= 0 {
		return base
	}
	overlayLines := strings.Split(overlay, "\n")
	baseLines := strings.Split(base, "\n")

	// Pad base to screen height.
	for len(baseLines) < screenHeight {
		baseLines = append(baseLines, "")
	}

	overlayWidth := 0
	for _, line := range overlayLines {
		if w := lipgloss.Width(line); w > overlayWidth {
			overlayWidth = w
		}
	}
	if overlayWidth > screenWidth {
		overlayWidth = screenWidth
	}

	compositor := newOverlayLineCompositor(screenWidth)
	startY = maxInt(0, startY)
	for i, overlayLine := range overlayLines {
		row := startY + i
		if row >= screenHeight {
			break
		}
		baseLines[row] = compositor.compose(baseLines[row], overlayLine, overlayWidth, startX)
	}

	return strings.Join(baseLines, "\n")
}

type overlayLineCompositor struct {
	screen uv.ScreenBuffer
	width  int
}

func newOverlayLineCompositor(width int) overlayLineCompositor {
	if width <= 0 {
		return overlayLineCompositor{}
	}
	screen := uv.NewScreenBuffer(width, 1)
	screen.Method = ansi.GraphemeWidth
	return overlayLineCompositor{screen: screen, width: width}
}

func (c overlayLineCompositor) compose(baseLine string, overlayLine string, overlayWidth int, startX int) string {
	if c.width <= 0 {
		return baseLine
	}
	startX = maxInt(0, startX)
	if startX >= c.width {
		return ansi.Truncate(baseLine, c.width, "")
	}
	overlayWidth = min(maxInt(0, overlayWidth), c.width-startX)

	c.screen.Clear()
	uv.NewStyledString(baseLine).Draw(c.screen, c.screen.Bounds())
	if overlayWidth > 0 {
		overlayBounds := uv.Rect(startX, 0, overlayWidth, 1)
		c.screen.ClearArea(overlayBounds)
		uv.NewStyledString(overlayLine).Draw(c.screen, overlayBounds)
	}
	return c.screen.Line(0).Render()
}
