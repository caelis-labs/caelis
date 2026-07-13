package tuiapp

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

func TestCopySelectionToClipboardRunsAsCommand(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := NewModel(Config{
		WriteClipboardText: func(text string) error {
			if text != "selected text" {
				t.Errorf("unexpected clipboard text %q", text)
			}
			close(started)
			<-release
			return nil
		},
	})

	cmd := model.copySelectionToClipboard("selected text")
	if cmd == nil {
		t.Fatal("expected clipboard command")
	}
	select {
	case <-started:
		t.Fatal("clipboard writer ran synchronously")
	default:
	}

	result := make(chan any, 1)
	go func() {
		result <- cmd()
	}()

	select {
	case <-started:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("clipboard command did not start")
	}
	close(release)

	select {
	case msg := <-result:
		if got, ok := msg.(clipboardCopyResultMsg); !ok {
			t.Fatalf("expected clipboardCopyResultMsg, got %T", msg)
		} else if got.err != nil {
			t.Fatalf("unexpected clipboard error: %v", got.err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("clipboard command did not finish")
	}
}

func TestNewSessionBackendWorkRunsOutsideModelUpdate(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	model := NewModel(Config{
		ExecuteLine: func(submission Submission) TaskResultMsg {
			if submission.Text != "/new" {
				t.Errorf("submission text = %q, want /new", submission.Text)
			}
			close(started)
			<-release
			return TaskResultMsg{SuppressTurnDivider: true}
		},
	})

	cmd := model.executeLineCmd(Submission{Text: "/new"})
	select {
	case <-started:
		t.Fatal("/new backend work ran synchronously while creating tea.Cmd")
	default:
	}
	done := make(chan tea.Msg, 1)
	go func() { done <- cmd() }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("/new backend command did not start")
	}

	begin := time.Now()
	next, _ := model.Update(keyPress("x"))
	if time.Since(begin) > 50*time.Millisecond {
		t.Fatal("model update blocked behind /new backend work")
	}
	if got := next.(*Model).textarea.Value(); got != "x" {
		t.Fatalf("composer input while /new is pending = %q, want x", got)
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("/new backend command did not finish")
	}
}

func TestViewportSelectionMotionDedupesSameEndpoint(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(10)
	m.viewportStyledLines = []string{"hello world"}
	m.viewportPlainLines = []string{"hello world"}
	m.selecting = true
	m.selectionStart = textSelectionPoint{line: 0, col: 0}
	m.selectionEnd = textSelectionPoint{line: 0, col: 5}
	version := m.viewportSelectionVersion

	_ = m.handleViewportMouseMotion(tea.Mouse{X: m.mainColumnX() + tuikit.GutterNarrative + 5, Y: 0})
	if got := m.viewportSelectionVersion; got != version {
		t.Fatalf("selection version after duplicate endpoint = %d, want %d", got, version)
	}

	_ = m.handleViewportMouseMotion(tea.Mouse{X: m.mainColumnX() + tuikit.GutterNarrative + 6, Y: 0})
	if got := m.viewportSelectionVersion; got != version+1 {
		t.Fatalf("selection version after changed endpoint = %d, want %d", got, version+1)
	}
}

func TestViewportSelectionAutoScrollsAtVerticalEdges(t *testing.T) {
	tests := []struct {
		name        string
		startOffset int
		mouseY      int
		wantOffset  int
		wantLine    int
	}{
		{name: "bottom edge scrolls down", startOffset: 0, mouseY: 2, wantOffset: 1, wantLine: 3},
		{name: "top edge scrolls up", startOffset: 3, mouseY: 0, wantOffset: 2, wantLine: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := NewModel(Config{})
			updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
			m := updated.(*Model)
			m.viewport.SetWidth(40)
			m.viewport.SetHeight(3)
			m.viewportStyledLines = []string{
				"line 0",
				"line 1",
				"line 2",
				"line 3",
				"line 4",
				"line 5",
			}
			m.viewportPlainLines = append([]string(nil), m.viewportStyledLines...)
			m.viewport.SetContentLines(m.viewportStyledLines)
			m.viewport.SetYOffset(tt.startOffset)
			m.selecting = true
			m.selectionStart = textSelectionPoint{line: tt.startOffset, col: 0}
			m.selectionEnd = m.selectionStart
			m.enterViewportSelecting()

			mouse := tea.Mouse{
				Button: tea.MouseLeft,
				X:      m.mainColumnX() + tuikit.GutterNarrative + 2,
				Y:      tt.mouseY,
			}
			cmd := m.handleViewportMouseMotion(mouse)
			if cmd == nil {
				t.Fatal("edge motion should schedule selection auto-scroll")
			}

			updated, nextCmd := m.Update(frameTickMsg{kind: frameTickSelectionScroll, at: time.Now()})
			m = updated.(*Model)
			if got := m.viewport.YOffset(); got != tt.wantOffset {
				t.Fatalf("viewport offset = %d, want %d", got, tt.wantOffset)
			}
			if got := m.selectionEnd.line; got != tt.wantLine {
				t.Fatalf("selection end line = %d, want %d", got, tt.wantLine)
			}
			if m.viewportScrollbarVisibleUntil.IsZero() {
				t.Fatal("edge auto-scroll should touch the viewport scrollbar")
			}
			if nextCmd == nil {
				t.Fatal("held edge selection should schedule the next auto-scroll tick")
			}
		})
	}
}

