package tuiapp

import (
	"fmt"
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/colorprofile"
)

func TestCaelisToolHeaderKeepsRoleAndActionColorsSeparate(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	ctx := BlockRenderContext{Width: 100, TermWidth: 100, Theme: theme}

	styled := styleACPTranscriptHeader(ctx, "• Ran git status --short")
	if got := ansiTextForForeground(t, styled, theme.ToolFg); got != "•" {
		t.Fatalf("tool foreground text = %q, want icon only\nstyled=%q", got, styled)
	}
	if got := ansiTextForForeground(t, styled, theme.TextPrimary); !strings.Contains(got, "Ran") {
		t.Fatalf("primary foreground text = %q, want Ran action\nstyled=%q", got, styled)
	}
	if got := ansiTextForForeground(t, styled, theme.Focus); strings.Contains(got, "Ran") {
		t.Fatalf("focus foreground should not color ordinary action, got %q", got)
	}

	plan := styleACPTranscriptHeader(ctx, "• Updated Plan")
	if got := ansiTextForForeground(t, plan, theme.Accent); !strings.Contains(got, "Updated") {
		t.Fatalf("accent foreground text = %q, want Updated plan action\nstyled=%q", got, plan)
	}
	if got := ansiTextForForeground(t, plan, theme.Success); strings.Contains(got, "Updated") {
		t.Fatalf("success foreground should not describe a plan refresh, got %q", got)
	}

	wrote := styleACPTranscriptHeader(ctx, "• Wrote theme.go")
	if got := ansiTextForForeground(t, wrote, theme.Success); !strings.Contains(got, "Wrote") {
		t.Fatalf("success foreground text = %q, want Wrote action", got)
	}
	failed := styleACPTranscriptHeader(ctx, "• Failed theme.go")
	if got := ansiTextForForeground(t, failed, theme.Error); !strings.Contains(got, "Failed") {
		t.Fatalf("danger foreground text = %q, want Failed action", got)
	}
}

func TestCaelisPlanRowsUseStatusColorOnIcons(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	ctx := BlockRenderContext{Width: 100, TermWidth: 100, Theme: theme}
	rows := renderACPPlanRows("plan", SubagentEvent{
		PlanEntries: []planEntryState{
			{Content: "queued step", Status: "pending"},
			{Content: "completed step", Status: "completed"},
			{Content: "active step", Status: "in_progress"},
			{Content: "blocked step", Status: "blocked"},
			{Content: "failed step", Status: "failed"},
		},
	}, 100, ctx)
	styled := strings.Join(renderedRowStyles(rows), "\n")

	assertIconOnly := func(name string, foreground color.Color, icon string, body string) {
		t.Helper()
		got := ansiTextForForeground(t, styled, foreground)
		if !strings.Contains(got, icon) {
			t.Fatalf("%s foreground text = %q, want icon %q", name, got, icon)
		}
		if strings.Contains(got, body) {
			t.Fatalf("%s foreground text = %q, should not color body %q", name, got, body)
		}
	}
	assertIconOnly("success", theme.Success, "✔", "completed step")
	assertIconOnly("warning", theme.Warning, "⊘", "blocked step")
	assertIconOnly("danger", theme.Error, "✗", "failed step")

	focusText := ansiTextForForeground(t, styled, theme.Focus)
	if !strings.Contains(focusText, "◉") || strings.Contains(focusText, "active step") {
		t.Fatalf("focus foreground text = %q, want active icon only", focusText)
	}
	primaryText := ansiTextForForeground(t, styled, theme.TextPrimary)
	if !strings.Contains(primaryText, "active step") {
		t.Fatalf("primary foreground text = %q, want active body", primaryText)
	}
	secondaryText := ansiTextForForeground(t, styled, theme.TextSecondary)
	for _, body := range []string{"queued step", "completed step", "blocked step", "failed step"} {
		if !strings.Contains(secondaryText, body) {
			t.Fatalf("secondary foreground text = %q, want %q", secondaryText, body)
		}
	}
}

