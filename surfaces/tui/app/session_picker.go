package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type sessionPickerState struct {
	rows     []ResumeCandidate
	allRows  []ResumeCandidate
	query    string
	index    int
	offset   int
	loading  bool
	err      string
	request  uint64
	cancel   context.CancelFunc
	geometry subagentOverlayGeometry
	pressed  string
}

type sessionPickerResultMsg struct {
	request uint64
	rows    []ResumeCandidate
	err     error
}

type sessionPickerRefreshMsg struct{ request uint64 }

func (m *Model) openSessionPicker() tea.Cmd {
	if m.sessionPicker != nil {
		return nil
	}
	m.clearInputOverlays()
	m.sessionPickerSeq++
	m.sessionPicker = &sessionPickerState{request: m.sessionPickerSeq}
	return m.loadSessionPicker()
}

func (m *Model) closeSessionPicker() {
	if m.sessionPicker != nil && m.sessionPicker.cancel != nil {
		m.sessionPicker.cancel()
	}
	m.sessionPicker = nil
}

func (m *Model) loadSessionPicker() tea.Cmd {
	state := m.sessionPicker
	if state == nil || state.loading {
		return nil
	}
	if m.cfg.ListSessions == nil {
		return nil
	}
	state.loading = true
	ctx, cancel := context.WithCancel(contextOrBackground(m.cfg.Context))
	state.cancel = cancel
	request, list := state.request, m.cfg.ListSessions
	return func() tea.Msg {
		rows, err := list(ctx)
		return sessionPickerResultMsg{request: request, rows: rows, err: err}
	}
}

func (m *Model) applySessionPickerResult(msg sessionPickerResultMsg) tea.Cmd {
	state := m.sessionPicker
	if state == nil || state.request != msg.request {
		return nil
	}
	state.loading = false
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	if msg.err != nil {
		state.err = msg.err.Error()
	} else {
		selected := m.currentSessionID
		if state.index < len(state.rows) {
			selected = state.rows[state.index].SessionID
		}
		state.allRows, state.err = msg.rows, ""
		state.filterRows()
		state.geometry = subagentOverlayGeometry{}
		state.index = clampInt(state.index, 0, maxInt(0, len(state.rows)-1))
		for i, row := range state.rows {
			if row.SessionID == selected {
				state.index = i
				break
			}
		}
	}
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return sessionPickerRefreshMsg{request: msg.request} })
}

func (m *Model) selectSessionPicker() tea.Cmd {
	state := m.sessionPicker
	if state == nil || len(state.rows) == 0 {
		return nil
	}
	id := state.rows[state.index].SessionID
	m.closeSessionPicker()
	return m.executeLineCmd(Submission{Text: "/resume " + id})
}

func (m *Model) handleSessionPickerKey(msg tea.KeyMsg) tea.Cmd {
	if _, release := msg.(tea.KeyReleaseMsg); release {
		return nil
	}
	state := m.sessionPicker
	state.pressed = ""
	switch msg.String() {
	case "esc", "ctrl+o":
		m.closeSessionPicker()
	case "up":
		state.index = maxInt(0, state.index-1)
	case "down":
		state.index = minInt(maxInt(0, len(state.rows)-1), state.index+1)
	case "pgup":
		state.index = maxInt(0, state.index-maxInt(1, m.height-9))
	case "pgdown":
		state.index = minInt(maxInt(0, len(state.rows)-1), state.index+maxInt(1, m.height-9))
	case "home":
		state.index = 0
	case "end":
		state.index = maxInt(0, len(state.rows)-1)
	case "enter":
		if state.err != "" {
			return m.loadSessionPicker()
		}
		return m.selectSessionPicker()
	case "backspace":
		state.setQuery(trimLastRune(state.query))
	case "ctrl+u", "ctrl+w":
		state.setQuery("")
	default:
		if k := msg.Key(); k.Text != "" && !k.Mod.Contains(tea.ModCtrl) && !k.Mod.Contains(tea.ModAlt) {
			state.setQuery(state.query + k.Text)
		}
	}
	return nil
}