func TestViewportSelectionMouseWheelExtendsSelectionAfterScroll(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(3)
	m.viewport.MouseWheelDelta = 2
	m.viewportStyledLines = []string{
		"line 0",
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
	}
	m.viewportPlainLines = append([]string(nil), m.viewportStyledLines...)
	m.viewport.SetContentLines(m.viewportStyledLines)
	m.selecting = true
	m.selectionStart = textSelectionPoint{line: 0, col: 0}
	m.selectionEnd = textSelectionPoint{line: 1, col: 2}
	m.enterViewportSelecting()

	mouse := tea.Mouse{
		Button: tea.MouseWheelDown,
		X:      m.mainColumnX() + tuikit.GutterNarrative + 2,
		Y:      1,
	}
	updated, _ = m.handleMouse(tea.MouseWheelMsg(mouse))
	m = updated.(*Model)
	if got := m.viewport.YOffset(); got != 2 {
		t.Fatalf("viewport offset = %d, want 2", got)
	}
	if got := m.selectionEnd; got != (textSelectionPoint{line: 3, col: 2}) {
		t.Fatalf("selection end = %#v, want line 3 col 2", got)
	}
	if got := m.viewportFollowState; got != viewportSelecting {
		t.Fatalf("viewport follow state = %v, want selecting", got)
	}
	if m.viewportScrollbarVisibleUntil.IsZero() {
		t.Fatal("selection wheel scroll should touch the viewport scrollbar")
	}
}

func TestViewportSelectionUsesInputSelectionStyle(t *testing.T) {
	model := NewModel(Config{})
	model.theme.SelectionFg = lipgloss.Color("#abcdef")
	model.theme.SelectionBg = lipgloss.Color("#123456")
	model.theme.InputSelectionFg = lipgloss.Color("#111111")
	model.theme.InputSelectionBg = lipgloss.Color("#fedcba")
	model.viewport.SetWidth(40)
	model.viewport.SetHeight(3)
	model.viewportStyledLines = []string{"hello world"}
	model.viewportPlainLines = []string{"hello world"}
	model.selectionStart = textSelectionPoint{line: 0, col: 0}
	model.selectionEnd = textSelectionPoint{line: 0, col: 5}

	rendered := strings.Join(model.renderSelectionLines(), "\n")
	want := model.theme.InputSelectionStyle().Render("hello")
	if !strings.Contains(rendered, want) {
		t.Fatalf("viewport selection render missing input selection style %q: %q", want, rendered)
	}
	genericSelection := model.theme.SelectionStyle().Render("hello")
	if strings.Contains(rendered, genericSelection) {
		t.Fatalf("viewport selection render used generic selection style %q: %q", genericSelection, rendered)
	}
}

func TestInputSelectionUsesInputSelectionStyleWithUserBg(t *testing.T) {
	model := NewModel(Config{})
	model.width = 80
	model.theme.UserBg = lipgloss.Color("#141414")
	model.theme.AppBg = lipgloss.Color("#000000")
	model.theme.Focus = lipgloss.Color("#00ff00")
	model.theme.SelectionFg = lipgloss.Color("#abcdef")
	model.theme.SelectionBg = lipgloss.Color("#123456")
	model.theme.InputSelectionFg = lipgloss.Color("#111111")
	model.theme.InputSelectionBg = lipgloss.Color("#fedcba")
	model.textarea.SetValue("hello")
	model.syncInputFromTextarea()

	promptWidth := displayColumns(model.inputPromptPrefix())
	model.inputSelectionStart = textSelectionPoint{line: 0, col: promptWidth}
	model.inputSelectionEnd = textSelectionPoint{line: 0, col: promptWidth + 2}

	rendered := model.renderInputBar()
	want := model.theme.InputSelectionStyle().Render("he")
	if !strings.Contains(rendered, want) {
		t.Fatalf("input selection render missing input selection style %q: %q", want, rendered)
	}
	genericSelection := model.theme.SelectionStyle().Render("he")
	if strings.Contains(rendered, genericSelection) {
		t.Fatalf("input selection render used generic selection style %q: %q", genericSelection, rendered)
	}
	regressed := lipgloss.NewStyle().
		Foreground(model.theme.AppBg).
		Background(model.theme.Focus).
		Render("he")
	if strings.Contains(rendered, regressed) {
		t.Fatalf("input selection render used AppBg/Focus override %q: %q", regressed, rendered)
	}
}

