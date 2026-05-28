package tuiapp

import (
	"runtime"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
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
