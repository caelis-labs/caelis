package tuiapp

import (
	"errors"
	"image/color"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

func TestUIPreferencesStartupTheme(t *testing.T) {
	for _, tc := range []struct {
		name, env, saved, selected string
		profile                    colorprofile.Profile
		noColor                    bool
	}{
		{name: "legacy", selected: "auto"},
		{name: "saved", saved: "nord", selected: "nord"},
		{name: "adaptive", saved: "catppuccin", selected: "catppuccin"},
		{name: "alias", saved: "mocha", selected: "catppuccin-mocha"},
		{name: "unknown saved", saved: "future-palette", selected: "auto"},
		{name: "environment", env: "latte", saved: "nord", selected: "catppuccin-latte"},
		{name: "explicit auto", env: "auto", saved: "nord", selected: "auto"},
		{name: "invalid environment", env: "invalid", saved: "nord", selected: "auto"},
		{name: "ANSI fallback", saved: "nord", selected: "nord", profile: colorprofile.ANSI},
		{name: "no color", saved: "nord", selected: "nord", noColor: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CAELIS_THEME", tc.env)
			profile := tc.profile
			if profile == colorprofile.Unknown {
				profile = colorprofile.TrueColor
			}
			client := &paneTestClient{preferences: uipreferences.Preferences{Theme: tc.saved, SubagentLayout: uipreferences.Right}}
			m := NewModel(Config{UIPreferences: client, ColorProfile: profile, NoColor: tc.noColor})
			m.Update(tea.BackgroundColorMsg{Color: color.White})
			runTeaCmds(t, m, m.loadUIPreferences())
			if m.themeName != tc.selected || m.uiPreferences.value.SubagentLayout != uipreferences.Right {
				t.Fatalf("startup selection = %q, preferences = %#v", m.themeName, m.uiPreferences.value)
			}
			want := tuikit.ResolveSelectedTheme(tc.selected, color.White, false, tc.noColor, profile)
			if m.theme.Name != want.Name || !reflect.DeepEqual(m.theme.AppBg, want.AppBg) {
				t.Fatalf("resolved theme = %s, want %s", m.theme.Name, want.Name)
			}
			if len(client.saves) != 0 || client.preferences.Theme != tc.saved {
				t.Fatal("startup resolution rewrote stored selection")
			}
		})
	}
}

func TestThemePreferencesConfirmationAndRestart(t *testing.T) {
	t.Setenv("CAELIS_THEME", "")
	client := &paneTestClient{preferences: (uipreferences.Preferences{SubagentLayout: uipreferences.Left, HorizontalRatio: 65}).WithDefaults()}
	calls := 0
	cfg := Config{UIPreferences: client, ColorProfile: colorprofile.TrueColor, NoAnimation: true,
		ExecuteLine: func(Submission) TaskResultMsg { calls++; return TaskResultMsg{} }}
	m := NewModel(cfg)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m.Update(tea.BackgroundColorMsg{Color: color.Black})
	runTeaCmds(t, m, m.loadUIPreferences())
	m.beginLiveTurn(SubmissionModeDefault, false, time.Now())
	doc, history := m.doc.Len(), len(m.history)
	_, cmd := m.submitInteractiveLine("/theme mocha", "/theme mocha", nil)
	runTeaCmds(t, m, cmd)
	if calls != 0 || m.doc.Len() != doc || len(m.history) != history || !m.turnRunning() {
		t.Fatal("theme preference entered Agent execution or Session history")
	}
	if !reflect.DeepEqual(client.saves, []uipreferences.Preferences{{Theme: "catppuccin-mocha"}}) {
		t.Fatalf("command saved noncanonical or unrelated fields: %#v", client.saves)
	}
	// Confirm an adaptive choice before its delayed preview is delivered.
	m.submitThemeCommand("/theme")
	m.handleKey(keyPress("up"))
	m.handleKey(keyPress("up"))
	_, cmd = m.handleKey(keyPress("enter"))
	runTeaCmds(t, m, cmd)
	if m.themeName != "catppuccin" || client.preferences.Theme != "catppuccin" || client.preferences.HorizontalRatio != 65 {
		t.Fatalf("confirmation = %q, saved = %#v", m.themeName, client.preferences)
	}

	reopened := NewModel(cfg)
	reopened.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	reopened.Update(tea.BackgroundColorMsg{Color: color.White})
	before := reopened.View().Content
	runTeaCmds(t, reopened, reopened.loadUIPreferences())
	if reopened.themeName != "catppuccin" || reopened.theme.Name != "catppuccin-latte" || reopened.uiPreferences.value != client.preferences {
		t.Fatal("restart lost selected name, terminal adaptation, or layout")
	}
	after := reopened.View().Content
	updates := renderFullscreenFramesForTest(t, 100, 30, before, after)
	assertPhysicalFullscreenFrame(t, 100, 30, after, updates)
	screen := vt.NewSafeEmulator(100, 30)
	defer screen.Close()
	for _, update := range updates {
		if _, err := screen.Write([]byte(update)); err != nil {
			t.Fatal(err)
		}
	}
	wantBg := colorprofile.ANSI256.Convert(reopened.theme.AppBg)
	if cell := screen.CellAt(0, 0); cell == nil || !colorInSet(cell.Style.Bg, []color.Color{wantBg}) {
		t.Fatal("loaded theme did not repaint the physical frame")
	}
}