func TestInputSelectionReleaseCopiesAndClearsSelection(t *testing.T) {
	copied := ""
	model := NewModel(Config{
		WriteClipboardText: func(text string) error {
			copied = text
			return nil
		},
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.textarea.SetValue("hello")
	m.syncInputFromTextarea()

	startY, _, ok := m.inputAreaBounds()
	if !ok {
		t.Fatal("input area bounds unavailable")
	}
	startX := inputSelectionMouseX(m, 0)
	endX := inputSelectionMouseX(m, len([]rune("hello")))

	handled, cmd := m.handleInputAreaMouse(tea.Mouse{Button: tea.MouseLeft, X: startX, Y: startY}, mousePhasePress)
	if !handled || cmd != nil {
		t.Fatalf("press handled=%v cmd=%v, want handled without command", handled, cmd != nil)
	}
	handled, cmd = m.handleInputAreaMouse(tea.Mouse{Button: tea.MouseLeft, X: endX, Y: startY}, mousePhaseRelease)
	if !handled || cmd == nil {
		t.Fatalf("release handled=%v cmd=%v, want copy command", handled, cmd != nil)
	}
	if got, ok := cmd().(clipboardCopyResultMsg); !ok {
		t.Fatalf("copy command returned %T, want clipboardCopyResultMsg", got)
	} else if got.err != nil {
		t.Fatalf("copy command returned error: %v", got.err)
	}
	if copied != "hello" {
		t.Fatalf("copied text = %q, want hello", copied)
	}
	if m.inputSelecting || m.inputSelectionStart.line != -1 || m.inputSelectionEnd.line != -1 {
		t.Fatalf("input selection after release = selecting %v start %#v end %#v, want cleared", m.inputSelecting, m.inputSelectionStart, m.inputSelectionEnd)
	}
}

func TestInputSelectionAutoScrollExtendsCopyToBottom(t *testing.T) {
	copied := ""
	model := NewModel(Config{
		WriteClipboardText: func(text string) error {
			copied = text
			return nil
		},
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.textarea.SetValue(strings.Join([]string{
		"line0",
		"line1",
		"line2",
		"line3",
		"line4",
		"line5",
	}, "\n"))
	m.moveTextareaCursorToIndex(0)
	m.syncInputFromTextarea()
	if got := m.composeInputLayout().layout.totalRows; got <= maxInputBarRows {
		t.Fatalf("composer rows = %d, want more than visible max %d", got, maxInputBarRows)
	}

	startY, height, ok := m.inputAreaBounds()
	if !ok {
		t.Fatal("input area bounds unavailable")
	}
	startMouse := tea.Mouse{Button: tea.MouseLeft, X: inputSelectionMouseX(m, 0), Y: startY}
	edgeMouse := tea.Mouse{Button: tea.MouseLeft, X: inputSelectionMouseX(m, len([]rune("line3"))), Y: startY + height - 1}
	outsideMouse := tea.Mouse{Button: tea.MouseLeft, X: inputSelectionMouseX(m, len([]rune("line4"))), Y: startY + height}

	handled, _ := m.handleInputAreaMouse(startMouse, mousePhasePress)
	if !handled {
		t.Fatal("input selection press was not handled")
	}
	handled, cmd := m.handleInputAreaMouse(edgeMouse, mousePhaseMotion)
	if !handled || cmd != nil {
		t.Fatalf("inside-edge motion handled=%v cmd=%v, want handled without input auto-scroll", handled, cmd != nil)
	}
	if got := m.inputSelectionEnd.line; got != 3 {
		t.Fatalf("input selection end line inside edge = %d, want 3", got)
	}

	handled, cmd = m.handleInputAreaMouse(outsideMouse, mousePhaseMotion)
	if !handled || cmd == nil {
		t.Fatalf("outside motion handled=%v cmd=%v, want scheduled input auto-scroll", handled, cmd != nil)
	}
	token := m.selectionAutoScroll.scheduledToken
	if token == 0 {
		t.Fatalf("selection auto-scroll state = %#v, want scheduled token", m.selectionAutoScroll)
	}

	updated, _ = m.Update(frameTickMsg{kind: frameTickSelectionScroll, token: token, at: time.Now()})
	m = updated.(*Model)
	if got := m.inputSelectionEnd.line; got != 4 {
		t.Fatalf("input selection end line = %d, want 4 after bottom-edge auto-scroll", got)
	}

	handled, cmd = m.handleInputAreaMouse(outsideMouse, mousePhaseRelease)
	if !handled || cmd == nil {
		t.Fatalf("release handled=%v cmd=%v, want copy command", handled, cmd != nil)
	}
	if got, ok := cmd().(clipboardCopyResultMsg); !ok {
		t.Fatalf("copy command returned %T, want clipboardCopyResultMsg", got)
	} else if got.err != nil {
		t.Fatalf("copy command returned error: %v", got.err)
	}
	want := strings.Join([]string{"line0", "line1", "line2", "line3", "line4"}, "\n")
	if copied != want {
		t.Fatalf("copied text = %q, want %q", copied, want)
	}
}

func TestInputSelectionCopyMatchesHighlightSpans(t *testing.T) {
	prompt := "> "
	promptWidth := displayColumns(prompt)
	lines := []string{
		prompt + "line0",
		strings.Repeat(" ", promptWidth) + "line1",
		strings.Repeat(" ", promptWidth) + "line2",
	}
	start := textSelectionPoint{line: 0, col: promptWidth}
	end := textSelectionPoint{line: 2, col: promptWidth + displayColumns("line2")}

	got := selectionTextFromInputLines(lines, start, end, promptWidth)
	want := "line0\nline1\nline2"
	if got != want {
		t.Fatalf("selection text = %q, want %q", got, want)
	}
}

func TestInputSelectionCopyPreservesBlankLines(t *testing.T) {
	prompt := "> "
	promptWidth := displayColumns(prompt)
	lines := []string{
		prompt + "line0",
		strings.Repeat(" ", promptWidth),
		strings.Repeat(" ", promptWidth) + "line2",
	}
	start := textSelectionPoint{line: 0, col: promptWidth}
	end := textSelectionPoint{line: 2, col: promptWidth + displayColumns("line2")}

	got := selectionTextFromInputLines(lines, start, end, promptWidth)
	want := "line0\n\nline2"
	if got != want {
		t.Fatalf("selection text = %q, want %q", got, want)
	}
}

func TestFixedFooterHitboxAccountsForComposerChromeRows(t *testing.T) {
	model := NewModel(Config{Workspace: "/tmp/workspace"})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.theme.UserBg = lipgloss.Color("#141414")
	m.theme.NoColor = false
	m.syncTextareaChrome()
	m.ensureViewportLayout()

	frame := ansi.Strip(m.View().Content)
	var footerY = -1
	for y, line := range strings.Split(strings.TrimRight(frame, "\n"), "\n") {
		if strings.Contains(line, "/tmp/workspace") {
			footerY = y
			break
		}
	}
	if footerY < 0 {
		t.Fatalf("rendered frame missing footer workspace:\n%s", frame)
	}

	region, ok := m.fixedRegionAt(footerY)
	if !ok {
		t.Fatalf("fixedRegionAt(%d) missed rendered footer", footerY)
	}
	if region.area != fixedSelectionFooter {
		t.Fatalf("fixedRegionAt(%d) area = %v, want footer", footerY, region.area)
	}
}

func TestInputClickPreservesComposerRowOffset(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.textarea.SetValue(strings.Join([]string{
		"line0",
		"line1",
		"line2",
		"line3",
		"line4",
		"line5",
	}, "\n"))
	m.moveTextareaCursorToIndex(len([]rune("line0\nline1\nline2\nline3\nline4\n")))
	m.syncInputFromTextarea()
	m.composerRowOffset = 2

	startY, _, ok := m.inputAreaBounds()
	if !ok {
		t.Fatal("input area bounds unavailable")
	}
	beforeOffset := m.composerRowOffset
	clickX := inputSelectionMouseX(m, len([]rune("line2")))
	handled, cmd := m.handleInputAreaMouse(tea.Mouse{Button: tea.MouseLeft, X: clickX, Y: startY}, mousePhasePress)
	if !handled || cmd != nil {
		t.Fatalf("press handled=%v cmd=%v, want handled without command", handled, cmd != nil)
	}
	if got := m.composerRowOffset; got != beforeOffset {
		t.Fatalf("composer row offset after click = %d, want unchanged %d", got, beforeOffset)
	}
	layout := m.buildComposeInputLayout()
	if got := layout.rowOffset; got != beforeOffset {
		t.Fatalf("layout row offset after click = %d, want unchanged %d", got, beforeOffset)
	}
}

func TestMouseWheelInsideInputScrollsComposerNotViewport(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.viewport.SetYOffset(7)
	m.textarea.SetValue(strings.Join([]string{
		"line0",
		"line1",
		"line2",
		"line3",
		"line4",
		"line5",
	}, "\n"))
	m.moveTextareaCursorToIndex(0)
	m.syncInputFromTextarea()

	startY, _, ok := m.inputAreaBounds()
	if !ok {
		t.Fatal("input area bounds unavailable")
	}
	beforeViewportOffset := m.viewport.YOffset()
	beforeCursor := m.textareaCursorIndex()

	updated, _ = m.handleMouse(tea.MouseWheelMsg(tea.Mouse{
		Button: tea.MouseWheelDown,
		X:      inputSelectionMouseX(m, 0),
		Y:      startY,
	}))
	m = updated.(*Model)

	if got := m.viewport.YOffset(); got != beforeViewportOffset {
		t.Fatalf("viewport offset = %d, want unchanged %d", got, beforeViewportOffset)
	}
	if got := m.textareaCursorIndex(); got <= beforeCursor {
		t.Fatalf("textarea cursor index = %d, want advanced beyond %d", got, beforeCursor)
	}
}

func TestMouseWheelInsideShortInputDoesNotScrollViewport(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.viewport.SetYOffset(5)
	m.textarea.SetValue("short")
	m.syncInputFromTextarea()

	startY, _, ok := m.inputAreaBounds()
	if !ok {
		t.Fatal("input area bounds unavailable")
	}
	beforeViewportOffset := m.viewport.YOffset()

	updated, _ = m.handleMouse(tea.MouseWheelMsg(tea.Mouse{
		Button: tea.MouseWheelDown,
		X:      inputSelectionMouseX(m, 0),
		Y:      startY,
	}))
	m = updated.(*Model)

	if got := m.viewport.YOffset(); got != beforeViewportOffset {
		t.Fatalf("viewport offset = %d, want unchanged %d", got, beforeViewportOffset)
	}
}

func inputSelectionMouseX(m *Model, contentCol int) int {
	return m.mainColumnX() + m.composerInputColumnOffset() + displayColumns(m.inputPromptPrefix()) + contentCol
}

func TestViewportSelectionAutoScrollUsesVisibleMouseYWhenFrameTopTrimmed(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.viewport.SetHeight(3)
	m.frameTopTrim = 1

	if got := m.selectionAutoScrollDelta(tea.Mouse{Y: 0}); got != -selectionScrollSlow {
		t.Fatalf("top visible row delta = %d, want slow upward scroll", got)
	}
	if got := m.selectionAutoScrollDelta(tea.Mouse{Y: 1}); got != selectionScrollSlow {
		t.Fatalf("bottom visible row delta = %d, want slow downward scroll", got)
	}
	if got := m.selectionAutoScrollDelta(tea.Mouse{Y: 2}); got != selectionScrollFast {
		t.Fatalf("below visible viewport delta = %d, want fast downward scroll", got)
	}
}

func TestViewportSelectionAutoScrollStopsAtDocumentBoundary(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(3)
	m.viewportStyledLines = []string{
		"line 0",
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
	}
	m.viewportPlainLines = append([]string(nil), m.viewportStyledLines...)
	m.viewport.SetContentLines(m.viewportStyledLines)
	m.viewport.SetYOffset(3)
	m.selecting = true
	m.selectionStart = textSelectionPoint{line: 3, col: 0}
	m.selectionEnd = textSelectionPoint{line: 5, col: 2}
	m.selectionAutoScroll = selectionAutoScrollState{
		active:        true,
		tickScheduled: true,
		mouse: tea.Mouse{
			Button: tea.MouseLeft,
			X:      m.mainColumnX() + tuikit.GutterNarrative + 2,
			Y:      2,
		},
	}

	updated, cmd := m.Update(frameTickMsg{kind: frameTickSelectionScroll, at: time.Now()})
	m = updated.(*Model)
	if got := m.viewport.YOffset(); got != 3 {
		t.Fatalf("viewport offset = %d, want max offset 3", got)
	}
	if m.selectionAutoScroll.active || m.selectionAutoScroll.tickScheduled {
		t.Fatalf("selection auto-scroll state = %#v, want inactive with no pending tick", m.selectionAutoScroll)
	}
	if cmd != nil {
		t.Fatal("boundary stop should not schedule another auto-scroll tick")
	}
}

func TestViewportSelectionAutoScrollPausesWhenMouseLeavesEdge(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(3)
	m.viewportStyledLines = []string{"line 0", "line 1", "line 2", "line 3"}
	m.viewportPlainLines = append([]string(nil), m.viewportStyledLines...)
	m.viewport.SetContentLines(m.viewportStyledLines)
	m.selecting = true
	m.selectionStart = textSelectionPoint{line: 0, col: 0}
	m.selectionEnd = m.selectionStart
	m.selectionAutoScroll = selectionAutoScrollState{active: true, tickScheduled: true, scheduledToken: 7, nextToken: 7}

	cmd := m.handleViewportMouseMotion(tea.Mouse{
		Button: tea.MouseLeft,
		X:      m.mainColumnX() + tuikit.GutterNarrative + 2,
		Y:      1,
	})
	if cmd != nil {
		t.Fatal("leaving the edge should not schedule selection auto-scroll")
	}
	if m.selectionAutoScroll.active {
		t.Fatalf("selection auto-scroll state = %#v, want inactive after leaving edge", m.selectionAutoScroll)
	}
	if !m.selectionAutoScroll.tickScheduled || m.selectionAutoScroll.scheduledToken != 7 {
		t.Fatalf("selection auto-scroll state = %#v, want pending tick preserved", m.selectionAutoScroll)
	}

	updated, cmd = m.Update(frameTickMsg{kind: frameTickSelectionScroll, token: 7, at: time.Now()})
	m = updated.(*Model)
	if cmd != nil {
		t.Fatal("paused auto-scroll tick should not schedule another tick")
	}
	if m.selectionAutoScroll.active || m.selectionAutoScroll.tickScheduled {
		t.Fatalf("selection auto-scroll state = %#v, want cleared after pending tick drains", m.selectionAutoScroll)
	}
}

func TestViewportSelectionAutoScrollDoesNotDuplicatePendingTickOnReentry(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(3)
	m.viewportStyledLines = []string{
		"line 0",
		"line 1",
		"line 2",
		"line 3",
		"line 4",
		"line 5",
	}
	m.viewportPlainLines = append([]string(nil), m.viewportStyledLines...)
	m.viewport.SetContentLines(m.viewportStyledLines)
	m.selecting = true
	m.selectionStart = textSelectionPoint{line: 0, col: 0}
	m.selectionEnd = m.selectionStart

	edgeMouse := tea.Mouse{
		Button: tea.MouseLeft,
		X:      m.mainColumnX() + tuikit.GutterNarrative + 2,
		Y:      2,
	}
	if cmd := m.handleViewportMouseMotion(edgeMouse); cmd == nil {
		t.Fatal("first edge entry should schedule selection auto-scroll")
	}
	token := m.selectionAutoScroll.scheduledToken
	if token == 0 || !m.selectionAutoScroll.tickScheduled {
		t.Fatalf("selection auto-scroll state = %#v, want scheduled token", m.selectionAutoScroll)
	}

	_ = m.handleViewportMouseMotion(tea.Mouse{
		Button: tea.MouseLeft,
		X:      m.mainColumnX() + tuikit.GutterNarrative + 2,
		Y:      1,
	})
	if m.selectionAutoScroll.active || !m.selectionAutoScroll.tickScheduled {
		t.Fatalf("selection auto-scroll state = %#v, want paused pending tick", m.selectionAutoScroll)
	}
	if cmd := m.handleViewportMouseMotion(edgeMouse); cmd != nil {
		t.Fatal("re-entering before the old tick drains should not schedule a duplicate tick")
	}
	if m.selectionAutoScroll.scheduledToken != token {
		t.Fatalf("scheduled token = %d, want original token %d", m.selectionAutoScroll.scheduledToken, token)
	}

	updated, nextCmd := m.Update(frameTickMsg{kind: frameTickSelectionScroll, token: token, at: time.Now()})
	m = updated.(*Model)
	if got := m.viewport.YOffset(); got != 1 {
		t.Fatalf("viewport offset = %d, want one scroll from the pending tick", got)
	}
	if nextCmd == nil {
		t.Fatal("active edge after pending tick should schedule the next tick")
	}
	nextToken := m.selectionAutoScroll.scheduledToken
	if nextToken == 0 || nextToken == token {
		t.Fatalf("next scheduled token = %d, want new token after %d", nextToken, token)
	}
	if got := m.viewport.YOffset(); got != 1 {
		t.Fatalf("viewport offset = %d, old tick should have scrolled only once", got)
	}
}

func TestViewportSelectionReleaseCancelsPendingAutoScroll(t *testing.T) {
	model := NewModel(Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(3)
	m.viewportStyledLines = []string{"line 0", "line 1", "line 2", "line 3"}
	m.viewportPlainLines = append([]string(nil), m.viewportStyledLines...)
	m.viewport.SetContentLines(m.viewportStyledLines)
	m.selecting = true
	m.selectionStart = textSelectionPoint{line: 0, col: 0}
	m.selectionEnd = textSelectionPoint{line: 1, col: 2}
	m.selectionAutoScroll = selectionAutoScrollState{active: true, tickScheduled: true, scheduledToken: 7, nextToken: 7}

	_ = m.handleViewportMouseRelease(tea.Mouse{
		Button: tea.MouseLeft,
		X:      m.mainColumnX() + tuikit.GutterNarrative + 2,
		Y:      1,
	})
	if m.selectionAutoScroll.active || m.selectionAutoScroll.tickScheduled {
		t.Fatalf("selection auto-scroll state = %#v, want cancelled on release", m.selectionAutoScroll)
	}
	if m.selectionAutoScroll.nextToken != 7 {
		t.Fatalf("next token = %d, want preserved token counter", m.selectionAutoScroll.nextToken)
	}
	updated, _ = m.Update(frameTickMsg{kind: frameTickSelectionScroll, token: 7, at: time.Now()})
	m = updated.(*Model)
	if got := m.viewport.YOffset(); got != 0 {
		t.Fatalf("viewport offset = %d, stale release tick should not scroll", got)
	}
}

func TestViewportWhitespaceSelectionDoesNotToggleFoldToken(t *testing.T) {
	model := NewModel(Config{
		WriteClipboardText: func(text string) error {
			if text != "  " {
				t.Errorf("clipboard text = %q, want two spaces", text)
			}
			return nil
		},
	})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m := updated.(*Model)
	m.viewport.SetWidth(40)
	m.viewport.SetHeight(10)

	block := NewParticipantTurnBlock("codex-001", "codex-001")
	block.UpdateToolWithMeta("ws-1", "lookup_weather", `"weather"`, strings.Join([]string{
		"result 01",
		"result 02",
		"result 03",
		"result 04",
		"result 05",
		"result 06",
	}, "\n"), true, false, ToolUpdateMeta{ToolKind: "other"})
	m.doc.Append(block)
	m.viewportStyledLines = []string{"   "}
	m.viewportPlainLines = []string{"   "}
	m.viewportBlockIDs = []string{block.BlockID()}
	m.viewportClickTokens = []string{acpToolPanelClickToken("ws-1")}
	m.selecting = true
	m.selectionStart = textSelectionPoint{line: 0, col: 0}
	m.selectionEnd = m.selectionStart

	cmd := m.handleViewportMouseRelease(tea.Mouse{
		Button: tea.MouseLeft,
		X:      m.mainColumnX() + tuikit.GutterNarrative + 2,
		Y:      0,
	})
	if cmd == nil {
		t.Fatal("whitespace selection should still produce a copy command")
	}
	if got, ok := cmd().(clipboardCopyResultMsg); !ok {
		t.Fatalf("copy command returned %T, want clipboardCopyResultMsg", got)
	} else if got.err != nil {
		t.Fatalf("copy command returned error: %v", got.err)
	}
	if block.toolPanelFullOutput("ws-1") {
		t.Fatal("drag selection over a clickable row must not toggle the fold state")
	}
}

func TestImagePasteWhileRunningShowsFeedback(t *testing.T) {
	model := NewModel(Config{
		PasteClipboardImage: func() ([]string, string, error) {
			t.Fatal("PasteClipboardImage must not run while model is running")
			return nil, "", nil
		},
	})
	model.liveTurn.Active = true

	updated, cmd := model.handleKey(tea.KeyPressMsg(tea.Key{Text: imagePasteKeysForPlatform(runtime.GOOS, isWSL())[0]}))
	m := updated.(*Model)
	if cmd == nil {
		t.Fatal("running image paste should schedule hint cleanup")
	}
	if !strings.Contains(m.hint, "image") && !strings.Contains(m.hint, "running") {
		t.Fatalf("model hint = %q, want image/running feedback", m.hint)
	}
}

func TestCtrlUInActivePromptNoOpWithoutUpdateOffered(t *testing.T) {
	model := NewModel(Config{
		OnUpdateRequested: func() {
			t.Fatal("OnUpdateRequested must not run without update offer")
		},
	})
	model.activePrompt = newPromptState(PromptRequestMsg{
		Prompt:   "Name",
		Response: make(chan PromptResponse, 1),
	})
	model.activePrompt.input = []rune("draft")
	model.activePrompt.cursor = len(model.activePrompt.input)

	updated, cmd := model.handleKey(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	m := updated.(*Model)
	if cmd != nil {
		t.Fatal("prompt Ctrl+U command != nil, want nil")
	}
	if m.quit {
		t.Fatal("model quit = true, want false")
	}
	if got := string(m.activePrompt.input); got != "draft" {
		t.Fatalf("active prompt input = %q, want draft preserved", got)
	}
}

func TestTerminalResponseFragmentsDoNotPolluteComposer(t *testing.T) {
	cases := []string{
		"0c",
		"0c0c",
		"?0c",
		"?1;2c",
		">0;95;0c",
		"?16u",
		"?2004;1$y",
		"11;rgb:0000/0000/0000",
	}
	for _, text := range cases {
		t.Run(text, func(t *testing.T) {
			model := NewModel(Config{})
			model.terminalResponseGuardUntil = time.Now().Add(time.Second)

			updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Text: text}))
			m := updated.(*Model)
			if got := m.textarea.Value(); got != "" {
				t.Fatalf("textarea value = %q, want terminal response fragment dropped", got)
			}
		})
	}
}

func TestSplitTerminalResponseFragmentsDoNotPolluteComposer(t *testing.T) {
	cases := []struct {
		name  string
		parts []string
	}{
		{name: "da", parts: []string{"0", "c"}},
		{name: "repeated-da", parts: []string{"0", "c", "0", "c"}},
		{name: "prefixed-da", parts: splitRunesForTest("?1;2c")},
		{name: "keyboard-report", parts: splitRunesForTest("?16u")},
		{name: "mode-report", parts: splitRunesForTest("?2004;1$y")},
		{name: "color-report", parts: splitRunesForTest("11;rgb:0000/0000/0000")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			model := NewModel(Config{})
			model.terminalResponseGuardUntil = time.Now().Add(time.Second)
			var updated tea.Model = model

			for _, part := range tc.parts {
				updated, _ = updated.(*Model).handleKey(tea.KeyPressMsg(tea.Key{Text: part}))
				if got := updated.(*Model).textarea.Value(); got != "" {
					t.Fatalf("textarea value after %q = %q, want terminal response fragments dropped", part, got)
				}
			}
		})
	}
}

func TestTerminalResponseGuardDoesNotBlockNormalInput(t *testing.T) {
	model := NewModel(Config{})
	model.terminalResponseGuardUntil = time.Now().Add(time.Second)

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Text: "hello"}))
	m := updated.(*Model)
	if got := m.textarea.Value(); got != "hello" {
		t.Fatalf("textarea value inside guard = %q, want hello", got)
	}

	model = NewModel(Config{})
	model.terminalResponseGuardUntil = time.Now().Add(-time.Second)
	updated, _ = model.handleKey(tea.KeyPressMsg(tea.Key{Text: "0c"}))
	m = updated.(*Model)
	if got := m.textarea.Value(); got != "0c" {
		t.Fatalf("textarea value after guard = %q, want 0c", got)
	}
}

