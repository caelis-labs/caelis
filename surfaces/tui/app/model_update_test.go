package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func TestUpdateCheckResultShowsHintWhenIdle(t *testing.T) {
	model := NewModel(Config{})

	updated, _ := model.handleUpdateCheckResult(UpdateCheckResultMsg{
		LatestVersion: "v1.2.0",
		Eligible:      true,
	})
	m := updated.(*Model)
	if !m.updateOffered {
		t.Fatal("updateOffered = false, want true")
	}
	if !strings.Contains(m.hint, "v1.2.0") || !strings.Contains(m.hint, "Ctrl+U") {
		t.Fatalf("hint = %q, want update availability text", m.hint)
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
	model := NewModel(Config{})
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
		t.Fatalf("hint = %q, want update hint removed after submit", m.hint)
	}
}
