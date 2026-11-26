package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

const sessionPickerPageSize = 40

type sessionPickerLine struct {
	heading string
	row     int // -1 for a heading or spacer.
}

type sessionPickerState struct {
	rows             []ResumeCandidate
	lines            []sessionPickerLine
	positions        []int
	index            int
	offset           int
	loading          bool
	loadingCursor    string
	morePending      bool
	paged            bool
	err              string
	failedCursor     string
	headCursor       string // First continuation of the current traversal.
	nextCursor       string
	request          uint64
	refresh          uint64
	cancel           context.CancelFunc
	listWidth        int
	visible          map[int]int // physical terminal row -> selectable row index
	pressed          string
	detailsSessionID string
	detailsOffset    int
	detailsOverflow  int
}

type sessionPickerResultMsg struct {
	request   uint64
	rows      []ResumeCandidate
	refreshed []ResumeCandidate
	checked   []string
	cursor    string
	next      string
	err       error
}

type sessionPickerRefreshMsg struct{ request, refresh uint64 }

func (m *Model) openSessionPicker() tea.Cmd {
	if m.sessionPicker != nil || m.sessionSwitchPending {
		return nil
	}
	m.clearInputOverlays()
	m.sessionPickerSeq++
	m.sessionPicker = &sessionPickerState{request: m.sessionPickerSeq}
	return m.loadSessionPicker("")
}

func (m *Model) closeSessionPicker() {
	if m.sessionPicker != nil && m.sessionPicker.cancel != nil {
		m.sessionPicker.cancel()
	}
	m.sessionPicker = nil
	if m.viewportDirty {
		m.viewportDirty = false
		m.syncViewportContent()
	}
}

func (m *Model) loadSessionPicker(cursor string) tea.Cmd {
	state := m.sessionPicker
	if state == nil || state.loading || m.cfg.SessionWorkbench == nil && m.cfg.ListSessions == nil {
		return nil
	}
	state.loading = true
	state.loadingCursor = cursor
	state.refresh++
	ctx, cancel := context.WithCancel(contextOrBackground(m.cfg.Context))
	state.cancel = cancel
	request, workbench, list := state.request, m.cfg.SessionWorkbench, m.cfg.ListSessions
	params := appserver.SessionWorkbenchRequest{Cursor: cursor, Limit: sessionPickerPageSize}
	if cursor == "" && len(state.visible) != 0 {
		for y := sessionPickerTop; y < m.height && len(params.RefreshIDs) < 100; y++ {
			if index, ok := state.visible[y]; ok && index < len(state.rows) {
				params.RefreshIDs = append(params.RefreshIDs, state.rows[index].SessionID)
			}
		}
	}
	return func() tea.Msg {
		if workbench != nil {
			page, err := workbench(ctx, params)
			var checked []string
			if page.Refreshed != nil {
				checked = params.RefreshIDs
			}
			return sessionPickerResultMsg{
				request: request, cursor: cursor, next: page.NextCursor, err: err,
				rows: pickerRows(page.Sessions), refreshed: pickerRows(page.Refreshed), checked: checked,
			}
		}
		rows, err := list(ctx)
		return sessionPickerResultMsg{request: request, rows: rows, err: err}
	}
}

func pickerRows(rows []appserver.SessionWorkbenchRow) []ResumeCandidate {
	out := make([]ResumeCandidate, len(rows))
	for i, row := range rows {
		out[i] = ResumeCandidate{
			SessionID: row.SessionID, Workspace: row.CWD, WorkspaceKey: row.WorkspaceKey,
			Title: row.Title, UpdatedAt: row.UpdatedAt, Running: row.Active, Unread: row.Unread, Failed: row.Failed,
		}
	}
	return out
}

