package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func TestUpdateCheckResultShowsWelcomeNoticeWhenIdle(t *testing.T) {
	model := newWelcomeTestModel(t, 80, 24, Config{})

	updated, cmd := model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	m := updated.(*Model)
	if cmd != nil {
		t.Fatal("update notice command != nil, want synchronous Welcome refresh")
	}
	if !m.updateOffered {
		t.Fatal("updateOffered = false, want true")
	}
	if m.hint != "" {
		t.Fatalf("composer hint = %q, want update notice confined to Welcome", m.hint)
	}
	plain := strings.Join(m.viewportPlainLines, "\n")
	if want := "v1.2.0 available, press ctrl+u to update"; !strings.Contains(plain, want) {
		t.Fatalf("welcome notice missing %q\n%s", want, plain)
	}
}

func TestUpdateCheckResultStylesRenderedWelcomeCards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		width  int
		height int
	}{
		{name: "standard", width: 80, height: 24},
		{name: "compact", width: 35, height: 16},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := newWelcomeTestModel(t, tc.width, tc.height, Config{})
			model.theme = tuikit.ResolveThemeFromOptions(false, colorprofile.TrueColor)
			_, _ = model.handleUpdateCheckResult(UpdateCheckResultMsg{
				LatestVersion: "v1.2.0",
				Eligible:      true,
			})

			var warningParts []string
			var mutedParts []string
			for i, styled := range model.viewportStyledLines {
				if text := ansiTextForForeground(t, styled, model.theme.Warning); text != "" {
					warningParts = append(warningParts, text)
				}
				if text := ansiTextForForeground(t, styled, model.theme.MutedText); text != "" {
					mutedParts = append(mutedParts, text)
				}
				if got := displayColumns(ansi.Strip(styled)); got > tc.width {
					t.Fatalf("styled row %d width = %d, want <= %d: %q", i, got, tc.width, ansi.Strip(styled))
				}
			}
			if got := strings.Join(warningParts, " "); got != "v1.2.0 available" {
				t.Fatalf("rendered warning emphasis = %q", got)
			}
			if got := strings.Join(mutedParts, " "); got != ", press ctrl+u to update" {
				t.Fatalf("rendered muted detail = %q", got)
			}

			for i, plain := range model.viewportPlainLines {
				content := strings.TrimSpace(strings.Trim(strings.TrimSpace(plain), "│"))
				if !strings.Contains(plain, "v1.2.0") && !strings.Contains(plain, "ctrl+u") && content != "update" {
					continue
				}
				if strings.Count(plain, "│") != 2 {
					t.Fatalf("announcement row %d escaped the card frame: %q", i, plain)
				}
			}
		})
	}
}

func TestUpdateCheckResultFallsBackAfterWelcomeDismissal(t *testing.T) {
	model := newWelcomeTestModel(t, 80, 24, Config{})
	model.dismissWelcomeCard()

	updated, cmd := model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	m := updated.(*Model)
	if cmd != nil {
		t.Fatal("persistent update hint command != nil, want no expiry timer")
	}
	if want := "v1.2.0 available, press ctrl+u to update"; !m.updateOffered || m.hint != want {
		t.Fatalf("update after Welcome dismissal = (offered:%v hint:%q), want %q", m.updateOffered, m.hint, want)
	}
}

func TestUpdateNoticeKeepsVersionAndShortcutInSmallLayout(t *testing.T) {
	model := newWelcomeTestModel(t, 35, 16, Config{})

	_, _ = model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	plain := strings.Join(model.viewportPlainLines, "\n")
	for _, part := range []string{"v1.2.0", "available", "press", "ctrl+u", "to", "update"} {
		if !strings.Contains(plain, part) {
			t.Fatalf("small Welcome lost update copy %q\n%s", part, plain)
		}
	}
	for i, line := range model.viewportPlainLines {
		if got := displayColumns(line); got > 35 {
			t.Fatalf("small Welcome row %d width = %d, want <= 35: %q", i, got, line)
		}
	}
}

func TestUpdateNoticeRemainsVisibleInNoticeOnlyHeightFallback(t *testing.T) {
	model := newWelcomeTestModel(t, 80, 24, Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 80, Height: 14})
	model = updated.(*Model)

	_, _ = model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	plain := strings.Join(model.viewportPlainLines, "\n")
	if want := "v1.2.0 available, press ctrl+u to update"; !model.updateOffered || !strings.Contains(plain, want) {
		t.Fatalf("notice-only Welcome hid update copy %q\n%s", want, plain)
	}
}

func TestUpdateCheckResultFallsBackWhenWelcomeCannotRenderNotice(t *testing.T) {
	model := newWelcomeTestModel(t, 35, 16, Config{})
	updated, _ := model.Update(tea.WindowSizeMsg{Width: 35, Height: 7})
	model = updated.(*Model)

	updated, cmd := model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	model = updated.(*Model)
	if cmd != nil {
		t.Fatal("persistent update hint command != nil, want no expiry timer")
	}
	if want := "v1.2.0 available, press ctrl+u to update"; !model.updateOffered || model.hint != want {
		t.Fatalf("small-layout update = (offered:%v hint:%q), want %q", model.updateOffered, model.hint, want)
	}
}

