package tuiapp

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/charmbracelet/x/ansi"
)

func TestSessionPickerOpensWhileTurnRunsAndMarksRunningRows(t *testing.T) {
	rows := []ResumeCandidate{
		{SessionID: "session-running", Title: "Background review", Running: true},
		{SessionID: "session-idle", Title: "Earlier work"},
	}
	model := newSessionPickerTestModel(t, 80, 24, rows, nil)
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())

	openSessionPickerForTest(t, model)
	if !model.turnRunning() {
		t.Fatal("opening Session picker interrupted the running Turn")
	}
	if got := ansi.Strip(model.renderSessionPicker()); !strings.Contains(got, "running") {
		t.Fatalf("Session picker omitted running marker:\n%s", got)
	}
}

func TestSessionPickerCompletionClearsCommandBeforeLoading(t *testing.T) {
	for _, key := range []string{"enter", "tab"} {
		t.Run(key, func(t *testing.T) {
			m := newSessionPickerTestModel(t, 80, 24, []ResumeCandidate{{SessionID: "session-a", Title: "Earlier work"}}, func(Submission) TaskResultMsg {
				return TaskResultMsg{ContinueRunning: true}
			})
			m.setCommands(DefaultCommands())
			m.setInputText("/res")
			m.syncTextareaFromInput()
			m.refreshSlashCommands()
			_, load := m.Update(keyPress(key))
			if load == nil || m.sessionPicker == nil || !m.sessionPicker.loading {
				t.Fatal("completion did not open loading Session picker")
			}
			if m.textarea.Value() != "" || len(m.input) != 0 {
				t.Fatalf("completion retained command: %q", m.textarea.Value())
			}
			frames := []string{m.View().Content}
			m.Update(load())
			frames = append(frames, m.View().Content)
			_, attach := m.Update(keyPress("enter"))
			if attach == nil || !m.sessionSwitchPending || m.textarea.Value() != "" {
				t.Fatal("selection did not clear composer before attachment")
			}
			m.Update(sessionViewStartMsg{generation: 1, state: appserver.SessionState{SessionID: "session-a"}})
			frames = append(frames, m.View().Content)
			if !strings.Contains(ansi.Strip(frames[len(frames)-1]), sessionHistoryLoadingHint) || m.textarea.Value() != "" {
				t.Fatal("loading history did not paint with an empty composer")
			}
			updates := renderFullscreenFramesForTest(t, m.width, m.height, frames...)
			assertPhysicalFullscreenFrame(t, m.width, m.height, frames[len(frames)-1], updates)
		})
	}
}

func TestSessionPickerShortcutPreservesDraft(t *testing.T) {
	m := newSessionPickerTestModel(t, 80, 24, nil, nil)
	m.setInputText("unfinished draft")
	m.syncTextareaFromInput()
	openSessionPickerForTest(t, m)
	m.Update(keyPress("esc"))
	if m.textarea.Value() != "unfinished draft" {
		t.Fatal("Session shortcut discarded the draft")
	}
}

func TestSessionPickerEscapeOnlyClosesAndDoesNotInterrupt(t *testing.T) {
	interrupts := 0
	model := newSessionPickerTestModel(t, 80, 24, []ResumeCandidate{{SessionID: "session-a", Title: "A"}}, nil)
	model.cfg.CancelRunning = func() bool {
		interrupts++
		return true
	}
	model.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	openSessionPickerForTest(t, model)

	updated, cmd := model.Update(keyPress("esc"))
	model = updated.(*Model)
	if cmd != nil {
		t.Fatal("closing Session picker returned a command")
	}
	if model.sessionPicker != nil || !model.turnRunning() || interrupts != 0 {
		t.Fatalf("Esc changed Turn state: picker=%v running=%v interrupts=%d", model.sessionPicker != nil, model.turnRunning(), interrupts)
	}
}