func (m *Model) renderSessionPicker() string {
	state := m.sessionPicker
	if state == nil {
		return ""
	}
	width := min(112, maxInt(20, m.width-4))
	inner := maxInt(1, width-m.overlayBorderChromeWidth())
	title := m.theme.TitleStyle().Render("/resume · Sessions")
	body := []string{title + strings.Repeat(" ", maxInt(1, inner-displayColumns(title)-1)) + "×", m.theme.HelpHintTextStyle().Render(state.searchLine(inner))}
	count := minInt(len(state.rows), maxInt(1, m.height-9))
	// Keep visible rows stationary while hovering; scroll only when selection
	// leaves the window or a resize/refresh changes its bounds.
	start := maxInt(state.index-count+1, minInt(state.offset, state.index))
	start = clampInt(start, 0, maxInt(0, len(state.rows)-count))
	state.offset = start
	now := time.Now()
	rowOffsets := make([]int, len(state.rows))
	for i := range rowOffsets {
		rowOffsets[i] = -1
	}
	for i := start; i < start+count; i++ {
		row := state.rows[i]
		label := strings.Join(strings.Fields(firstNonEmpty(row.Title, row.Prompt, row.SessionID)), " ")
		status := ""
		if row.Running {
			status = "running"
		}
		if row.SessionID == m.currentSessionID {
			if status != "" {
				status += " · "
			}
			status += "current"
		}
		prefix := "  "
		if i == state.index {
			prefix = "> "
		}
		age := sessionPickerAge(row.UpdatedAt, now)
		// Reserve the age and some title even when status must be shortened.
		statusBudget := maxInt(0, inner-displayColumns(prefix)-displayColumns(age)-minInt(12, inner/2)-4)
		if statusBudget == 0 {
			status = ""
		} else {
			status = truncateTailDisplay(status, statusBudget)
		}
		suffix := age
		if status != "" {
			suffix = status + "  " + age
		}
		budget := maxInt(1, inner-displayColumns(prefix)-displayColumns(suffix)-2)
		label = truncateTailDisplay(label, budget)
		line := prefix + label
		line += strings.Repeat(" ", maxInt(1, inner-displayColumns(line)-displayColumns(suffix))) + suffix
		if i == state.index {
			line = m.theme.CommandActiveStyle().Padding(0).Render(line)
		}
		rowOffsets[i] = len(body)
		body = append(body, line)
	}
	if len(state.rows) == 0 {
		label := "No sessions"
		if state.loading {
			label = "Loading sessions…"
		}
		body = append(body, m.theme.MutedTextStyle().Render(label))
	}
	if state.err != "" {
		body = append(body, m.theme.ErrorStyle().Render(truncateTailDisplay(state.err, inner)))
	}
	body = append(body, "")
	if len(state.rows) > 0 {
		body = append(body, m.theme.MutedTextStyle().Render(truncateTailDisplay("ID "+state.rows[state.index].SessionID, inner)))
	}
	body = append(body, m.theme.HelpHintTextStyle().Render(truncateTailDisplay("↑↓ select  enter open  esc close", inner)))
	frame := tuikit.RenderResponsiveOverlayFrame(m.theme, tuikit.ResponsiveOverlayFrameModel{Body: body, Width: width, UseBorder: m.overlayUsesBorder()})
	w, h := lipgloss.Width(frame), lipgloss.Height(frame)
	x, y := maxInt(0, (m.width-w)/2), maxInt(0, (m.height-h)/2)
	inset := 0
	if m.overlayUsesBorder() {
		inset = 1
	}
	for i, offset := range rowOffsets {
		if offset >= 0 {
			rowOffsets[i] = y + inset + offset
		}
	}
	state.geometry = subagentOverlayGeometry{x: x, y: y, width: w, height: h, rows: rowOffsets, closeX: x + w - 1 - m.overlayBorderChromeWidth()/2, closeY: y + inset}
	return frame
}

func sessionPickerAge(updatedAt, now time.Time) string {
	if updatedAt.IsZero() {
		return "—"
	}
	age := now.Sub(updatedAt)
	switch {
	case age < time.Minute:
		return "now"
	case age < time.Hour:
		return fmt.Sprintf("%dm", age/time.Minute)
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh", age/time.Hour)
	default:
		return fmt.Sprintf("%dd", age/(24*time.Hour))
	}
}

func (m *Model) handleSessionPickerMouse(msg tea.MouseMsg) tea.Cmd {
	state, mouse := m.sessionPicker, msg.Mouse()
	g := state.geometry
	inside := mouse.X >= g.x && mouse.X < g.x+g.width && mouse.Y >= g.y && mouse.Y < g.y+g.height
	row := -1
	inset := m.overlayBorderChromeWidth() / 2
	if inside && mouse.X >= g.x+inset && mouse.X < g.x+g.width-inset {
		row = subagentRowAtY(g.rows, mouse.Y)
	}
	closeTarget := inside && mouse.Y == g.closeY && mouse.X == g.closeX
	switch msg.(type) {
	case tea.MouseMotionMsg:
		if row >= 0 {
			state.index = row
		}
	case tea.MouseWheelMsg:
		state.pressed = ""
		if !inside {
			return nil
		}
		if mouse.Button == tea.MouseWheelUp {
			state.index = maxInt(0, state.index-1)
		}
		if mouse.Button == tea.MouseWheelDown {
			state.index = minInt(maxInt(0, len(state.rows)-1), state.index+1)
		}
	case tea.MouseClickMsg:
		state.pressed = ""
		if !inside || mouse.Button != tea.MouseLeft {
			return nil
		}
		if closeTarget {
			state.pressed = "close"
		} else if row >= 0 {
			state.index = row
			state.pressed = "session:" + state.rows[row].SessionID
		}
	case tea.MouseReleaseMsg:
		pressed := state.pressed
		state.pressed = ""
		if !inside || (mouse.Button != tea.MouseLeft && mouse.Button != tea.MouseNone) {
			return nil
		}
		if pressed == "close" && closeTarget {
			m.closeSessionPicker()
		} else if row >= 0 && pressed == "session:"+state.rows[row].SessionID {
			state.index = row
			return m.selectSessionPicker()
		}
	}
	return nil
}

func (s *sessionPickerState) setQuery(query string) {
	s.query = truncateRunes(query, 160)
	s.index, s.offset = 0, 0
	s.filterRows()
}

func (s *sessionPickerState) filterRows() {
	s.rows = nil
	query := strings.ToLower(strings.TrimSpace(s.query))
	for _, row := range s.allRows {
		if query == "" || strings.Contains(strings.ToLower(row.Title+" "+row.Prompt+" "+row.SessionID), query) {
			s.rows = append(s.rows, row)
		}
	}
	s.index = clampInt(s.index, 0, max(0, len(s.rows)-1))
}

func (s *sessionPickerState) searchLine(width int) string {
	value := "/ " + s.query + "▏"
	if s.query == "" {
		value += "Search"
	}
	return truncateTailDisplay(value, width)
}