func TestThemePreferencesPreviewNeverSaves(t *testing.T) {
	t.Setenv("CAELIS_THEME", "")
	client := &paneTestClient{preferences: uipreferences.Preferences{Theme: "dracula"}}
	m := NewModel(Config{UIPreferences: client, ColorProfile: colorprofile.TrueColor})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	runTeaCmds(t, m, m.loadUIPreferences())
	layoutSave := m.setSubagentLayout(uipreferences.Right)
	m.submitThemeCommand("/theme")
	_, textCmd := m.Update(tea.KeyPressMsg{Text: "nord"})
	runTeaCmds(t, m, textCmd)
	if m.themePicker == nil {
		t.Fatal("committed text confirmed a theme without Enter")
	}
	nord := themePickerNamedPoint(t, m, "Nord")
	m.Update(tea.MouseMotionMsg(nord))
	m.Update(tea.MouseClickMsg(nord))
	m.Update(tea.MouseReleaseMsg(nord))
	settleThemePreview(t, m)
	if m.themeName != "nord" || m.uiPreferences.value.Theme != "dracula" {
		t.Fatal("mouse preview changed confirmed preferences")
	}
	runTeaCmds(t, m, layoutSave)
	if !reflect.DeepEqual(client.saves, []uipreferences.Preferences{{SubagentLayout: uipreferences.Right}}) {
		t.Fatalf("layout save serialized a theme preview: %#v", client.saves)
	}
	_, cmd := m.handleKey(keyPress("esc"))
	runTeaCmds(t, m, cmd)
	if m.themeName != "dracula" {
		t.Fatal("cancel did not restore selection")
	}
	m.submitThemeCommand("/theme")
	m.handleKey(keyPress("down"))
	settleThemePreview(t, m)
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.ANSI})
	m.Update(tea.BackgroundColorMsg{Color: color.White})
	m.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	if len(client.saves) != 1 || client.preferences.Theme != "dracula" || m.themeName != "dracula" || m.theme.Name != "dracula" {
		t.Fatal("capability fallback saved or lost the selected theme")
	}
	m.submitThemeCommand("/theme")
	m.handleKey(keyPress("down"))
	settleThemePreview(t, m)
	_, cmd = m.handleKey(keyPress("enter"))
	runTeaCmds(t, m, cmd)
	if len(client.saves) != 2 || client.saves[1] != (uipreferences.Preferences{Theme: "nord"}) {
		t.Fatal("confirming an already rendered preview did not save the selected name")
	}
}

func TestUIPreferencesSerializeOnlyConfirmedFields(t *testing.T) {
	t.Setenv("CAELIS_THEME", "")
	client := &paneTestClient{}
	m := NewModel(Config{UIPreferences: client, ColorProfile: colorprofile.TrueColor})
	runTeaCmds(t, m, m.loadUIPreferences())
	first := m.submitThemeCommand("/theme nord")
	if cmd := m.setSubagentLayout(uipreferences.Left); cmd != nil {
		t.Fatal("layout save raced theme save")
	}
	m.submitThemeCommand("/theme dracula")
	m.submitThemeCommand("/theme catppuccin")
	// Another TUI changes a field this instance has not edited.
	client.preferences.VerticalRatio = 65
	runTeaCmds(t, m, first)
	want := []uipreferences.Preferences{{Theme: "nord"}, {Theme: "catppuccin", SubagentLayout: uipreferences.Left}}
	if !reflect.DeepEqual(client.saves, want) || client.preferences.VerticalRatio != 65 {
		t.Fatalf("sparse save order = %#v, final = %#v", client.saves, client.preferences)
	}
	m.workspace.resizeRatio = 60
	runTeaCmds(t, m, m.applyPaneResize())
	if last := client.saves[len(client.saves)-1]; last != (uipreferences.Preferences{HorizontalRatio: 60}) {
		t.Fatalf("resize sent unrelated fields: %#v", last)
	}
}