func (m *Model) applySessionPickerResult(msg sessionPickerResultMsg) tea.Cmd {
	state := m.sessionPicker
	if state == nil || state.request != msg.request {
		return nil
	}
	state.loading = false
	state.loadingCursor = ""
	morePending := state.morePending
	state.morePending = false
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	if msg.cursor != "" && errorcode.Is(msg.err, errorcode.Conflict) {
		// Keep the mounted rows and selection while replacing the stale traversal.
		state.headCursor = ""
		state.nextCursor = ""
		state.paged = false
		state.err = ""
		state.failedCursor = ""
		return m.loadSessionPicker("")
	}
	if msg.err != nil {
		state.err = msg.err.Error()
		state.failedCursor = msg.cursor
	} else {
		state.err = ""
		state.failedCursor = ""
		selected := state.selectionID()
		if len(state.rows) == 0 {
			selected = m.currentSessionID
		}
		if msg.cursor != "" {
			state.nextCursor = msg.next
			state.paged = true
		} else if !state.paged || msg.next == "" || appserver.SessionWorkbenchPriorityChanged(state.headCursor, msg.next) {
			state.headCursor = msg.next
			state.nextCursor = msg.next
			state.paged = false
		}
		if m.cfg.SessionWorkbench == nil {
			state.rows = msg.rows
		} else {
			state.mergeRows(msg.rows, msg.refreshed, msg.checked)
		}
		state.groupRows()
		state.index = clampInt(state.index, 0, max(0, state.lastIndex()))
		for i, row := range state.rows {
			if row.SessionID == selected {
				state.index = i
				break
			}
		}
		state.visible = nil
		if morePending && state.index == state.lastIndex() && state.nextCursor != "" {
			return m.loadSessionPicker(state.nextCursor)
		}
	}
	refresh := sessionPickerRefreshMsg{request: msg.request, refresh: state.refresh}
	return tea.Tick(3*time.Second, func(time.Time) tea.Msg { return refresh })
}

func (s *sessionPickerState) mergeRows(rows, refreshed []ResumeCandidate, checked []string) {
	missing := make(map[string]bool, len(checked))
	for _, id := range checked {
		missing[id] = true
	}
	for _, row := range refreshed {
		delete(missing, row.SessionID)
	}
	for _, row := range rows {
		delete(missing, row.SessionID)
	}
	index := make(map[string]int, len(s.rows)+len(rows))
	kept := s.rows[:0]
	for _, row := range s.rows {
		if !missing[row.SessionID] {
			index[row.SessionID] = len(kept)
			kept = append(kept, row)
		}
	}
	s.rows = kept
	for _, row := range rows {
		if i, ok := index[row.SessionID]; ok {
			s.rows[i] = row
		} else {
			index[row.SessionID] = len(s.rows)
			s.rows = append(s.rows, row)
		}
	}
	for _, row := range refreshed {
		if i, ok := index[row.SessionID]; ok {
			s.rows[i] = row
		}
	}
}

func (s *sessionPickerState) groupRows() {
	type workspace struct{ key, cwd string }
	var order []workspace
	groups := make(map[workspace][]ResumeCandidate)
	names := make(map[workspace]string)
	nameCount := make(map[string]int)
	for _, row := range s.rows {
		key := workspace{row.WorkspaceKey, row.Workspace}
		if _, ok := groups[key]; !ok {
			order = append(order, key)
			name := sessionPickerLabel(firstNonEmpty(displayPathBase(key.cwd), key.key, "Workspace"))
			names[key] = name
			nameCount[name]++
		}
		groups[key] = append(groups[key], row)
	}
	s.rows = s.rows[:0]
	s.lines, s.positions = s.lines[:0], s.positions[:0]
	for i, key := range order {
		if i != 0 {
			s.lines = append(s.lines, sessionPickerLine{row: -1})
		}
		heading := names[key]
		if nameCount[heading] > 1 && key.cwd != "" {
			heading = sessionPickerLabel(key.cwd)
		}
		s.lines = append(s.lines, sessionPickerLine{heading: heading, row: -1})
		for _, row := range groups[key] {
			s.positions = append(s.positions, len(s.lines))
			s.lines = append(s.lines, sessionPickerLine{heading: heading, row: len(s.rows)})
			s.rows = append(s.rows, row)
		}
	}
}

func (s *sessionPickerState) lastIndex() int { return len(s.positions) - 1 }

// scrollList keeps the selection visible and returns the heading to repeat
// when the list starts inside a workspace group.
func (s *sessionPickerState) scrollList(height int) string {
	if len(s.positions) == 0 {
		return ""
	}
	position := s.positions[s.index]
	lastOffset := max(0, len(s.lines)-height)
	if height > 1 && s.lines[lastOffset].row >= 0 && s.lines[lastOffset].heading != "" {
		lastOffset++
	}
	s.offset = min(clampInt(s.offset, max(0, position-height+1), position), lastOffset)
	line := s.lines[s.offset]
	if height == 1 || line.row < 0 || line.heading == "" {
		return ""
	}
	if position-s.offset == height-1 {
		s.offset++ // The repeated heading also occupies a terminal row.
		line = s.lines[s.offset]
	}
	if line.row >= 0 {
		return line.heading
	}
	return ""
}