func TestTerminalResponsePendingPrefixFallsBackToUserInput(t *testing.T) {
	model := NewModel(Config{})
	model.terminalResponseGuardUntil = time.Now().Add(time.Second)

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Text: "0"}))
	m := updated.(*Model)
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("textarea value while prefix pending = %q, want empty", got)
	}

	updated, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Text: "x"}))
	m = updated.(*Model)
	if got := m.textarea.Value(); got != "0x" {
		t.Fatalf("textarea value after prefix mismatch = %q, want 0x", got)
	}
}

func TestTerminalResponsePendingPrefixFlushesAsUserInput(t *testing.T) {
	model := NewModel(Config{})
	model.terminalResponseGuardUntil = time.Now().Add(time.Second)

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Text: "0"}))
	m := updated.(*Model)
	seq := m.terminalResponsePendingSeq
	updated, _ = m.Update(terminalResponsePendingFlushMsg{seq: seq})
	m = updated.(*Model)
	if got := m.textarea.Value(); got != "0" {
		t.Fatalf("textarea value after pending flush = %q, want 0", got)
	}
}

func splitRunesForTest(text string) []string {
	parts := make([]string, 0, len([]rune(text)))
	for _, r := range text {
		parts = append(parts, string(r))
	}
	return parts
}

func TestComposerUsesBarCursorToAvoidCoveringEditedText(t *testing.T) {
	model := NewModel(Config{})
	model.textarea.SetValue("edit middle")
	model.moveTextareaCursorToIndex(5)

	cursor := model.regularInputCursor()
	if cursor == nil {
		t.Fatal("regular input cursor = nil")
		return
	}
	if cursor.Shape != tea.CursorBar {
		t.Fatalf("cursor shape = %v, want bar cursor", cursor.Shape)
	}
}