func TestUIPreferencesLateLoadPreservesConfirmedFields(t *testing.T) {
	t.Setenv("CAELIS_THEME", "")
	for _, themeFirst := range []bool{false, true} {
		client := &paneTestClient{preferences: uipreferences.Preferences{Theme: "nord", SubagentLayout: uipreferences.Right, HorizontalRatio: 60}}
		m := NewModel(Config{UIPreferences: client, ColorProfile: colorprofile.TrueColor})
		loaded := m.loadUIPreferences()()
		if themeFirst {
			runTeaCmds(t, m, m.submitThemeCommand("/theme dracula"))
		} else {
			runTeaCmds(t, m, m.setSubagentLayout(uipreferences.Left))
		}
		m.Update(loaded)
		wantTheme, wantLayout := "nord", uipreferences.Left
		if themeFirst {
			wantTheme, wantLayout = "dracula", uipreferences.Right
		}
		if m.themeName != wantTheme || m.uiPreferences.value.SubagentLayout != wantLayout || m.uiPreferences.value.HorizontalRatio != 60 {
			t.Fatalf("late load clobbered confirmed fields or omitted untouched defaults: %#v", m.uiPreferences.value)
		}
	}
}

func TestThemePreferencesLateLoadDuringPicker(t *testing.T) {
	t.Setenv("CAELIS_THEME", "")
	for _, preview := range []bool{false, true} {
		client := &paneTestClient{preferences: uipreferences.Preferences{Theme: "nord"}}
		m := NewModel(Config{UIPreferences: client, ColorProfile: colorprofile.TrueColor})
		loaded := m.loadUIPreferences()()
		m.submitThemeCommand("/theme")
		if preview {
			m.handleKey(keyPress("down"))
			settleThemePreview(t, m)
		}
		m.Update(loaded)
		if preview && m.themeName != "catppuccin" {
			t.Fatal("late load displaced the active preview")
		}
		if !preview && (m.themeName != "nord" || m.activePrompt.choices[m.activePrompt.choiceIndex].value != "nord") {
			t.Fatal("untouched picker did not select loaded default")
		}
		m.handleKey(keyPress("esc"))
		if m.themeName != "nord" || len(client.saves) != 0 {
			t.Fatal("cancel failed to restore loaded default without saving")
		}
	}
}

func TestUIPreferencesFailuresAreVisibleWithoutRetry(t *testing.T) {
	t.Setenv("CAELIS_THEME", "")
	client := &paneTestClient{loadErr: errors.New("offline"), saveErr: errors.New("read-only store")}
	m := NewModel(Config{UIPreferences: client, ColorProfile: colorprofile.TrueColor, NoAnimation: true})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	runTeaCmds(t, m, m.loadUIPreferences())
	if !strings.Contains(ansi.Strip(m.View().Content), "Could not load UI preferences: offline") {
		t.Fatal("load failure is not rendered")
	}
	runTeaCmds(t, m, m.submitThemeCommand("/theme nord"))
	frame := m.View().Content
	if !strings.Contains(ansi.Strip(frame), "Could not save UI preferences: read-only store") || m.themeName != "nord" || len(client.saves) != 1 {
		t.Fatal("save failure lost local choice, retried, or was not rendered")
	}
	assertPhysicalFullscreenFrame(t, 120, 30, frame, renderFullscreenFramesForTest(t, 120, 30, frame))
	client.saveErr = nil
	runTeaCmds(t, m, m.submitThemeCommand("/theme nord"))
	if len(client.saves) != 2 || client.preferences.Theme != "nord" {
		t.Fatal("explicitly confirming the same theme did not retry the failed save")
	}
}
