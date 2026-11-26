package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

const (
	sessionPickerInset   = 2
	sessionPickerTop     = 2
	sessionPickerBottom  = 2
	sessionPickerDivider = "   │  "
)

func (m *Model) sessionPickerView(start time.Time) tea.View {
	m.frameTopTrim = 0
	content := tuikit.PaintLineBackground(m.renderSessionPicker(), m.width, m.theme.AppBg)
	m.observeRender(time.Since(start), len(content), "sessions")
	view := tea.NewView(content)
	view.AltScreen, view.ReportFocus = true, true
	view.MouseMode = tea.MouseModeAllMotion
	view.WindowTitle = m.windowTitle()
	return view
}

func (m *Model) renderSessionPicker() string {
	state := m.sessionPicker
	if state == nil || m.height <= 0 || m.width <= 0 {
		return ""
	}
	inset := strings.Repeat(" ", min(sessionPickerInset, max(0, (m.width-1)/2)))
	width := m.width - 2*len(inset)
	if m.height < sessionPickerTop+sessionPickerBottom+1 {
		return inset + m.theme.TitleStyle().Render(pickerTruncate("Sessions", width))
	}
	bodyHeight := m.height - sessionPickerTop - sessionPickerBottom
	listWidth, detailsWidth := width, 0
	if width >= 114 && bodyHeight > 1 {
		detailsWidth = max(32, width*28/100)
		listWidth = width - detailsWidth - displayColumns(sessionPickerDivider)
	}
	state.listWidth = listWidth + 2*len(inset)
	statusWidth, ageWidth := min(8, width/4), min(7, width/5)
	if width < 44 {
		ageWidth = min(ageWidth, 4)
	}
	gapWidth := min(2, listWidth/10)
	if listWidth >= 64 {
		gapWidth = 3
	}
	gap := strings.Repeat(" ", gapWidth)
	titleWidth := max(1, listWidth-statusWidth-ageWidth-2*len(gap))

	state.visible = make(map[int]int, bodyHeight)
	if len(state.positions) > 0 {
		position := state.positions[state.index]
		state.offset = clampInt(state.offset, max(0, position-bodyHeight+1), position)
		state.offset = min(state.offset, max(0, len(state.lines)-bodyHeight))
	}

	tokens := m.theme.Tokens()
	border := m.theme.DiffPanelBorderStyle()
	rule := border.Render(strings.Repeat("─", width))
	divider := border.Render(sessionPickerDivider)
	lines := make([]string, 0, m.height)
	lines = append(lines, inset+m.theme.TitleStyle().Render(pickerTruncate("Sessions", width)), inset+rule)
	details := m.sessionPickerDetails(detailsWidth, bodyHeight-1)
	if id := state.selectionID(); id != state.detailsSessionID {
		state.detailsSessionID, state.detailsOffset = id, 0
	}
	state.detailsOverflow = max(0, len(details)-(bodyHeight-1))
	state.scrollDetails(0)
	now := time.Now()
	for i := range bodyHeight {
		index := state.offset + i
		left := ""
		switch {
		case index < len(state.lines):
			line := state.lines[index]
			if line.row < 0 {
				left = tokens.ListMeta.Render(pickerPathTail(line.heading, titleWidth))
			} else {
				state.visible[sessionPickerTop+i] = line.row
				left = m.renderSessionPickerRow(state, line.row, titleWidth, statusWidth, ageWidth, gap, now)
			}
		case i == 0:
			message := "No sessions"
			if state.loading {
				message = "Loading sessions…"
			} else if state.err != "" {
				message = "Could not load sessions · Enter retry"
			}
			left = m.theme.MutedTextStyle().Render(pickerTruncate(message, listWidth))
		}
		if detailsWidth != 0 {
			left += strings.Repeat(" ", max(0, listWidth-displayColumns(left))) + divider
			if i == 0 {
				left += m.theme.TitleStyle().Render("Details")
			} else if detailRow := i - 1 + state.detailsOffset; detailRow < len(details) {
				left += details[detailRow]
			}
		}
		lines = append(lines, inset+left)
	}
	key := m.theme.TextStyle().Bold(true)
	hint := tokens.ListMeta
	footer := key.Render("↑↓") + hint.Render(" Select   ") + key.Render("Enter/→") + hint.Render(" Open   ") +
		key.Render("Esc") + hint.Render(" Back   ") + tokens.Accent.Render("●") + hint.Render(" Unread")
	if state.err != "" && len(state.rows) != 0 {
		footer = hint.Render("Refresh failed   r Retry   Esc Back")
	}
	if state.detailsOverflow > 0 {
		footer += "   " + key.Render("⇧↑↓") + hint.Render(" Scroll details")
	}
	lines = append(lines, "", inset+pickerTruncate(footer, width))
	return strings.Join(lines, "\n")
}

