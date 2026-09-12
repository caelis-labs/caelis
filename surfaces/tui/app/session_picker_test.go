package tuiapp

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
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
