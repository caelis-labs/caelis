package tuiapp

import (
	"image/color"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestThemeCommandStaysLocalDuringRunningTurn(t *testing.T) {
	t.Setenv("CAELIS_THEME", "auto")
	calls := 0
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor, Commands: []string{"help"}, ExecuteLine: func(Submission) TaskResultMsg { calls++; return TaskResultMsg{} }})
	other := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 35})
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	beforeHistory, beforeDoc := len(m.history), m.doc.Len()
	m.setInputText("/th")
	m.syncTextareaFromInput()
	m.refreshSlashCommands()
	if !slices.Contains(m.slashCandidates, "/theme") {
		t.Fatal("local theme missing during running turn")
	}
	for i, name := range m.slashCandidates {
		if name == "/theme" {
			m.slashIndex = i
		}
	}
	_, cmd := m.handleKey(keyPress("enter"))
	if cmd != nil {
		t.Fatal("theme completion scheduled a Control command")
	}
	if m.themePicker == nil {
		t.Fatal("theme picker did not open")
	}
	m.handleKey(keyPress("down"))
	settleThemePreview(t, m)
	if m.theme.Name != "catppuccin-latte" {
		t.Fatalf("light preview = %s", m.theme.Name)
	}
	m.handleKey(keyPress("esc"))
	if m.themeName != "auto" || m.theme.IsDark || m.activePrompt != nil {
		t.Fatal("cancel did not restore light terminal theme")
	}
	m.submitInteractiveLine("/theme nord", "/theme nord", nil)
	if m.theme.Name != "nord" || m.theme.AppBg == nil {
		t.Fatal("named dark theme must paint its background on a light terminal")
	}
	m.Update(tea.BackgroundColorMsg{Color: color.Black})
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI256})
	if m.theme.Name != "nord" {
		t.Fatal("terminal reports replaced the instance selection")
	}
	m.submitInteractiveLine("/theme unknown", "/theme unknown", nil)
	if m.themeName != "nord" {
		t.Fatal("invalid theme reset the selection")
	}
	if calls != 0 || !m.turnRunning() || len(m.history) != beforeHistory || m.doc.Len() != beforeDoc {
		t.Fatal("local theme command changed the turn or history")
	}
	if other.themeName != "auto" || os.Getenv("CAELIS_THEME") != "auto" {
		t.Fatal("theme leaked to another instance or environment")
	}
}

func TestThemePickerConfirmationAndProfileDowngrade(t *testing.T) {
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.submitThemeCommand("/theme")
	m.handleKey(keyPress("down"))
	m.handleKey(keyPress("enter"))
	if m.themeName != "catppuccin" || m.activePrompt != nil {
		t.Fatal("confirmation did not retain preview")
	}
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	if m.theme.Name != "catppuccin-latte" {
		t.Fatal("auto flavour did not follow light terminal")
	}
	m.submitThemeCommand("/theme")
	m.handleKey(keyPress("down"))
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI})
	if m.themePicker != nil || m.activePrompt != nil || m.theme.AppBg != nil {
		t.Fatal("profile downgrade retained an unsupported preview")
	}
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	if m.theme.IsDark {
		t.Fatal("ANSI fallback lost sampled light background")
	}
}

func TestThemeSwitchPaintsWholePhysicalFrame(t *testing.T) {
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	terminal := vt.NewSafeEmulator(80, 24)
	defer terminal.Close()
	for _, name := range []string{"nord", "catppuccin-latte", "dracula"} {
		m.submitThemeCommand("/theme " + name)
		frame := m.View().Content
		if _, err := terminal.Write([]byte(renderFullscreenFramesForTest(t, 80, 24, frame)[0])); err != nil {
			t.Fatal(err)
		}
		want := colorprofile.ANSI256.Convert(m.theme.AppBg)
		for _, point := range [][2]int{{0, 0}, {79, 0}, {0, 12}, {79, 23}} {
			cell := terminal.CellAt(point[0], point[1])
			if cell == nil || cell.Style.Bg == nil {
				t.Fatalf("%s left transparent cell at %v", name, point)
			}
			wr, wg, wb, _ := want.RGBA()
			r, g, b, _ := cell.Style.Bg.RGBA()
			if r != wr || g != wg || b != wb {
				t.Fatalf("%s corner background mismatch at %v", name, point)
			}
		}
		m.submitThemeCommand("/theme")
		text := ansi.Strip(m.renderPromptModal())
		if !strings.Contains(text, "Esc restore") {
			t.Fatal("theme rollback instruction missing")
		}
		m.handleKey(keyPress("esc"))
	}
}

func TestThemePickerNamesInWideAndCompactViews(t *testing.T) {
	for _, width := range []int{48, 120} {
		m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		m.Update(tea.BackgroundColorMsg{Color: color.White})
		m.submitThemeCommand("/theme nord")
		m.submitThemeCommand("/theme")
		plain := ansi.Strip(m.renderPromptModal())
		if strings.Count(plain, "↑/↓") != 1 || !strings.Contains(plain, "Esc restore") {
			t.Fatalf("theme picker must explain preview/apply/restore once:\n%s", plain)
		}
		for _, prefix := range []string{"Auto ·", "Dark ·", "Light ·", "Fixed dark background", "Dark palette", "Light palette", "terminal:"} {
			if strings.Contains(plain, prefix) {
				t.Fatalf("width %d: picker obscures theme names with %q:\n%s", width, prefix, plain)
			}
		}
		last := -1
		for _, label := range []string{"Terminal", "Catppuccin", "Catppuccin Latte", "Catppuccin Mocha", "Dracula", "Nord"} {
			index := strings.Index(plain, label)
			if index <= last {
				t.Fatalf("width %d: missing or out-of-order theme name %q:\n%s", width, label, plain)
			}
			last = index
		}
	}
}

func TestThemePickerSurvivesSessionReconnectWithoutAnsweringPrompts(t *testing.T) {
	m := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.submitThemeCommand("/theme nord")
	m.submitThemeCommand("/theme")
	m.handleKey(keyPress("up"))
	response := make(chan PromptResponse, 1)
	m.enqueuePrompt(PromptRequestMsg{Title: "Old approval", Response: response})
	m.applySessionReconnectState(appserver.SessionState{SessionID: "new-session"})
	settleThemePreview(t, m)
	if m.themePicker == nil || m.activePrompt != m.themePicker.prompt || m.themeName != "dracula" || len(m.pendingPrompt) != 0 {
		t.Fatal("reconnect lost the local preview or retained a Session prompt")
	}
	m.handleKey(keyPress("esc"))
	if m.themeName != "nord" || m.activePrompt != nil || len(response) != 0 {
		t.Fatal("cancel failed to restore the theme or answered an old Session prompt")
	}
	m.submitThemeCommand("/theme")
	m.enqueuePrompt(PromptRequestMsg{Title: "New approval", Response: response})
	m.handleKey(keyPress("enter"))
	if m.themePicker != nil || m.activePrompt == nil || m.activePrompt.title != "New approval" || len(response) != 0 {
		t.Fatal("theme confirmation consumed or answered a queued approval")
	}
}
