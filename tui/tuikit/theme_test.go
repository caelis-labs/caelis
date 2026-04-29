package tuikit

import (
	"fmt"
	"image/color"
	"testing"

	"github.com/charmbracelet/colorprofile"
	xansi "github.com/charmbracelet/x/ansi"
)

func TestComposeFooter(t *testing.T) {
	got := ComposeFooter(20, "left", "right")
	if len(got) != 20 {
		t.Fatalf("expected width 20, got %d", len(got))
	}
	if got[:4] != "left" {
		t.Fatalf("expected left prefix, got %q", got)
	}
}

func TestResolveThemeFromEnv_UsesNamedThemeAndAccentOverride(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CAELIS_THEME", "nord")
	t.Setenv("CAELIS_ACCENT", "#ff9900")
	t.Setenv("COLORTERM", "truecolor")

	theme := ResolveThemeFromEnv()
	if got := stringifyColor(theme.AppBg); got != "#2e3440" {
		t.Fatalf("expected nord app bg, got %q", got)
	}
	if got := stringifyColor(theme.Accent); got != "#ff9900" {
		t.Fatalf("expected accent override, got %q", got)
	}
	if got := stringifyColor(theme.ComposerBorderFocus); got != "#ff9900" {
		t.Fatalf("expected composer focus override, got %q", got)
	}
}

func TestResolveThemeFromEnv_FallsBackTo256Palette(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CAELIS_THEME", "dracula")
	t.Setenv("COLORTERM", "")
	t.Setenv("TERM", "xterm-256color")

	theme := ResolveThemeFromEnv()
	if got := stringifyColor(theme.AppBg); got != "236" {
		t.Fatalf("expected 256-color fallback app bg, got %q", got)
	}
	if got := stringifyColor(theme.Focus); got != "123" {
		t.Fatalf("expected 256-color fallback focus, got %q", got)
	}
}

func TestResolveThemeForBackground_SelectsLightTheme(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CAELIS_THEME", "")
	t.Setenv("COLORTERM", "truecolor")

	theme := ResolveThemeForBackground(false)
	if theme.IsDark {
		t.Fatal("expected light theme for light terminal background")
	}
	if got := stringifyColor(theme.TextPrimary); got != "#1f2937" {
		t.Fatalf("expected readable light-theme text, got %q", got)
	}
	if got := stringifyColor(theme.PanelBorder); got != "#c8d2df" {
		t.Fatalf("expected light-theme border, got %q", got)
	}
}

func TestThemeUsesAutoBackground(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CAELIS_THEME", "")
	if !ThemeUsesAutoBackground() {
		t.Fatal("expected empty theme to use auto background detection")
	}

	t.Setenv("CAELIS_THEME", "auto")
	if !ThemeUsesAutoBackground() {
		t.Fatal("expected auto theme to use background detection")
	}

	t.Setenv("CAELIS_THEME", "light")
	if ThemeUsesAutoBackground() {
		t.Fatal("expected explicit light theme to disable auto background detection")
	}
}

func TestResolveThemeFromOptions_NoColor(t *testing.T) {
	theme := ResolveThemeFromOptions(true, 0)
	if !theme.NoColor {
		t.Fatal("expected explicit no-color option to be preserved on theme")
	}
	if theme.TextPrimary != nil || theme.Accent != nil || theme.StatusBg != nil {
		t.Fatalf("expected no-color theme to strip palette, got primary=%v accent=%v status=%v", theme.TextPrimary, theme.Accent, theme.StatusBg)
	}
}

func TestDefaultThemeSoftensTranscriptSupportingColors(t *testing.T) {
	theme := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	if got := stringifyColor(theme.ReasoningFg); got != "#7f8ba3" {
		t.Fatalf("expected softer reasoning color, got %q", got)
	}
	if got := stringifyColor(theme.ToolFg); got != "#8bd5ff" {
		t.Fatalf("expected quieter tool meta color, got %q", got)
	}
	if got := stringifyColor(theme.TranscriptRail); got != "#465268" {
		t.Fatalf("expected quieter transcript rail, got %q", got)
	}
	if got := stringifyColor(theme.SeparatorFg); got != "#293241" {
		t.Fatalf("expected subtler separator, got %q", got)
	}
}

func TestDefaultLightDarkPalettesExposeModernSemanticColors(t *testing.T) {
	dark := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	if got := stringifyColor(dark.AppBg); got != "#0f1117" {
		t.Fatalf("dark app bg = %q", got)
	}
	if got := stringifyColor(dark.Focus); got != "#8bd5ff" {
		t.Fatalf("dark focus = %q", got)
	}
	if got := stringifyColor(dark.CodeBlockBg); got != "#171c26" {
		t.Fatalf("dark code block bg = %q", got)
	}

	light := ResolveThemeWithState(false, false, colorprofile.TrueColor)
	if got := stringifyColor(light.AppBg); got != "#fbfcfe" {
		t.Fatalf("light app bg = %q", got)
	}
	if got := stringifyColor(light.ToolFg); got != "#0f766e" {
		t.Fatalf("light tool fg = %q", got)
	}
	if got := stringifyColor(light.CodeBg); got != "#fff7ed" {
		t.Fatalf("light inline code bg = %q", got)
	}
}

func TestTokensIncludeToolAndMarkdownSemantics(t *testing.T) {
	theme := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	tokens := theme.Tokens()
	if got := stringifyColor(tokens.ToolName.GetForeground()); got != "#8bd5ff" {
		t.Fatalf("tool name token foreground = %q", got)
	}
	if got := stringifyColor(tokens.MarkdownInlineCode.GetBackground()); got != "#20283a" {
		t.Fatalf("inline code token background = %q", got)
	}
	if got := stringifyColor(tokens.MarkdownTableEdge.GetForeground()); got != "#59657a" {
		t.Fatalf("table edge token foreground = %q", got)
	}
}

func stringifyColor(value interface{}) string {
	switch c := value.(type) {
	case xansi.BasicColor:
		return fmt.Sprintf("%d", c)
	case xansi.IndexedColor:
		return fmt.Sprintf("%d", c)
	case color.RGBA:
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	case color.NRGBA:
		return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	return fmt.Sprint(value)
}