func TestUpdateCheckResultFallsBackWhenWelcomeIsTooNarrowForFullNotice(t *testing.T) {
	model := newWelcomeTestModel(t, 30, 16, Config{})

	updated, cmd := model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	model = updated.(*Model)
	if cmd != nil {
		t.Fatal("persistent update hint command != nil, want no expiry timer")
	}
	if want := "v1.2.0 available, press ctrl+u to update"; !model.updateOffered || model.hint != want {
		t.Fatalf("narrow-layout update = (offered:%v hint:%q), want %q", model.updateOffered, model.hint, want)
	}
	if plain := strings.Join(model.viewportPlainLines, "\n"); strings.Contains(plain, "v1.2.0") {
		t.Fatalf("narrow Welcome rendered a truncated update instead of using the composer hint\n%s", plain)
	}
}

func TestUpdateNoticeTemporarilyOverridesConfiguredAnnouncement(t *testing.T) {
	const announcement = "Explore the new workspace flow."
	model := newWelcomeTestModel(t, 80, 24, Config{WelcomeNotice: announcement})

	_, _ = model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	if plain := strings.Join(model.viewportPlainLines, "\n"); !strings.Contains(plain, "ctrl+u") || strings.Contains(plain, announcement) {
		t.Fatalf("update availability did not replace configured announcement\n%s", plain)
	}

	model.revokeUpdateOffer()
	if plain := strings.Join(model.viewportPlainLines, "\n"); !strings.Contains(plain, "Explore the new") ||
		!strings.Contains(plain, "workspace flow.") ||
		strings.Contains(plain, "ctrl+u") {
		t.Fatalf("restored Welcome notice is incorrect\n%s", plain)
	}
}

func TestUpdateCheckResultSkippedWhileTurnRunning(t *testing.T) {
	model := NewModel(Config{})
	model.liveTurn.Active = true

	updated, cmd := model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	m := updated.(*Model)
	if cmd != nil {
		t.Fatal("running update check command != nil, want nil")
	}
	if m.updateOffered {
		t.Fatal("updateOffered = true, want false while turn is running")
	}
}

func TestCtrlUNoOpWithoutUpdateOffered(t *testing.T) {
	requested := false
	model := NewModel(Config{
		OnUpdateRequested: func() {
			requested = true
		},
	})

	updated, cmd := model.handleKey(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	m := updated.(*Model)
	if requested {
		t.Fatal("OnUpdateRequested should not run without update offer")
	}
	if m.quit {
		t.Fatal("model quit = true, want false")
	}
	if cmd != nil {
		t.Fatal("Ctrl+U command != nil, want nil without update offer")
	}
}

func TestCtrlURequestsUpdateAndQuitsWhenOffered(t *testing.T) {
	requested := false
	model := NewModel(Config{
		OnUpdateRequested: func() {
			requested = true
		},
	})
	model.updateOffered = true

	updated, cmd := model.handleKey(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	m := updated.(*Model)
	if !requested {
		t.Fatal("OnUpdateRequested was not called")
	}
	if !m.quit {
		t.Fatal("model quit = false, want true")
	}
	if cmd == nil {
		t.Fatal("Ctrl+U should return quit command")
	}
	if m.updateOffered {
		t.Fatal("updateOffered = true, want revoked after Ctrl+U")
	}
}

func TestCtrlUInActivePromptUpdatesWhenOffered(t *testing.T) {
	requested := false
	model := NewModel(Config{
		OnUpdateRequested: func() {
			requested = true
		},
	})
	model.updateOffered = true
	model.activePrompt = newPromptState(PromptRequestMsg{
		Prompt:   "Name",
		Response: make(chan PromptResponse, 1),
	})
	model.activePrompt.input = []rune("draft")

	updated, cmd := model.handleKey(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	m := updated.(*Model)
	if !requested {
		t.Fatal("OnUpdateRequested was not called from active prompt")
	}
	if !m.quit {
		t.Fatal("model quit = false, want true")
	}
	if cmd == nil {
		t.Fatal("Ctrl+U should return quit command")
	}
}

func TestSubmitRevokesUpdateOffer(t *testing.T) {
	model := newWelcomeTestModel(t, 80, 24, Config{})
	_, _ = model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	if !model.updateOffered {
		t.Fatal("updateOffered = false, want true before submit")
	}

	updated, _ := model.submitLine("hello")
	m := updated.(*Model)
	if m.updateOffered {
		t.Fatal("updateOffered = true, want false after submit")
	}
	if strings.Contains(m.hint, "ctrl+u") {
		t.Fatalf("post-submit composer hint = %q, want update notice revoked", m.hint)
	}
}