func TestComposerMixedWidthMiddleInsertKeepsGlyphVisible(t *testing.T) {
	model := NewModel(Config{})
	model.textarea.SetValue("Y offset值")
	model.moveTextareaCursorToIndex(len([]rune("Y offset")))
	model.syncInputFromTextarea()

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Text: "差"}))
	m := updated.(*Model)

	if got := m.textarea.Value(); got != "Y offset差值" {
		t.Fatalf("textarea value = %q, want Y offset差值", got)
	}
	assertComposerRenderContains(t, m, "Y offset差值")
}

func TestComposerMixedWidthMiddleDeleteKeepsGlyphVisible(t *testing.T) {
	model := NewModel(Config{})
	model.textarea.SetValue("Y offset误差值")
	model.moveTextareaCursorToIndex(len([]rune("Y offset误")))
	model.syncInputFromTextarea()

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	m := updated.(*Model)

	if got := m.textarea.Value(); got != "Y offset差值" {
		t.Fatalf("textarea value = %q, want Y offset差值", got)
	}
	assertComposerRenderContains(t, m, "Y offset差值")
}

func TestComposerMixedWidthDeleteAsciiBeforeCJKKeepsNextGlyphVisible(t *testing.T) {
	model := NewModel(Config{})
	model.textarea.SetValue("甲a乙b丙c丁d")
	model.moveTextareaCursorToIndex(len([]rune("甲a乙b")))
	model.syncInputFromTextarea()

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	m := updated.(*Model)

	if got := m.textarea.Value(); got != "甲a乙丙c丁d" {
		t.Fatalf("textarea value = %q, want 甲a乙丙c丁d", got)
	}
	assertComposerRenderContains(t, m, "甲a乙丙c丁d")
}

