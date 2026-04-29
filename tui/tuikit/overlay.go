package tuikit

import (
	"strings"

	"charm.land/lipgloss/v2"
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

// RenderOverlayFrame renders a bordered overlay frame with optional title.
func RenderOverlayFrame(theme Theme, m OverlayFrameModel) string {
	tok := theme.Tokens()
	width := maxInt(20, m.Width)

	content := make([]string, 0, len(m.Body)+1)
	if title := strings.TrimSpace(m.Title); title != "" {
		content = append(content, tok.OverlayTitle.Render(title))
	}
	content = append(content, m.Body...)

	boxStyle := lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(tok.OverlayBorder.GetForeground()).
		Padding(0, 1).
		Width(width)

	return boxStyle.Render(strings.Join(content, "\n"))
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
			line = tok.Focus.Bold(true).Render("▸ " + label)
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
		w := lipgloss.Width(line)
		if w > overlayWidth {
			overlayWidth = w
		}
	}

	startY := maxInt(0, (screenHeight-len(overlayLines))/2)
	startX := maxInt(0, (screenWidth-overlayWidth)/2)

	for i, overlayLine := range overlayLines {
		row := startY + i
		if row >= len(baseLines) {
			break
		}
		baseLines[row] = placeOverlayOnLine(baseLines[row], overlayLine, startX, screenWidth)
	}

	return strings.Join(baseLines, "\n")
}

// placeOverlayOnLine replaces a portion of a base line with overlay content
// at the given x offset.
func placeOverlayOnLine(baseLine, overlayLine string, startX, screenWidth int) string {
	if startX < 0 {
		startX = 0
	}
	_ = baseLine
	prefix := strings.Repeat(" ", startX)
	overlayWidth := lipgloss.Width(overlayLine)
	remaining := screenWidth - startX - overlayWidth
	suffix := ""
	if remaining > 0 {
		suffix = strings.Repeat(" ", remaining)
	}
	return prefix + overlayLine + suffix
}
