package tuiapp

import (
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
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
	model.running = true

	updated, cmd := model.handleKey(tea.KeyPressMsg(tea.Key{Text: imagePasteKeysForPlatform(runtime.GOOS, isWSL())[0]}))
	m := updated.(*Model)
	if cmd == nil {
		t.Fatal("running image paste should schedule hint cleanup")
	}
	if !strings.Contains(m.hint, "image") && !strings.Contains(m.hint, "running") {
		t.Fatalf("model hint = %q, want image/running feedback", m.hint)
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
	if got := m.textarea.Value(); got != "" {
		t.Fatalf("textarea value = %q, want empty image-only paste", got)
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
	model.running = true
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
	model.running = true

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