func assertComposerRenderContains(t *testing.T, m *Model, want string) {
	t.Helper()
	render := m.composeInputRender()
	plain := strings.Join(render.plainLines, "\n")
	if !strings.Contains(plain, want) {
		t.Fatalf("plain composer render = %q, want to contain %q", plain, want)
	}
	styled := ansi.Strip(render.styledText())
	if !strings.Contains(styled, want) {
		t.Fatalf("styled composer render = %q, want to contain %q", styled, want)
	}
}

func TestWindowsCtrlVFallsBackToImageWhenTextClipboardEmpty(t *testing.T) {
	withClipboardPlatform(t, "windows")
	model := NewModel(Config{
		ReadClipboardText: func() (string, error) {
			return "", nil
		},
		PasteClipboardImage: func() ([]string, string, error) {
			return []string{"shot.png"}, "shot.png", nil
		},
	})
	model.keys = defaultKeyMapForPlatform("windows", false)

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	m := updated.(*Model)
	if runes := []rune(m.textarea.Value()); len(runes) != 1 || !isAttachmentSentinel(runes[0]) {
		t.Fatalf("textarea value = %q, want image sentinel", m.textarea.Value())
	}
	if len(m.inputAttachments) != 1 || m.inputAttachments[0].Name != "shot.png" {
		t.Fatalf("input attachments = %#v, want pasted image", m.inputAttachments)
	}
}