func (m *Model) renderSessionPickerRow(state *sessionPickerState, index, titleWidth, statusWidth, ageWidth int, gap string, now time.Time) string {
	tokens := m.theme.Tokens()
	style := m.theme.TextStyle()
	muted := tokens.ListMeta
	prefix := " "
	selected := state.index == index
	if selected {
		prefix = "›"
		style = m.theme.ListSelectionStyle()
		muted = style
	}
	if index == len(state.rows) {
		label := "Show more"
		if state.loading {
			label = "Loading sessions…"
		}
		return style.Render(pickerCell(prefix+"   "+label, titleWidth) + strings.Repeat(" ", statusWidth+ageWidth+2*len(gap)))
	}

	row := state.rows[index]
	dot := " "
	if row.Unread {
		dot = "●"
	}
	title := sessionPickerTitle(row)
	if row.SessionID == m.currentSessionID {
		title += " (current)"
	}
	label, statusStyle := "Inactive", muted
	switch {
	case row.Running:
		label, statusStyle = "Active", tokens.Success
	case row.Failed:
		label, statusStyle = "Failed", m.theme.ErrorStyle()
	case row.Unread:
		label, statusStyle = "Ready", style
	}
	accent := tokens.Accent
	if selected {
		if style.GetReverse() {
			accent, statusStyle = style, style
		} else {
			background := style.GetBackground()
			if row.Running || row.Failed {
				statusStyle = style.Foreground(m.theme.ReadableTextColor(statusStyle.GetForeground(), background))
			}
			accent = style.Foreground(m.theme.ReadableTextColor(accent.GetForeground(), background))
		}
	}
	var titleCell string
	if titleWidth >= 5 {
		titleCell = style.Render(prefix+" ") + accent.Render(dot) + style.Render(" "+pickerCell(title, titleWidth-4))
	} else {
		titleCell = style.Render(pickerCell(title, titleWidth))
	}
	return titleCell + style.Render(gap) + statusStyle.Render(pickerCell(label, statusWidth)) + style.Render(gap) + muted.Render(pickerAgeCell(sessionPickerAge(row.UpdatedAt, now), ageWidth))
}

func pickerCell(text string, width int) string {
	text = pickerTruncate(text, width)
	return text + strings.Repeat(" ", max(0, width-displayColumns(text)))
}

func pickerAgeCell(text string, width int) string {
	text = pickerTruncate(text, width)
	return strings.Repeat(" ", max(0, width-displayColumns(text))) + text
}

func (m *Model) sessionPickerDetails(width, height int) []string {
	state := m.sessionPicker
	if width == 0 || state.index >= len(state.rows) {
		return nil
	}
	row := state.rows[state.index]
	muted := m.theme.Tokens().ListMeta
	path := firstNonEmpty(row.Workspace, row.WorkspaceKey, "—")
	pathLines := wrapPickerPath(strings.ReplaceAll(tuikit.SanitizeLogText(path), "\n", `\n`), width)
	details := []string{""}
	titleLines := graphemeWordWrap(sessionPickerTitle(row), width)
	for _, line := range titleLines[:min(len(titleLines), max(1, height-len(pathLines)-6))] {
		details = append(details, m.theme.TitleStyle().Render(line))
	}
	details = append(details, "", muted.Render("Workspace"))
	for _, line := range pathLines {
		details = append(details, m.theme.TextStyle().Render(line))
	}
	date := "—"
	if !row.UpdatedAt.IsZero() {
		date = row.UpdatedAt.Local().Format("Jan 2, 2006 15:04")
	}
	return append(details, "", muted.Render("Updated"), date)
}

func wrapPickerPath(path string, width int) []string {
	var lines []string
	line := ""
	for path != "" {
		end := strings.IndexAny(path, `/\`) + 1
		if end == 0 {
			end = len(path)
		}
		part := path[:end]
		path = path[end:]
		partWidth := displayColumns(part)
		if line != "" && displayColumns(line)+partWidth > width {
			lines = append(lines, line)
			line = ""
		}
		if partWidth > width {
			wrapped := graphemeHardWrap(part, width)
			lines = append(lines, wrapped[:len(wrapped)-1]...)
			part = wrapped[len(wrapped)-1]
		}
		line += part
	}
	return append(lines, line)
}

func pickerPathTail(path string, width int) string {
	if columns := displayColumns(path); columns > width {
		tail := ansi.TruncateLeft(path, columns-width+1, "…")
		if sep := strings.IndexAny(tail, `/\`); sep > len("…") && sep < len(tail)-1 {
			return "…" + tail[sep:]
		}
		return tail
	}
	return path
}

func pickerTruncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	return ansi.Truncate(text, width, "…")
}
