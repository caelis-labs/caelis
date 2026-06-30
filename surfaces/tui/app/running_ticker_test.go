package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestRunningTickerUsesStaticCarouselWhenViewportSyncIsDeferred(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true

	if !m.shouldUseStaticRunningCarousel() {
		t.Fatal("running hint should switch to static rendering when animation is disabled")
	}

	_ = m.buildRunningHintText()
	if got := m.diag.RunningTickerStaticRenders; got == 0 {
		t.Fatal("static running ticker counter was not incremented")
	}
	if got := m.diag.RunningTickerAnimatedRenders; got != 0 {
		t.Fatalf("animated running ticker renders = %d, want 0", got)
	}
}

func TestRunningTickerStyleCacheInvalidatesOnThemeChange(t *testing.T) {
	m := NewModel(Config{NoColor: true})

	styles := m.runningTickerStyleSet()
	if len(styles) == 0 {
		t.Fatal("running ticker style cache was empty")
	}
	firstKey := m.runningTickerThemeKey
	if firstKey == "" {
		t.Fatal("running ticker style cache key was empty")
	}

	_ = m.runningTickerStyleSet()
	if got := m.diag.RunningTickerStyleCacheMisses; got != 1 {
		t.Fatalf("style cache misses = %d, want 1 before theme change", got)
	}

	next := m.theme
	next.Name += "-alternate"
	m.applyTheme(next)
	_ = m.runningTickerStyleSet()

	if m.runningTickerThemeKey == firstKey {
		t.Fatal("running ticker style cache key did not change after theme change")
	}
	if got := m.diag.RunningTickerStyleCacheMisses; got != 2 {
		t.Fatalf("style cache misses = %d, want 2 after theme change", got)
	}
}

func TestRunningHintPlainRowDoesNotExposeANSI(t *testing.T) {
	m := NewModel(Config{})
	m.liveTurn.Active = true

	plain := m.hintRowText()
	if plain != ansi.Strip(plain) || strings.Contains(plain, "[38;") {
		t.Fatalf("hintRowText() = %q, want plain clipboard-safe text", plain)
	}
}

func TestSpinnerTickReschedulesWhenCarouselIsStatic(t *testing.T) {
	m := NewModel(Config{})
	m.liveTurn.Active = true
	m.selecting = true
	m.spinnerTickScheduled = true

	updated, cmd := m.Update(m.spinner.Tick())
	next := updated.(*Model)
	if cmd == nil {
		t.Fatal("static-carousel spinner tick should keep scheduling future ticks")
	}
	if !next.spinnerTickScheduled {
		t.Fatal("spinnerTickScheduled = false, want true after throttled tick")
	}
}

func TestRunningTickerContinuesWhenViewportPinned(t *testing.T) {
	m := NewModel(Config{Workspace: "/tmp/storage"})
	m.liveTurn.Active = true
	m.viewportFollowState = viewportPinnedHistory
	m.spinnerTickScheduled = true

	before := m.windowTitle()
	updated, cmd := m.Update(m.spinner.Tick())
	next := updated.(*Model)
	after := next.windowTitle()

	if cmd == nil {
		t.Fatal("pinned viewport running tick should schedule the next tick")
	}
	if !next.spinnerTickScheduled {
		t.Fatal("spinnerTickScheduled = false, want true after pinned viewport tick")
	}
	if before == after {
		t.Fatalf("windowTitle did not advance while viewport was pinned: %q", before)
	}
	if !strings.Contains(after, "storage") {
		t.Fatalf("windowTitle() = %q, want workspace title", after)
	}
}

func TestResumeRunningAnimationIgnoresViewportPin(t *testing.T) {
	m := NewModel(Config{})
	m.liveTurn.Active = true
	m.viewportFollowState = viewportPinnedHistory

	if cmd := m.resumeRunningAnimationIfNeeded(); cmd == nil {
		t.Fatal("resumeRunningAnimationIfNeeded() = nil, want tick command while viewport is pinned")
	}
}

func TestInterruptReplacesApprovalReviewActivity(t *testing.T) {
	cancelled := false
	m := NewModel(Config{
		CancelRunning: func() bool {
			cancelled = true
			return true
		},
	})
	m.liveTurn.Active = true
	m.runningActivity = runningActivityState{
		Kind:   runningActivityApprovalReview,
		Detail: "Reviewing approval request: command: go test ./...",
	}

	updated, cmd := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	next := updated.(*Model)
	if cmd == nil {
		t.Fatal("interrupt should return a cancel command")
	}
	if cancelled {
		t.Fatal("cancel command should not run synchronously")
	}
	if next.runningActivity.Kind != runningActivityInterrupting {
		t.Fatalf("runningActivity = %#v, want interrupting", next.runningActivity)
	}
	if strings.Contains(next.buildHintText(), "go test") {
		t.Fatalf("hint = %q, want stale approval text cleared", next.buildHintText())
	}
}