func TestWindowsCtrlVPrefersTextClipboardOverImageFallback(t *testing.T) {
	withClipboardPlatform(t, "windows")
	imageCalled := false
	model := NewModel(Config{
		ReadClipboardText: func() (string, error) {
			return "hello", nil
		},
		PasteClipboardImage: func() ([]string, string, error) {
			imageCalled = true
			return []string{"shot.png"}, "shot.png", nil
		},
	})
	model.keys = defaultKeyMapForPlatform("windows", false)

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	m := updated.(*Model)
	if imageCalled {
		t.Fatal("PasteClipboardImage should not run when text paste succeeds")
	}
	if got := m.textarea.Value(); got != "hello" {
		t.Fatalf("textarea value = %q, want text paste", got)
	}
	if len(m.inputAttachments) != 0 {
		t.Fatalf("input attachments = %#v, want none", m.inputAttachments)
	}
}

func TestWindowsCtrlVFallsBackToImageWhenTextClipboardErrors(t *testing.T) {
	withClipboardPlatform(t, "windows")
	model := NewModel(Config{
		ReadClipboardText: func() (string, error) {
			return "", errors.New("clipboard has no text")
		},
		PasteClipboardImage: func() ([]string, string, error) {
			return []string{"shot.png"}, "shot.png", nil
		},
	})
	model.keys = defaultKeyMapForPlatform("windows", false)

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	m := updated.(*Model)
	if m.hint != "" {
		t.Fatalf("model hint = %q, want no text paste error after image fallback", m.hint)
	}
	if len(m.inputAttachments) != 1 || m.inputAttachments[0].Name != "shot.png" {
		t.Fatalf("input attachments = %#v, want pasted image", m.inputAttachments)
	}
}