func TestPlanDrawerPreservesSurfaceSpecificTextPolicy(t *testing.T) {
	theme := tuikit.ResolveThemeWithState(true, false, colorprofile.TrueColor)
	model := &Model{theme: theme}

	completed := renderPlanLine(model, planEntryState{Content: "completed step", Status: "completed"})
	wantCompleted := theme.NoteStyle().Strikethrough(true).Render("completed step")
	if !strings.Contains(completed, wantCompleted) {
		t.Fatalf("completed drawer row = %q, want strikethrough body %q", completed, wantCompleted)
	}

	active := renderPlanLine(model, planEntryState{Content: "active step", Status: "in_progress"})
	wantActive := theme.Tokens().Focus.Bold(true).Render("active step")
	if !strings.Contains(active, wantActive) {
		t.Fatalf("active drawer row = %q, want focus body %q", active, wantActive)
	}
}

func TestCaelisSeparatesUserAndComposerSurfaces(t *testing.T) {
	theme := tuikit.ResolveThemeWithBackgroundColor(color.RGBA{A: 255}, false, colorprofile.TrueColor)
	if colorToAnsiPtr(theme.UserBg) == nil || colorToAnsiPtr(theme.ComposerBg) == nil {
		t.Fatal("expected true-color user and composer surfaces")
	}
	if *colorToAnsiPtr(theme.UserBg) == *colorToAnsiPtr(theme.ComposerBg) {
		t.Fatalf("user and composer surfaces should differ, both are %s", *colorToAnsiPtr(theme.UserBg))
	}

	rows := renderPlainUserRows("user", "hello", "▌ ", 40, theme)
	userSurfaceCode := sgrBackgroundCode(t, theme.UserBg)
	if len(rows) < 2 || !strings.Contains(rows[1].Styled, userSurfaceCode) {
		t.Fatalf("user row missing warm user surface code %q: %#v", userSurfaceCode, rows)
	}

	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	model.width = 80
	model.theme = theme
	model.setInputText("hello")
	model.syncTextareaFromInput()
	composer := model.renderInputBar()
	composerSurfaceCode := sgrBackgroundCode(t, theme.ComposerBg)
	if !strings.Contains(composer, composerSurfaceCode) {
		t.Fatalf("composer missing neutral surface code %q: %q", composerSurfaceCode, composer)
	}
	if strings.Contains(composer, userSurfaceCode) {
		t.Fatalf("composer reused user surface code %q: %q", userSurfaceCode, composer)
	}
}

func TestColorProfileRefreshRetainsSampledTerminalBackground(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CAELIS_THEME", "auto")
	background := color.RGBA{R: 0x20, G: 0x24, B: 0x2a, A: 0xff}
	model := NewModel(Config{ColorProfile: colorprofile.ANSI256})

	updated, _ := model.Update(tea.BackgroundColorMsg{Color: background})
	model = updated.(*Model)
	updated, _ = model.Update(tea.ColorProfileMsg{Profile: colorprofile.TrueColor})
	model = updated.(*Model)

	want := tuikit.ResolveThemeWithBackgroundColor(background, false, colorprofile.TrueColor)
	if got, expected := colorToAnsiPtr(model.theme.ComposerBg), colorToAnsiPtr(want.ComposerBg); got == nil || expected == nil || *got != *expected {
		t.Fatalf("composer surface after profile refresh = %v, want sampled-background result %v", got, expected)
	}
	if got, expected := colorToAnsiPtr(model.theme.UserBg), colorToAnsiPtr(want.UserBg); got == nil || expected == nil || *got != *expected {
		t.Fatalf("user surface after profile refresh = %v, want sampled-background result %v", got, expected)
	}
}

func ansiTextForForeground(t *testing.T, styled string, foreground color.Color) string {
	t.Helper()
	return normalizeInlineStyleText(textWithSGRForeground(styled, sgrForegroundCode(t, foreground)))
}

func sgrBackgroundCode(t *testing.T, background color.Color) string {
	t.Helper()
	if background == nil {
		t.Fatal("expected style to have a background color")
	}
	r, g, b, _ := background.RGBA()
	return fmt.Sprintf("48;2;%d;%d;%d", r>>8, g>>8, b>>8)
}

func renderedRowStyles(rows []RenderedRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Styled)
	}
	return out
}
