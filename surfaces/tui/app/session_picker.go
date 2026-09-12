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
	index    int
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
	if state == nil || state.loading || m.cfg.ListSessions == nil {
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
		state.rows, state.err = msg.rows, ""
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
	state := m.sessionPicker
	switch msg.String() {
	case "esc", "ctrl+o":
		m.closeSessionPicker()
	case "up", "k":
		state.index = maxInt(0, state.index-1)
	case "down", "j":
		state.index = minInt(maxInt(0, len(state.rows)-1), state.index+1)
	case "pgup":
		state.index = maxInt(0, state.index-maxInt(1, m.height-8))
	case "pgdown":
		state.index = minInt(maxInt(0, len(state.rows)-1), state.index+maxInt(1, m.height-8))
	case "home":
		state.index = 0
	case "end":
		state.index = maxInt(0, len(state.rows)-1)
	case "enter":
		return m.selectSessionPicker()
	}
	return nil
}

func (m *Model) renderSessionPicker() string {
	state := m.sessionPicker
	if state == nil {
		return ""
	}
	width := minInt(100, maxInt(12, m.width-4))
	inner := maxInt(1, width-m.overlayBorderChromeWidth())
	title := m.theme.TitleStyle().Render("Sessions")
	body := []string{title + strings.Repeat(" ", maxInt(1, inner-displayColumns(title)-1)) + "×", ""}
	count := minInt(len(state.rows), maxInt(1, m.height-9))
	start := maxInt(0, state.index-count+1)
	start = minInt(start, maxInt(0, len(state.rows)-count))
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
			status = strings.TrimSpace(status + " · current")
		}
		prefix := "  "
		if i == state.index {
			prefix = "> "
		}
		budget := maxInt(1, inner-displayColumns(prefix)-displayColumns(status)-2)
		label = truncateTailDisplay(label, budget)
		line := prefix + label
		if status != "" {
			line += strings.Repeat(" ", maxInt(1, inner-displayColumns(line)-displayColumns(status))) + status
		}
		if i == state.index {
			line = m.theme.CommandStyle().Padding(0).Render(line)
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
	body = append(body, m.theme.HelpHintTextStyle().Render(truncateTailDisplay("↑↓ Select  Enter Attach  Esc Close", inner)))
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
	state.geometry = subagentOverlayGeometry{x: x, y: y, width: w, height: h, rows: rowOffsets, closeX: x + w - 2, closeY: y + inset}
	return frame
}

func (m *Model) handleSessionPickerMouse(msg tea.MouseMsg) tea.Cmd {
	state, mouse := m.sessionPicker, msg.Mouse()
	g := state.geometry
	inside := mouse.X >= g.x && mouse.X < g.x+g.width && mouse.Y >= g.y && mouse.Y < g.y+g.height
	row := subagentRowAtY(g.rows, mouse.Y)
	switch msg.(type) {
	case tea.MouseWheelMsg:
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
		if mouse.Y == g.closeY && mouse.X >= g.closeX-1 {
			state.pressed = "close"
		} else if row >= 0 {
			state.index = row
			state.pressed = fmt.Sprint(row)
		}
	case tea.MouseReleaseMsg:
		pressed := state.pressed
		state.pressed = ""
		if !inside || (mouse.Button != tea.MouseLeft && mouse.Button != tea.MouseNone) {
			return nil
		}
		if pressed == "close" && mouse.Y == g.closeY && mouse.X >= g.closeX-1 {
			m.closeSessionPicker()
		} else if row >= 0 && pressed == fmt.Sprint(row) {
			state.index = row
			return m.selectSessionPicker()
		}
	}
	return nil
}
