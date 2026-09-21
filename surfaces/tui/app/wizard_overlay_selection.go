package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Selection coordinates address logical text, so terminal wrapping and scrolling
// do not insert newlines into copied commands or download URLs.
type wizardTextSelection struct {
	lines      []string
	rows       []wizardTextRow
	selecting  bool
	start, end textSelectionPoint
}

type wizardTextRow struct {
	y, line, from, width int
}

func (m *Model) wizardTextPoint(mouse tea.Mouse, clamp bool) (textSelectionPoint, bool) {
	s, g := &m.wizardOverlay.text, m.wizardOverlay.geometry
	if len(s.rows) == 0 {
		return textSelectionPoint{}, false
	}
	y, x := mouse.Y-g.contentY, mouse.X-g.contentX
	if !clamp && (x < 0 || x >= g.contentWidth) {
		return textSelectionPoint{}, false
	}
	row := s.rows[0]
	for _, candidate := range s.rows {
		if candidate.y > y {
			break
		}
		row = candidate
	}
	if !clamp && row.y != y {
		return textSelectionPoint{}, false
	}
	col := row.from + clampInt(x, 0, row.width)
	col = alignDisplayColumnToCharBoundary(s.lines[row.line], col)
	return textSelectionPoint{line: row.line, col: col}, true
}

func (m *Model) renderWizardTextSelection(body []string) {
	s := &m.wizardOverlay.text
	if !s.selecting {
		return
	}
	start, end, ok := normalizedSelectionRange(s.start, s.end, len(s.lines))
	if !ok {
		return
	}
	for _, row := range s.rows {
		from, to, selected := selectionContentRange(s.lines[row.line], row.line, start, end, 0)
		from, to = max(from, row.from)-row.from, min(to, row.from+row.width)-row.from
		if !selected || from >= to {
			continue
		}
		line := body[row.y]
		body[row.y] = ansi.Cut(line, 0, from) + m.theme.InputSelectionStyle().Render(ansi.Strip(ansi.Cut(line, from, to))) + ansi.Cut(line, to, ansi.StringWidth(line))
	}
}

// Only read-only rows enter this plane. Form values and catalog actions retain
// their own input behavior; secrets never enter clipboard selection data.
func (m *Model) collectWizardReadonlyText(body []string, controls []int) {
	s := &m.wizardOverlay.text
	s.lines = make([]string, len(body))
	for y := 1; y < len(body)-2; y++ {
		interactive := false
		for _, control := range controls {
			if control == y {
				interactive = true
				break
			}
		}
		plain := strings.TrimRight(ansi.Strip(body[y]), " ")
		if interactive || strings.HasPrefix(plain, "─") {
			continue
		}
		s.lines[y] = plain
		s.rows = append(s.rows, wizardTextRow{y: y, line: y, width: displayColumns(plain)})
	}
}

func (m *Model) finishWizardTextSelection(mouse tea.Mouse) tea.Cmd {
	s := &m.wizardOverlay.text
	if point, ok := m.wizardTextPoint(mouse, true); ok {
		s.end = point
	}
	s.selecting = false
	m.cancelSelectionAutoScroll()
	start, end, ok := normalizedSelectionRange(s.start, s.end, len(s.lines))
	if !ok || (mouse.Button != tea.MouseLeft && mouse.Button != tea.MouseNone) {
		return nil
	}
	if text := selectionTextFromLines(s.lines, start, end); text != "" {
		return m.copySelectionToClipboard(text)
	}
	return nil
}

func (m *Model) wizardSelectionScrollDelta(mouse tea.Mouse) int {
	s, g := &m.wizardOverlay.text, m.wizardOverlay.geometry
	if !s.selecting || !m.isACPManualSetup() || len(s.rows) == 0 {
		return 0
	}
	if mouse.Y <= g.contentY+s.rows[0].y {
		return -1
	}
	if mouse.Y >= g.contentY+s.rows[len(s.rows)-1].y {
		return 1
	}
	return 0
}

func (m *Model) scrollWizardSelectionBy(delta int, mouse tea.Mouse) (bool, tea.Cmd) {
	s := m.wizardOverlay
	previous := s.window
	s.window = max(0, s.window+delta)
	m.renderWizardOverlay()
	if point, ok := m.wizardTextPoint(mouse, true); ok {
		s.text.end = point
	}
	return previous != s.window, nil
}
