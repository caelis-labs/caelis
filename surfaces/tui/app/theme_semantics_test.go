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

	for _, verb := range []string{"Updated", "Edit"} {
		styledVerb := styleACPTranscriptHeader(ctx, "• "+verb+" theme.go")
		if got := ansiTextForForeground(t, styledVerb, theme.TextPrimary); !strings.Contains(got, verb) {
			t.Fatalf("primary foreground text = %q, want %s action\nstyled=%q", got, verb, styledVerb)
		}
		if got := ansiTextForForeground(t, styledVerb, theme.Accent); strings.Contains(got, verb) {
			t.Fatalf("accent foreground should not color ordinary action %s, got %q", verb, got)
		}
		if got := ansiTextForForeground(t, styledVerb, theme.Success); strings.Contains(got, verb) {
			t.Fatalf("success foreground should not color ordinary action %s, got %q", verb, got)
		}
	}

	edit := styleACPTranscriptHeader(ctx, "• Edit process_alive.go +21 -0")
	if got := ansiTextForForeground(t, edit, theme.DiffAddFg); !strings.Contains(got, "+21") {
		t.Fatalf("diff-add foreground text = %q, want +21\nstyled=%q", got, edit)
	}
	if got := ansiTextForForeground(t, edit, theme.DiffRemoveFg); !strings.Contains(got, "-0") {
		t.Fatalf("diff-remove foreground text = %q, want -0\nstyled=%q", got, edit)
	}
	if got := ansiTextForForeground(t, edit, theme.Success); strings.Contains(got, "Edit") {
		t.Fatalf("success foreground should not color Edit action, got %q", got)
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
		t.Fatalf("user and composer tokens should differ, both are %s", *colorToAnsiPtr(theme.UserBg))
	}

	rows := renderPlainUserRows("user", "hello", userNarrativePrefix, 40, theme)
	composerSurfaceCode := sgrBackgroundCode(t, theme.ComposerBg)
	var content RenderedRow
	for _, row := range rows {
		if strings.Contains(row.Plain, "hello") {
			content = row
			break
		}
	}
	if content.Plain == "" {
		t.Fatalf("user rows missing content: %#v", rows)
	}
	if !strings.Contains(content.Plain, "> hello") {
		t.Fatalf("user plain = %q, want composer prompt", content.Plain)
	}
	if strings.Contains(content.Plain, "▌") {
		t.Fatalf("user plain = %q, want no chat-bubble rail", content.Plain)
	}
	if !strings.Contains(content.Styled, composerSurfaceCode) {
		t.Fatalf("user row missing composer surface code %q: %#v", composerSurfaceCode, content)
	}
	userSurfaceCode := sgrBackgroundCode(t, theme.UserBg)
	if userSurfaceCode != composerSurfaceCode && strings.Contains(content.Styled, userSurfaceCode) {
		t.Fatalf("user row reused warm user surface code %q: %#v", userSurfaceCode, content)
	}

	model := NewModel(Config{ColorProfile: colorprofile.TrueColor})
	model.width = 80
	model.theme = theme
	model.setInputText("hello")
	model.syncTextareaFromInput()
	composer := model.renderInputBar()
	if !strings.Contains(composer, composerSurfaceCode) {
		t.Fatalf("composer missing neutral surface code %q: %q", composerSurfaceCode, composer)
	}
	if userSurfaceCode != composerSurfaceCode && strings.Contains(composer, userSurfaceCode) {
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
