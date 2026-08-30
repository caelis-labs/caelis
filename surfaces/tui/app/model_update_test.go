package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
	if !strings.Contains(plain, "v1.2.0") || !strings.Contains(plain, "Ctrl+U") {
		t.Fatalf("welcome notice missing update availability text\n%s", plain)
	}
}

func TestUpdateCheckResultRequiresVisibleWelcomeCard(t *testing.T) {
	model := NewModel(Config{})

	updated, cmd := model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	m := updated.(*Model)
	if cmd != nil || m.updateOffered {
		t.Fatalf("update without Welcome = (cmd:%v offered:%v)", cmd != nil, m.updateOffered)
	}
}

func TestUpdateNoticeKeepsVersionAndShortcutInSmallLayout(t *testing.T) {
	model := newWelcomeTestModel(t, 35, 16, Config{})

	_, _ = model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	plain := strings.Join(model.viewportPlainLines, "\n")
	if !strings.Contains(plain, "v1.2.0") || !strings.Contains(plain, "Ctrl+U") {
		t.Fatalf("small Welcome lost update version or shortcut\n%s", plain)
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
	if !model.updateOffered || !strings.Contains(plain, "v1.2.0") || !strings.Contains(plain, "Ctrl+U") {
		t.Fatalf("notice-only Welcome hid the update offer\n%s", plain)
	}
}

func TestUpdateNoticeTemporarilyOverridesConfiguredAnnouncement(t *testing.T) {
	const announcement = "Explore the new workspace flow."
	model := newWelcomeTestModel(t, 80, 24, Config{WelcomeNotice: announcement})

	_, _ = model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	if plain := strings.Join(model.viewportPlainLines, "\n"); !strings.Contains(plain, "Ctrl+U") || strings.Contains(plain, announcement) {
		t.Fatalf("update availability did not replace configured announcement\n%s", plain)
	}

	model.revokeUpdateOffer()
	if plain := strings.Join(model.viewportPlainLines, "\n"); !strings.Contains(plain, "Explore the new") ||
		!strings.Contains(plain, "workspace flow.") ||
		strings.Contains(plain, "Ctrl+U") {
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
	if strings.Contains(m.hint, "Ctrl+U") {
		t.Fatalf("post-submit composer hint = %q, want update notice revoked", m.hint)
	}
}
