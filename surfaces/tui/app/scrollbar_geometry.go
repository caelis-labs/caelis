package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

// scrollbarGeometry is the shared paint and pointer model for a vertical
// terminal-cell scrollbar.
type scrollbarGeometry struct {
	trackLength   int
	thumbStart    int
	thumbLength   int
	maxThumbStart int
	offset        int
	maxOffset     int
}

func newScrollbarGeometry(total, visible, offset int) scrollbarGeometry {
	visible = maxInt(1, visible)
	total = maxInt(0, total)
	maxOffset := maxInt(0, total-visible)
	thumbLength := visible
	if maxOffset > 0 {
		thumbLength = maxInt(1, visible*visible/maxInt(visible, total))
	}
	maxThumbStart := maxInt(0, visible-thumbLength)
	offset = minInt(maxInt(0, offset), maxOffset)
	thumbStart := scaleScrollbarPosition(offset, maxOffset, maxThumbStart)
	return scrollbarGeometry{
		trackLength:   visible,
		thumbStart:    thumbStart,
		thumbLength:   thumbLength,
		maxThumbStart: maxThumbStart,
		offset:        offset,
		maxOffset:     maxOffset,
	}
}

func (g scrollbarGeometry) scrollable() bool {
	// A one-cell track cannot encode movement; hiding it avoids the old
	// behavior where any click jumped directly to the end.
	return g.maxOffset > 0 && g.maxThumbStart > 0
}

func (g scrollbarGeometry) thumbContains(position int) bool {
	return position >= 0 &&
		position < g.trackLength &&
		position >= g.thumbStart &&
		position < g.thumbStart+g.thumbLength
}

func (g scrollbarGeometry) grabOffsetAt(position int) int {
	if g.thumbContains(position) {
		return position - g.thumbStart
	}
	return minInt(maxInt(0, g.thumbLength/2), maxInt(0, g.thumbLength-1))
}

func (g scrollbarGeometry) offsetForPointer(position, grabOffset int) int {
	if !g.scrollable() {
		return g.maxOffset
	}
	grabOffset = minInt(maxInt(0, grabOffset), maxInt(0, g.thumbLength-1))
	thumbStart := minInt(maxInt(0, position-grabOffset), g.maxThumbStart)
	// A content range can collapse onto one terminal row. Preserve the exact
	// offset until the pointer actually moves the thumb to another row.
	if thumbStart == g.thumbStart {
		return g.offset
	}
	return scaleScrollbarPosition(thumbStart, g.maxThumbStart, g.maxOffset)
}

func scaleScrollbarPosition(position, sourceMax, targetMax int) int {
	if sourceMax <= 0 || targetMax <= 0 {
		return 0
	}
	position = minInt(maxInt(0, position), sourceMax)
	return (position*targetMax + sourceMax/2) / sourceMax
}

func addScrollbar(lines []string, contentWidth, visible, offset, total int, theme tuikit.Theme, visibleNow bool) []string {
	geometry := newScrollbarGeometry(total, visible, offset)
	if len(lines) == 0 || !geometry.scrollable() || !visibleNow {
		return lines
	}
	withScrollbar := make([]string, len(lines))
	for index, line := range lines {
		glyph := theme.ScrollbarTrackStyle().Render("▏")
		if geometry.thumbContains(index) {
			glyph = theme.ScrollbarThumbStyle().Render("▎")
		}
		if pad := contentWidth - lipgloss.Width(line); pad > 0 {
			line += strings.Repeat(" ", pad)
		}
		withScrollbar[index] = line + glyph
	}
	return withScrollbar
}