func TestWindowsCtrlVDoesNotFallbackToImageWhileRunning(t *testing.T) {
	withClipboardPlatform(t, "windows")
	imageCalled := false
	model := NewModel(Config{
		ReadClipboardText: func() (string, error) {
			return "", nil
		},
		PasteClipboardImage: func() ([]string, string, error) {
			imageCalled = true
			return []string{"shot.png"}, "shot.png", nil
		},
	})
	model.liveTurn.Active = true
	model.keys = defaultKeyMapForPlatform("windows", false)

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl}))
	m := updated.(*Model)
	if imageCalled {
		t.Fatal("PasteClipboardImage should not run while model is running")
	}
	if len(m.inputAttachments) != 0 {
		t.Fatalf("input attachments = %#v, want none", m.inputAttachments)
	}
}

func TestWindowsCtrlShiftVDoesNotUseImageFallback(t *testing.T) {
	withClipboardPlatform(t, "windows")
	imageCalled := false
	model := NewModel(Config{
		ReadClipboardText: func() (string, error) {
			return "", nil
		},
		PasteClipboardImage: func() ([]string, string, error) {
			imageCalled = true
			return []string{"shot.png"}, "shot.png", nil
		},
	})
	model.keys = defaultKeyMapForPlatform("windows", false)

	updated, _ := model.handleKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: tea.ModCtrl | tea.ModShift}))
	m := updated.(*Model)
	if imageCalled {
		t.Fatal("PasteClipboardImage should not run for Ctrl+Shift+V text paste")
	}
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("textarea value = %q, want empty", got)
	}
	if len(m.inputAttachments) != 0 {
		t.Fatalf("input attachments = %#v, want none", m.inputAttachments)
	}
}

func withClipboardPlatform(t *testing.T, goos string) {
	t.Helper()
	oldGOOS := clipboardGOOS
	clipboardGOOS = goos
	t.Cleanup(func() {
		clipboardGOOS = oldGOOS
	})
}

func TestModeToggleRunsWhileRunning(t *testing.T) {
	called := false
	model := NewModel(Config{
		ToggleMode: func() (string, error) {
			called = true
			return "mode updated", nil
		},
	})
	model.liveTurn.Active = true

	updated, cmd := model.handleKey(keyPress("shift+tab"))
	m := updated.(*Model)
	if !called {
		t.Fatal("ToggleMode was not called while running")
	}
	if cmd == nil {
		t.Fatal("mode toggle should schedule hint cleanup")
	}
	if !strings.Contains(m.hint, "mode updated") {
		t.Fatalf("model hint = %q, want mode update feedback", m.hint)
	}
}

func TestSubmitLineUsesActiveTurnModeOnlyWhileRunning(t *testing.T) {
	var submissions []Submission
	model := NewModel(Config{
		NoColor:     true,
		NoAnimation: true,
		ExecuteLine: func(submission Submission) TaskResultMsg {
			submissions = append(submissions, submission)
			return TaskResultMsg{SuppressTurnDivider: true}
		},
		CanSubmitRunningPrompt: func() bool { return true },
	})

	next, cmd := model.submitLine("new prompt")
	model = next.(*Model)
	if cmd == nil || !findAndRunTaskResult(cmd(), model) {
		t.Fatal("submitLine(new prompt) did not execute")
	}
	if got := submissions[0].Mode; got != SubmissionModeDefault {
		t.Fatalf("idle submission mode = %q, want default", got)
	}

	model.beginLiveTurn(SubmissionModeDefault, false, time.Unix(120, 0))
	next, cmd = model.submitLine("steer running turn")
	model = next.(*Model)
	if cmd == nil || !findAndRunTaskResult(cmd(), model) {
		t.Fatal("submitLine(steer running turn) did not execute")
	}
	if got := submissions[1].Mode; got != SubmissionModeActiveTurn {
		t.Fatalf("running submission mode = %q, want active_turn", got)
	}
}

func TestResolveSubmissionModes(t *testing.T) {
	t.Parallel()

	model := NewModel(Config{Commands: []string{"btw"}})
	cases := []struct {
		name          string
		line          string
		running       bool
		canSubmitNow  bool
		wantUI        SubmissionMode
		wantGateway   SubmissionMode
		wantDeferIdle bool
	}{
		{
			name:         "idle default starts new turn",
			line:         "new prompt",
			canSubmitNow: true,
			wantUI:       SubmissionModeDefault,
			wantGateway:  SubmissionModeDefault,
		},
		{
			name:         "running default steers active turn when accepted",
			line:         "guide running turn",
			running:      true,
			canSubmitNow: true,
			wantUI:       SubmissionModeDefault,
			wantGateway:  SubmissionModeActiveTurn,
		},
		{
			name:          "running default stays deferred when not accepted",
			line:          "queue after current turn",
			running:       true,
			canSubmitNow:  false,
			wantUI:        SubmissionModeDefault,
			wantGateway:   SubmissionModeDefault,
			wantDeferIdle: true,
		},
		{
			name:         "overlay keeps overlay mode while running",
			line:         "/btw side note",
			running:      true,
			canSubmitNow: true,
			wantUI:       SubmissionModeOverlay,
			wantGateway:  SubmissionModeOverlay,
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := resolveSubmissionModes(model.submissionModeForLine(tc.line), tc.running, tc.canSubmitNow)
			if got.uiMode != tc.wantUI || got.gatewayMode != tc.wantGateway || got.deferUntilIdle != tc.wantDeferIdle {
				t.Fatalf("resolveSubmission() = %#v, want ui=%q gateway=%q defer=%v", got, tc.wantUI, tc.wantGateway, tc.wantDeferIdle)
			}
		})
	}
}

func TestResolveSubmissionSkipsRunningPromptGateForOverlay(t *testing.T) {
	t.Parallel()

	called := false
	model := NewModel(Config{
		Commands: []string{"btw"},
		CanSubmitRunningPrompt: func() bool {
			called = true
			return false
		},
	})
	got := model.resolveSubmission("/btw side note", true)
	if called {
		t.Fatal("resolveSubmission() called running prompt gate for overlay submission")
	}
	if got.uiMode != SubmissionModeOverlay || got.gatewayMode != SubmissionModeOverlay || got.deferUntilIdle {
		t.Fatalf("resolveSubmission() = %#v, want overlay without defer", got)
	}
}