func TestSessionPickerKeyboardSelectionAttachesSelectedSession(t *testing.T) {
	var submitted []Submission
	rows := []ResumeCandidate{
		{SessionID: "session-alpha", Title: "Alpha"},
		{SessionID: "session-bravo", Title: "Bravo"},
	}
	model := newSessionPickerTestModel(t, 80, 24, rows, func(submission Submission) TaskResultMsg {
		submitted = append(submitted, submission)
		return TaskResultMsg{ContinueRunning: true}
	})
	openSessionPickerForTest(t, model)

	updated, cmd := model.Update(keyPress("down"))
	model = updated.(*Model)
	if cmd != nil || model.sessionPicker == nil || model.sessionPicker.index != 1 {
		t.Fatalf("down selection: picker=%v index=%d cmd=%v", model.sessionPicker != nil, model.sessionPicker.index, cmd != nil)
	}
	updated, cmd = model.Update(keyPress("enter"))
	model = updated.(*Model)
	if model.sessionPicker != nil {
		t.Fatal("Enter did not close the Session picker")
	}
	if cmd == nil {
		t.Fatal("Enter did not schedule the selected Session attachment")
	}
	_ = cmd()
	if len(submitted) != 1 || submitted[0].Text != "/resume session-bravo" {
		t.Fatalf("selected Session submission = %#v, want /resume session-bravo", submitted)
	}
}

func TestSessionPickerMouseSelectionAttachesSelectedSession(t *testing.T) {
	var submitted []Submission
	rows := []ResumeCandidate{
		{SessionID: "session-alpha", Title: "Alpha"},
		{SessionID: "session-bravo", Title: "Bravo"},
	}
	model := newSessionPickerTestModel(t, 80, 24, rows, func(submission Submission) TaskResultMsg {
		submitted = append(submitted, submission)
		return TaskResultMsg{ContinueRunning: true}
	})
	openSessionPickerForTest(t, model)
	model.renderSessionPicker()
	geometry := model.sessionPicker.geometry
	if len(geometry.rows) < 2 || geometry.rows[1] < 0 {
		t.Fatalf("Session picker did not cache second row geometry: %+v", geometry)
	}
	point := tea.Mouse{X: geometry.x + 2, Y: geometry.rows[1], Button: tea.MouseLeft}
	updated, cmd := model.handleMouse(tea.MouseClickMsg(point))
	model = updated.(*Model)
	if cmd != nil || model.sessionPicker == nil || model.sessionPicker.index != 1 {
		t.Fatalf("mouse press: picker=%v index=%d cmd=%v", model.sessionPicker != nil, model.sessionPicker.index, cmd != nil)
	}
	point.Button = tea.MouseNone
	updated, cmd = model.handleMouse(tea.MouseReleaseMsg(point))
	model = updated.(*Model)
	if model.sessionPicker != nil {
		t.Fatal("mouse release did not close the Session picker")
	}
	if cmd == nil {
		t.Fatal("mouse release did not schedule the selected Session attachment")
	}
	_ = cmd()
	if len(submitted) != 1 || submitted[0].Text != "/resume session-bravo" {
		t.Fatalf("mouse selected Session submission = %#v, want /resume session-bravo", submitted)
	}
}

func TestSessionPickerNarrowCJKRowsStayInPhysicalFrame(t *testing.T) {
	rows := []ResumeCandidate{
		{SessionID: "session-one", Title: "处理窄终端恢复会话名称显示异常", Running: true},
		{SessionID: "session-two", Title: "请核对工作树状态并继续完成修复"},
	}
	model := newSessionPickerTestModel(t, 36, 12, rows, nil)
	openSessionPickerForTest(t, model)
	frames := []string{model.View().Content}
	model.sessionPicker.index = 1
	frames = append(frames, model.View().Content)

	plain := ansi.Strip(frames[len(frames)-1])
	for _, want := range []string{"处理窄终端", "请核对工作树", "running"} {
		if !strings.Contains(plain, want) {
			t.Fatalf("narrow Session picker omitted %q:\n%s", want, plain)
		}
	}
	for i, line := range strings.Split(frames[len(frames)-1], "\n") {
		if width := displayColumns(ansi.Strip(line)); width > model.width {
			t.Fatalf("frame row %d width=%d, terminal width=%d: %q", i, width, model.width, ansi.Strip(line))
		}
	}
	updates := renderFullscreenFramesForTest(t, model.width, model.height, frames...)
	assertPhysicalFullscreenFrame(t, model.width, model.height, frames[len(frames)-1], updates)
}

