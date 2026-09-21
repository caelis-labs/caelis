package tuiapp

import (
	"fmt"
	"strings"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func (m *Model) acpManualBodyLines(width, offset, budget int) []string {
	s := m.wizardOverlay
	text := "Download ZIP:\n" + s.runtimeSetup.ArchiveURL + "\n\n" + strings.Join(s.runtimeSetup.ManualSteps, "\n\n")
	s.text.lines = strings.Split(tuikit.SanitizeLogText(text), "\n")
	var lines []string
	var mapping []wizardTextRow
	for index, line := range s.text.lines {
		column := 0
		for _, part := range strings.Split(hardWrapDisplayLine(line, width), "\n") {
			mapping = append(mapping, wizardTextRow{line: index, from: column, width: displayColumns(part)})
			column += displayColumns(part)
			styled := part
			if index == 1 {
				styled = m.theme.LinkStyle().Hyperlink(tuikit.EscapeHyperlinkURI(s.runtimeSetup.ArchiveURL)).Render(part)
			}
			lines = append(lines, styled)
		}
	}
	count := budget
	if len(lines) > budget {
		count = max(1, budget-1)
	}
	s.window = clampInt(s.window, 0, max(0, len(lines)-count))
	end := min(s.window+count, len(lines))
	visible := append([]string(nil), lines[s.window:end]...)
	for i := s.window; i < end; i++ {
		row := mapping[i]
		row.y = offset + i - s.window
		s.text.rows = append(s.text.rows, row)
	}
	if len(lines) > budget && budget > 1 {
		visible = append(visible, m.theme.HelpHintTextStyle().Render(fmt.Sprintf("%d–%d / %d lines", s.window+1, end, len(lines))))
	}
	return visible
}
