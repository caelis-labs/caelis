package tuiapp

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestRunningTickerUsesStaticHintWhenAnimationIsThrottled(t *testing.T) {
	m := NewModel(Config{NoColor: true, NoAnimation: true})
	m.liveTurn.Active = true

	if !m.shouldRenderStaticRunningHint() {
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

func TestSpinnerTickReschedulesWhenRunningAnimationIsThrottled(t *testing.T) {
	m := NewModel(Config{})
	m.liveTurn.Active = true
	m.selecting = true
	m.spinnerTickScheduled = true

	updated, cmd := m.Update(m.spinner.Tick())
	next := updated.(*Model)
	if cmd == nil {
		t.Fatal("throttled spinner tick should keep scheduling future ticks")
	}
	if !next.spinnerTickScheduled {
		t.Fatal("spinnerTickScheduled = false, want true after throttled tick")
	}
}
