package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func overlayAboveBottomAreaLeft(base string, overlay string, screenWidth int, startX int, bottomHeight int, gap int) string {
	baseLines := strings.Split(base, "\n")
	overlayLines := strings.Split(overlay, "\n")
	if len(baseLines) == 0 || len(overlayLines) == 0 {
		return base
	}
	if startX < 0 {
		startX = 0
	}
	startRow := len(baseLines) - maxInt(0, bottomHeight) - len(overlayLines) - gap
	if startRow < 0 {
		startRow = 0
	}
	for i, line := range overlayLines {
		row := startRow + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		baseLines[row] = overlayLineAt(baseLines[row], line, startX, screenWidth)
	}
	return strings.Join(baseLines, "\n")
}

func overlayTopRight(base string, overlay string, screenWidth int, top int, rightInset int) string {
	baseLines := strings.Split(base, "\n")
	overlayLines := strings.Split(overlay, "\n")
	if len(baseLines) == 0 || len(overlayLines) == 0 || screenWidth <= 0 {
		return base
	}
	if top < 0 {
		top = 0
	}
	if rightInset < 0 {
		rightInset = 0
	}
	overlayWidth := 0
	for _, line := range overlayLines {
		overlayWidth = maxInt(overlayWidth, lipgloss.Width(line))
	}
	if overlayWidth <= 0 {
		return base
	}
	startX := maxInt(0, screenWidth-rightInset-overlayWidth)
	startRow := topRightOverlayRow(baseLines, overlayLines, startX, top)
	for i, line := range overlayLines {
		row := startRow + i
		if row < 0 || row >= len(baseLines) {
			continue
		}
		baseLines[row] = overlayLineAtPreservingPrefix(baseLines[row], line, startX, screenWidth)
	}
	return strings.Join(baseLines, "\n")
}

func topRightOverlayRow(baseLines []string, overlayLines []string, startX int, top int) int {
	if len(baseLines) == 0 {
		return 0
	}
	maxStart := maxInt(0, len(baseLines)-len(overlayLines))
	if top > maxStart {
		top = maxStart
	}
	end := minInt(maxStart, top+8)
	for row := top; row <= end; row++ {
		clear := true
		for i := range overlayLines {
			if rightTrimmedDisplayWidth(baseLines[row+i]) > startX {
				clear = false
				break
			}
		}
		if clear {
			return row
		}
	}
	return top
}

func rightTrimmedDisplayWidth(line string) int {
	return displayColumns(strings.TrimRight(ansi.Strip(line), " \t"))
}

func overlayLineAtPreservingPrefix(baseLine string, overlayLine string, startX int, screenWidth int) string {
	if startX < 0 {
		startX = 0
	}
	overlayWidth := lipgloss.Width(overlayLine)
	prefix := ansi.Truncate(baseLine, startX, "")
	if prefixWidth := lipgloss.Width(prefix); prefixWidth < startX {
		prefix += strings.Repeat(" ", startX-prefixWidth)
	}
	remaining := screenWidth - startX - overlayWidth
	suffix := ""
	if remaining > 0 {
		suffix = strings.Repeat(" ", remaining)
	}
	return prefix + overlayLine + suffix
}

func overlayLineAt(_ string, overlayLine string, startX int, screenWidth int) string {
	if startX < 0 {
		startX = 0
	}
	prefix := strings.Repeat(" ", startX)
	overlayWidth := lipgloss.Width(overlayLine)
	remaining := screenWidth - startX - overlayWidth
	suffix := ""
	if remaining > 0 {
		suffix = strings.Repeat(" ", remaining)
	}
	return prefix + overlayLine + suffix
}

func normalizeFullscreenFrame(view string, width int, height int) string {
	normalized, _ := normalizeFullscreenFrameWithTopTrim(view, width, height)
	return normalized
}

func normalizeFullscreenFrameWithTopTrim(view string, width int, height int) (string, int) {
	if width <= 0 && height <= 0 {
		return view, 0
	}
	lines := strings.Split(view, "\n")
	if len(lines) == 0 {
		lines = []string{""}
	}
	topTrim := 0
	if height > 0 && len(lines) > height {
		// Keep the bottom portion so fixed input/footer rows survive if a
		// transient resize frame overproduces viewport rows.
		topTrim = len(lines) - height
		lines = lines[len(lines)-height:]
	}
	if width > 0 {
		for i, line := range lines {
			lines[i] = normalizeFullscreenFrameLine(line, width)
		}
	}
	if height > 0 && len(lines) < height {
		blank := ""
		if width > 0 {
			blank = strings.Repeat(" ", width)
		}
		for len(lines) < height {
			lines = append(lines, blank)
		}
	}
	return strings.Join(lines, "\n"), topTrim
}

func normalizeFullscreenFrameLine(line string, width int) string {
	if width <= 0 {
		return line
	}
	if displayColumns(line) > width {
		line = ansi.Truncate(line, width, "")
	}
	line = padRightDisplay(line, width)
	return protectWideCellRepaintLine(line, width)
}

func protectWideCellRepaintBlock(text string, width int) string {
	if text == "" || width <= 1 {
		return text
	}
	lines := strings.Split(text, "\n")
	changed := false
	for idx, line := range lines {
		next := protectWideCellRepaintLine(line, width)
		if next != line {
			lines[idx] = next
			changed = true
		}
	}
	if !changed {
		return text
	}
	return strings.Join(lines, "\n")
}

func protectWideCellRepaintLine(line string, width int) string {
	if line == "" || width <= 1 || !lineContainsWideCell(line) {
		return line
	}
	lineWidth := displayColumns(line)
	if lineWidth < width {
		return line + strings.Repeat(" ", width-lineWidth-1) + wideCellRendererSentinel()
	}
	if lineWidth == width && strings.HasSuffix(line, " ") {
		return strings.TrimSuffix(line, " ") + wideCellRendererSentinel()
	}
	return line
}

func wideCellRendererSentinel() string {
	return "\x1b[8m \x1b[28m"
}

func lineContainsWideCell(line string) bool {
	plain := ansi.Strip(line)
	for _, cluster := range splitGraphemeClusters(plain) {
		if graphemeWidth(cluster) > 1 {
			return true
		}
	}
	return false
}