func (s *sessionPickerState) scrollDetails(delta int) {
	s.detailsOffset = clampInt(s.detailsOffset+delta, 0, s.detailsOverflow)
}

func (s *sessionPickerState) selectionID() string {
	if s.index < len(s.rows) {
		return s.rows[s.index].SessionID
	}
	return ""
}

func (m *Model) moveSessionPicker(next int) tea.Cmd {
	state := m.sessionPicker
	forward := next > state.index
	last := state.lastIndex()
	state.index = clampInt(next, 0, max(0, last))
	if state.index != last {
		state.morePending = false
		return nil
	}
	if !forward || state.nextCursor == "" {
		return nil
	}
	if state.loading {
		state.morePending = state.loadingCursor == ""
		return nil
	}
	return m.loadSessionPicker(state.nextCursor)
}

func (m *Model) selectSessionPicker() tea.Cmd {
	state := m.sessionPicker
	switch id := state.selectionID(); {
	case id == "" && state.err != "":
		return m.loadSessionPicker("")
	case id == "":
		return nil
	case id == m.currentSessionID && !m.sessionHistoryFailed && !m.sessionObservationRecovering:
		m.closeSessionPicker()
		return m.scheduleSessionRead()
	default:
		m.closeSessionPicker()
		return m.executeLineCmd(Submission{Text: "/resume " + id})
	}
}

func (m *Model) handleSessionPickerKey(msg tea.KeyMsg) tea.Cmd {
	if _, release := msg.(tea.KeyReleaseMsg); release {
		return nil
	}
	state := m.sessionPicker
	state.pressed = ""
	page := max(1, m.height-sessionPickerTop-sessionPickerBottom-1)
	var next int
	switch msg.String() {
	case "esc", "ctrl+o":
		m.closeSessionPicker()
		return m.scheduleSessionRead()
	case "up":
		next = state.index - 1
	case "down":
		next = state.index + 1
	case "shift+up":
		state.scrollDetails(-1)
		return nil
	case "shift+down":
		state.scrollDetails(1)
		return nil
	case "pgup":
		next = state.index - page
	case "pgdown":
		next = state.index + page
	case "home":
		next = 0
	case "end":
		next = state.lastIndex() + 1
	case "enter", "right":
		return m.selectSessionPicker()
	case "n":
		if m.cfg.executeLineCmd == nil && m.cfg.ExecuteLine == nil {
			return nil
		}
		submission := Submission{Text: "/new"}
		if state.index < len(state.rows) {
			row := state.rows[state.index]
			submission.newSessionWorkspace = session.WorkspaceRef{Key: row.WorkspaceKey, CWD: row.Workspace}
		}
		m.closeSessionPicker()
		return m.executeLineCmd(submission)
	case "r":
		return m.loadSessionPicker(state.failedCursor)
	default:
		return nil
	}
	return m.moveSessionPicker(next)
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
	row, visible := state.visible[mouse.Y]
	inside := mouse.X >= sessionPickerInset && mouse.X < state.listWidth-sessionPickerInset
	switch msg.(type) {
	case tea.MouseMotionMsg:
		if visible && inside {
			state.index = row
		}
	case tea.MouseWheelMsg:
		state.pressed = ""
		if mouse.Y < sessionPickerTop || mouse.Y >= m.height-sessionPickerBottom {
			return nil
		}
		delta := 0
		switch mouse.Button {
		case tea.MouseWheelUp:
			delta = -1
		case tea.MouseWheelDown:
			delta = 1
		}
		if inside {
			return m.moveSessionPicker(state.index + delta)
		} else if mouse.X >= state.listWidth-sessionPickerInset {
			state.scrollDetails(delta)
		}
	case tea.MouseClickMsg:
		state.pressed = ""
		if visible && inside && mouse.Button == tea.MouseLeft {
			state.index = row
			state.pressed = state.selectionID()
		}
	case tea.MouseReleaseMsg:
		pressed := state.pressed
		state.pressed = ""
		if visible && inside && pressed != "" && (mouse.Button == tea.MouseLeft || mouse.Button == tea.MouseNone) {
			state.index = row
			if state.selectionID() == pressed {
				return m.selectSessionPicker()
			}
		}
	}
	return nil
}

func sessionPickerTitle(row ResumeCandidate) string {
	return firstNonEmpty(sessionPickerLabel(firstNonEmpty(row.Title, row.Prompt)), "Untitled session")
}

func sessionPickerLabel(text string) string {
	return strings.Join(strings.Fields(tuikit.SanitizeLogText(text)), " ")
}
