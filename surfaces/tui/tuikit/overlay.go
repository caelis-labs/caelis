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
// renders inside an OverlayFrame. The frame provides:
//
//   - Consistent rounded-border chrome with token-driven colors
//   - Optional title row
//   - Width/height constraints
//   - Positioning helpers (center, above-bottom, bottom-anchored)
//
// Z-order is managed by the caller (overlay_state.go); these primitives
// only handle rendering of a single overlay layer.
// ---------------------------------------------------------------------------

// OverlayFrameModel defines the content and structure of an overlay.
type OverlayFrameModel struct {
	Title string   // optional title text at top
	Body  []string // body content lines
	Width int      // desired frame width
}

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

// RenderOverlayFrame renders a bordered overlay frame with optional title.
func RenderOverlayFrame(theme Theme, m OverlayFrameModel) string {
	tok := theme.Tokens()
	width := maxInt(20, m.Width)

	content := make([]string, 0, len(m.Body)+1)
	if title := strings.TrimSpace(m.Title); title != "" {
		content = append(content, tok.OverlayTitle.Render(title))
	}
	content = append(content, m.Body...)

	return RenderResponsiveOverlayFrame(theme, ResponsiveOverlayFrameModel{
		Body:      content,
		Width:     width,
		UseBorder: true,
	})
}

// OverlayCompletionModel defines a completion/suggestion list overlay.
type OverlayCompletionModel struct {
	Title   string
	Items   []OverlayCompletionItem
	Index   int // currently selected index
	Width   int
	MaxShow int // max visible items (0 = show all)
}

// OverlayCompletionItem is a single item in a completion list.
type OverlayCompletionItem struct {
	Label string
	Desc  string
}

// RenderOverlayCompletion renders a completion/suggestion list inside an
// overlay frame. The selected item is highlighted.
func RenderOverlayCompletion(theme Theme, m OverlayCompletionModel) string {
	tok := theme.Tokens()
	if len(m.Items) == 0 {
		return ""
	}

	maxShow := m.MaxShow
	if maxShow <= 0 {
		maxShow = len(m.Items)
	}

	// Determine visible window centered on the selection.
	start := 0
	if m.Index >= maxShow {
		start = m.Index - maxShow + 1
	}
	end := start + maxShow
	if end > len(m.Items) {
		end = len(m.Items)
		start = maxInt(0, end-maxShow)
	}

	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		item := m.Items[i]
		label := strings.TrimSpace(item.Label)
		if label == "" {
			continue
		}
		var line string
		if i == m.Index {
			line = theme.SelectionStyle().Bold(true).Render("▸ " + label)
		} else {
			line = tok.TextPrimary.Render("  " + label)
		}
		if desc := strings.TrimSpace(item.Desc); desc != "" {
			line += "  " + tok.TextMuted.Render(desc)
		}
		lines = append(lines, line)
	}

	// Scroll indicators.
	if start > 0 {
		lines = append([]string{tok.TextMuted.Render("  ↑ more")}, lines...)
	}
	if end < len(m.Items) {
		lines = append(lines, tok.TextMuted.Render("  ↓ more"))
	}

	return RenderOverlayFrame(theme, OverlayFrameModel{
		Title: m.Title,
		Body:  lines,
		Width: m.Width,
	})
}

// ---------------------------------------------------------------------------
// Overlay positioning helpers
// ---------------------------------------------------------------------------

// OverlayCenter places an overlay centered on the screen. The base is the
// full-screen content, overlay is the rendered modal.
func OverlayCenter(base string, overlay string, screenWidth, screenHeight int) string {
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

	startY := maxInt(0, (screenHeight-len(overlayLines))/2)
	startX := maxInt(0, (screenWidth-overlayWidth)/2)
	compositor := newOverlayLineCompositor(screenWidth)
	for i, overlayLine := range overlayLines {
		row := startY + i
		if row >= len(baseLines) {
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