func TestSessionPickerCompactAge(t *testing.T) {
	now := time.Date(2026, time.September, 12, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name string
		at   time.Time
		want string
	}{
		{name: "unknown", want: "—"},
		{name: "future", at: now.Add(time.Minute), want: "now"},
		{name: "recent", at: now.Add(-59 * time.Second), want: "now"},
		{name: "minute", at: now.Add(-time.Minute), want: "1m"},
		{name: "before hour", at: now.Add(-time.Hour + time.Second), want: "59m"},
		{name: "hour", at: now.Add(-time.Hour), want: "1h"},
		{name: "before day", at: now.Add(-24*time.Hour + time.Second), want: "23h"},
		{name: "day", at: now.Add(-24 * time.Hour), want: "1d"},
		{name: "days", at: now.Add(-5 * 24 * time.Hour), want: "5d"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := sessionPickerAge(tt.at, now); got != tt.want {
				t.Fatalf("age = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSessionPickerResponsiveWidthAndRightAlignedAges(t *testing.T) {
	now := time.Now()
	rows := []ResumeCandidate{
		{SessionID: "session-one", Title: strings.Repeat("会话标题 ", 60), Running: true, Age: "obsolete age", UpdatedAt: now.Add(-90 * time.Second)},
		{SessionID: "session-two", Prompt: "Summary\nfrom prompt", UpdatedAt: now.Add(-90 * time.Minute)},
		{SessionID: "session-three", UpdatedAt: now.Add(-5*24*time.Hour - time.Hour)},
	}
	for _, width := range []int{24, 36, 80, 180, 240} {
		t.Run(fmt.Sprint(width), func(t *testing.T) {
			model := newSessionPickerTestModel(t, width, 24, rows, nil)
			model.currentSessionID = rows[0].SessionID
			openSessionPickerForTest(t, model)
			frame := model.View().Content
			g := model.sessionPicker.geometry
			if g.width != width-4 {
				t.Fatalf("picker width = %d, want %d", g.width, width-4)
			}
			lines := strings.Split(ansi.Strip(frame), "\n")
			inset := model.overlayBorderChromeWidth() / 2
			for i, age := range []string{"1m", "1h", "5d"} {
				line := sliceByDisplayColumns(lines[g.rows[i]], g.x+inset, g.x+g.width-inset)
				if !strings.HasSuffix(line, "  "+age) {
					t.Fatalf("row %d age not right aligned: %q", i, line)
				}
				if width >= 80 && i == 0 && !strings.Contains(line, "running · current") {
					t.Fatalf("row omitted Session status: %q", line)
				}
				if i == 0 && !strings.HasPrefix(line, "> 会话") {
					t.Fatalf("title not left aligned: %q", line)
				}
				if i == 1 && width >= 36 && !strings.HasPrefix(line, "  Summary from prompt") {
					t.Fatalf("prompt fallback not left aligned: %q", line)
				}
			}
			if strings.Contains(ansi.Strip(frame), "obsolete age") {
				t.Fatal("picker used the non-compact age instead of UpdatedAt")
			}
			for i, line := range strings.Split(frame, "\n") {
				if got := displayColumns(line); got > width {
					t.Fatalf("row %d width = %d, terminal width = %d", i, got, width)
				}
			}
			updates := renderFullscreenFramesForTest(t, width, model.height, frame)
			assertPhysicalFullscreenFrame(t, width, model.height, frame, updates)
			if width == 180 {
				t.Logf("Session picker %dx%d:\n%s", width, model.height, ansi.Strip(frame))
			}
		})
	}
}

func TestSessionPickerHoverKeepsVisibleRowsStationary(t *testing.T) {
	rows := make([]ResumeCandidate, 30)
	for i := range rows {
		rows[i] = ResumeCandidate{SessionID: fmt.Sprintf("session-%02d", i), Title: fmt.Sprintf("Session %02d", i)}
	}
	model := newSessionPickerTestModel(t, 100, 16, rows, nil)
	if model.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("unexpected baseline mouse mode")
	}
	openSessionPickerForTest(t, model)
	if model.View().MouseMode != tea.MouseModeAllMotion {
		t.Fatal("Session picker did not request terminal hover events")
	}
	frames := []string{model.View().Content}
	model.Update(keyPress("end"))
	frames = append(frames, model.View().Content)
	g := model.sessionPicker.geometry
	start := model.sessionPicker.offset
	if start == 0 {
		t.Fatal("End did not scroll the Session list")
	}
	for _, row := range []int{start, start + 2, start + 1} {
		_, cmd := model.Update(tea.MouseMotionMsg{X: g.x + 3, Y: g.rows[row]})
		if cmd != nil || model.sessionPicker == nil || model.sessionPicker.index != row {
			t.Fatalf("hover did not select row %d without attaching", row)
		}
		frames = append(frames, model.View().Content)
		if model.sessionPicker.offset != start || !slices.Equal(g.rows, model.sessionPicker.geometry.rows) {
			t.Fatal("hover moved the visible Session rows")
		}
		line := strings.Split(ansi.Strip(frames[len(frames)-1]), "\n")[g.rows[row]]
		if !strings.Contains(line, "> "+rows[row].Title) {
			t.Fatalf("hover selection was not rendered: %q", line)
		}
	}
	selected := model.sessionPicker.index
	for _, x := range []int{0, g.x, g.x + 1, g.x + g.width - 1, model.width - 1} {
		model.Update(tea.MouseMotionMsg{X: x, Y: g.rows[start]})
		if model.sessionPicker.index != selected {
			t.Fatalf("hover on border/outside at x=%d changed selection", x)
		}
	}
	model.Update(tea.MouseWheelMsg{X: 0, Y: 0, Button: tea.MouseWheelDown})
	if model.sessionPicker.index != selected {
		t.Fatal("wheel outside the picker changed selection")
	}
	updates := renderFullscreenFramesForTest(t, model.width, model.height, frames...)
	assertPhysicalFullscreenFrame(t, model.width, model.height, frames[len(frames)-1], updates)
	model.Update(keyPress("esc"))
	if model.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("closing the Session picker did not restore terminal mouse mode")
	}
}

func TestSessionPickerScrollKeepsSelectionVisible(t *testing.T) {
	rows := make([]ResumeCandidate, 30)
	for i := range rows {
		rows[i].SessionID = fmt.Sprintf("session-%02d", i)
	}
	model := newSessionPickerTestModel(t, 100, 16, rows, nil)
	openSessionPickerForTest(t, model)
	model.View()
	for _, tt := range []struct {
		key   string
		index int
	}{
		{key: "pgdown", index: 7},
		{key: "pgdown", index: 14},
		{key: "pgup", index: 7},
		{key: "home", index: 0},
		{key: "end", index: 29},
		{key: "up", index: 28},
	} {
		model.Update(keyPress(tt.key))
		model.View()
		state := model.sessionPicker
		if state.index != tt.index || state.geometry.rows[state.index] < 0 {
			t.Fatalf("%s: selection = %d, want visible row %d", tt.key, state.index, tt.index)
		}
	}
	model.Update(keyPress("home"))
	model.View()
	for i := 1; i <= 10; i++ {
		g := model.sessionPicker.geometry
		model.Update(tea.MouseWheelMsg{X: g.x + 3, Y: g.rows[model.sessionPicker.index], Button: tea.MouseWheelDown})
		model.View()
		if model.sessionPicker.index != i || model.sessionPicker.geometry.rows[i] < 0 {
			t.Fatalf("wheel: row %d not selected and visible", i)
		}
	}
	model.Update(tea.WindowSizeMsg{Width: 80, Height: 12})
	model.View()
	if model.sessionPicker.geometry.rows[model.sessionPicker.index] < 0 {
		t.Fatal("resize hid the selected Session")
	}
}

func TestSessionPickerMouseReleaseRequiresSameSession(t *testing.T) {
	for _, target := range []string{"other row", "outside", "border", "reordered row"} {
		t.Run(target, func(t *testing.T) {
			rows := []ResumeCandidate{{SessionID: "session-alpha", Title: "Alpha"}, {SessionID: "session-bravo", Title: "Bravo"}}
			model := newSessionPickerTestModel(t, 100, 24, rows, nil)
			openSessionPickerForTest(t, model)
			model.View()
			g := model.sessionPicker.geometry
			point := tea.Mouse{X: g.x + 3, Y: g.rows[0], Button: tea.MouseLeft}
			model.Update(tea.MouseClickMsg(point))
			switch target {
			case "other row":
				point.Y = g.rows[1]
			case "outside":
				point.X = 0
			case "border":
				point.X = g.x
			case "reordered row":
				model.Update(sessionPickerResultMsg{request: model.sessionPicker.request, rows: []ResumeCandidate{rows[1], rows[0]}})
				model.View()
			}
			model.Update(tea.MouseMotionMsg(point))
			point.Button = tea.MouseNone
			_, cmd := model.Update(tea.MouseReleaseMsg(point))
			if cmd != nil || model.sessionPicker == nil {
				t.Fatal("release attached a different Session or a non-row target")
			}
		})
	}
}

func TestSessionPickerMouseCloseUsesRenderedTarget(t *testing.T) {
	for _, width := range []int{36, 100} {
		model := newSessionPickerTestModel(t, width, 24, nil, nil)
		openSessionPickerForTest(t, model)
		frame := ansi.Strip(model.View().Content)
		g := model.sessionPicker.geometry
		if got := sliceByDisplayColumns(strings.Split(frame, "\n")[g.closeY], g.closeX, g.closeX+1); got != "×" {
			t.Fatalf("close geometry points at %q, not ×", got)
		}
		model.Update(tea.MouseClickMsg{X: g.closeX, Y: g.closeY, Button: tea.MouseLeft})
		_, cmd := model.Update(tea.MouseReleaseMsg{X: g.closeX, Y: g.closeY, Button: tea.MouseNone})
		if cmd != nil || model.sessionPicker != nil {
			t.Fatal("clicking × did not close the picker without attaching")
		}
	}
}

func newSessionPickerTestModel(t *testing.T, width, height int, rows []ResumeCandidate, execute func(Submission) TaskResultMsg) *Model {
	t.Helper()
	model := NewModel(Config{
		NoColor:     true,
		NoAnimation: true,
		ListSessions: func(context.Context) ([]ResumeCandidate, error) {
			return append([]ResumeCandidate(nil), rows...), nil
		},
		ExecuteLine: execute,
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return updated.(*Model)
}

func openSessionPickerForTest(t *testing.T, model *Model) {
	t.Helper()
	updated, cmd := model.Update(keyPress("ctrl+o"))
	model = updated.(*Model)
	if model.sessionPicker == nil {
		t.Fatal("Ctrl+O did not open Session picker")
	}
	if cmd == nil {
		t.Fatal("opening Session picker did not schedule list load")
	}
	updated, _ = model.Update(cmd())
	*model = *updated.(*Model)
}
