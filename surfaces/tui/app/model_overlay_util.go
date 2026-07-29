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
	hasWideCell := false
	var hardScrollIdentityColumns []int
	if width > 0 {
		hardScrollIdentityColumns = make([]int, len(lines))
		for i, line := range lines {
			normalized, lineHasWideCell, identityColumn := normalizeFullscreenFrameLineAnalysis(line, width)
			lines[i] = normalized
			hasWideCell = hasWideCell || lineHasWideCell
			hardScrollIdentityColumns[i] = identityColumn
		}
	}
	if height > 0 && len(lines) < height {
		blank := ""
		if width > 0 {
			blank = strings.Repeat(" ", width)
		}
		for len(lines) < height {
			lines = append(lines, blank)
			if width > 0 {
				hardScrollIdentityColumns = append(hardScrollIdentityColumns, width-1)
			}
		}
	}
	if width > 0 && hasWideCell {
		lines = protectWideCellHardScrollLines(lines, width, hardScrollIdentityColumns)
	}
	return strings.Join(lines, "\n"), topTrim
}

func normalizeFullscreenFrameLine(line string, width int) string {
	normalized, _, _ := normalizeFullscreenFrameLineAnalysis(line, width)
	return normalized
}

func normalizeFullscreenFrameLineAnalysis(line string, width int) (string, bool, int) {
	if width <= 0 {
		return line, false, -1
	}
	if displayColumns(line) > width {
		line = ansi.Truncate(line, width, "")
	}
	line = padRightDisplay(line, width)
	analysis := analyzeHardScrollLine(line, width)
	return protectWideCellRepaintLineKnown(line, width, analysis.hasWideCell),
		analysis.hasWideCell,
		analysis.identityColumn
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
	return protectWideCellRepaintLineKnown(line, width, lineContainsWideCell(line))
}

func protectWideCellRepaintLineKnown(line string, width int, hasWideCell bool) string {
	if line == "" || width <= 1 || !hasWideCell {
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

// protectWideCellHardScrollLines binds a known padding cell to its physical row
// whenever the frame contains wide cells. Ultraviolet's hard-scroll detector
// hashes cell content and can otherwise move those rows through DECSTBM without
// repainting them, exposing terminal-specific wide-cell damage. The padding
// cell may be the final cell or the one immediately before a one-cell scrollbar.
func protectWideCellHardScrollLines(lines []string, width int, identityColumns []int) []string {
	if width <= 1 || len(identityColumns) != len(lines) {
		return lines
	}
	for row, line := range lines {
		column := identityColumns[row]
		if column < 0 {
			continue
		}
		lines[row] = bindHardScrollRowIdentity(line, width, column, row)
	}
	return lines
}

func bindHardScrollRowIdentity(line string, width int, column int, row int) string {
	sentinel := hardScrollRowSentinel(row)
	if column == width-1 {
		switch {
		case strings.HasSuffix(line, wideCellRendererSentinel()):
			return strings.TrimSuffix(line, wideCellRendererSentinel()) + sentinel
		case strings.HasSuffix(line, " "):
			return strings.TrimSuffix(line, " ") + sentinel
		case strings.HasSuffix(line, "\u00a0"):
			return strings.TrimSuffix(line, "\u00a0") + sentinel
		default:
			return ansi.Truncate(line, column, "") + sentinel
		}
	}
	if column == width-2 {
		glyphIndex := maxInt(strings.LastIndex(line, "▏"), strings.LastIndex(line, "▎"))
		if glyphIndex >= 0 {
			prefix := line[:glyphIndex]
			paddingIndex := maxInt(strings.LastIndex(prefix, " "), strings.LastIndex(prefix, "\u00a0"))
			if paddingIndex >= 0 {
				paddingWidth := 1
				if strings.HasPrefix(line[paddingIndex:], "\u00a0") {
					paddingWidth = len("\u00a0")
				}
				return line[:paddingIndex] + sentinel + line[paddingIndex+paddingWidth:]
			}
		}
	}
	return ansi.Truncate(line, column, "") +
		sentinel +
		ansi.Cut(line, column+1, width)
}

func hardScrollIdentityColumn(line string, width int) (int, bool) {
	analysis := analyzeHardScrollLine(line, width)
	if analysis.identityColumn < 0 {
		return 0, false
	}
	return analysis.identityColumn, true
}

type hardScrollLineAnalysis struct {
	hasWideCell    bool
	identityColumn int
}

func analyzeHardScrollLine(line string, width int) hardScrollLineAnalysis {
	type displayCell struct {
		content string
		column  int
		width   int
	}
	column := 0
	cellCount := 0
	var previous, last displayCell
	remaining := ansi.Strip(line)
	analysis := hardScrollLineAnalysis{identityColumn: -1}
	for len(remaining) > 0 {
		cluster, clusterWidth := ansi.FirstGraphemeCluster(remaining, ansi.GraphemeWidth)
		remaining = remaining[len(cluster):]
		if clusterWidth < 0 {
			clusterWidth = 0
		}
		if clusterWidth > 0 {
			previous = last
			last = displayCell{
				content: cluster,
				column:  column,
				width:   clusterWidth,
			}
			cellCount++
		}
		if clusterWidth > 1 {
			analysis.hasWideCell = true
		}
		column += clusterWidth
	}
	if column != width || cellCount == 0 {
		return analysis
	}
	if isHardScrollPaddingCell(last.content, last.width) && last.column == width-1 {
		analysis.identityColumn = last.column
		return analysis
	}
	if cellCount < 2 || last.width != 1 || (last.content != "▏" && last.content != "▎") {
		return analysis
	}
	if isHardScrollPaddingCell(previous.content, previous.width) && previous.column == width-2 {
		analysis.identityColumn = previous.column
	}
	return analysis
}

func isHardScrollPaddingCell(content string, width int) bool {
	return width == 1 && (content == " " || content == "\u00a0")
}

func hardScrollRowSentinel(row int) string {
	if row < 0 {
		row = 0
	}
	// NBSP is decoded as a Unicode grapheme instead of the ASCII fast path,
	// so variation selectors remain in the same one-cell Content value that
	// Ultraviolet hashes. Encoding one hexadecimal digit per selector gives
	// every physical row a distinct, visually blank identity without spending
	// another terminal column.
	content := []rune{'\u00a0'}
	for {
		content = append(content, rune(0xfe00+row&0xf))
		row >>= 4
		if row == 0 {
			break
		}
	}
	return "\x1b[8m" + string(content) + "\x1b[28m"
}

func wideCellRendererSentinel() string {
	return "\x1b[8m \x1b[28m"
}

func lineContainsWideCell(line string) bool {
	remaining := ansi.Strip(line)
	for len(remaining) > 0 {
		cluster, width := ansi.FirstGraphemeCluster(remaining, ansi.GraphemeWidth)
		remaining = remaining[len(cluster):]
		if width > 1 {
			return true
		}
	}
	return false
}
