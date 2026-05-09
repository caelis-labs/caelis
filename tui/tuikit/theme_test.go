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
	if theme.TextPrimary != nil {
		t.Fatalf("expected light theme body text to inherit terminal foreground, got %v", theme.TextPrimary)
	}
	if got := stringifyColor(theme.Focus); got != "#0077aa" {
		t.Fatalf("expected light-theme focus accent, got %q", got)
	}
	if got := stringifyColor(theme.PanelBorder); got != "#c9d1dc" {
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

func TestAdaptiveDefaultThemeUsesTerminalNativeBodyAndSemanticAccents(t *testing.T) {
	theme := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	if theme.TextPrimary != nil {
		t.Fatalf("expected default text to inherit terminal foreground, got %v", theme.TextPrimary)
	}
	if theme.AssistantFg != nil {
		t.Fatalf("expected assistant text to inherit terminal foreground, got %v", theme.AssistantFg)
	}
	if got := stringifyColor(theme.ReasoningFg); got != "#7f8ba3" {
		t.Fatalf("expected reasoning text to use low-contrast theme color, got %q", got)
	}
	if got := stringifyColor(theme.ToolFg); got != "#22d3ee" {
		t.Fatalf("expected tool meta to use cyan focus color, got %q", got)
	}
	if got := stringifyColor(theme.Accent); got != "#d78bff" {
		t.Fatalf("expected agent/model accent color, got %q", got)
	}
	if got := stringifyColor(theme.TranscriptRail); got != "#3b4352" {
		t.Fatalf("expected subtle transcript rail, got %q", got)
	}
}

func TestDefaultLightDarkPalettesExposeModernSemanticColors(t *testing.T) {
	dark := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	if dark.AppBg != nil {
		t.Fatalf("dark app bg = %v", dark.AppBg)
	}
	if got := stringifyColor(dark.Focus); got != "#22d3ee" {
		t.Fatalf("dark focus = %q", got)
	}
	if got := stringifyColor(dark.CodeBlockBg); got != "#141820" {
		t.Fatalf("dark code block bg = %q", got)
	}

	light := ResolveThemeWithState(false, false, colorprofile.TrueColor)
	if light.AppBg != nil {
		t.Fatalf("light app bg = %v", light.AppBg)
	}
	if got := stringifyColor(light.ToolFg); got != "#0077aa" {
		t.Fatalf("light tool fg = %q", got)
	}
	if got := stringifyColor(light.CodeBg); got != "#eef2f7" {
		t.Fatalf("light inline code bg = %q", got)
	}
}

func TestTokensIncludeToolAndMarkdownSemantics(t *testing.T) {
	theme := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	tokens := theme.Tokens()
	if got := stringifyColor(tokens.ToolName.GetForeground()); got != "#22d3ee" {
		t.Fatalf("tool name token foreground = %q", got)
	}
	if got := stringifyColor(tokens.MarkdownInlineCode.GetBackground()); got != "#1d2430" {
		t.Fatalf("inline code token background = %q", got)
	}
	if got := stringifyColor(tokens.MarkdownTableEdge.GetForeground()); got != "#596579" {
		t.Fatalf("table edge token foreground = %q", got)
	}
	if !tokens.TextSecondary.GetFaint() {
		t.Fatal("expected terminal-native secondary text token to use faint style")
	}
}

func TestNamedThemesUseMutedReasoningText(t *testing.T) {
	for _, name := range []string{"nord", "solarized", "dracula"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("CAELIS_THEME", name)
			theme := ResolveThemeFromOptions(false, colorprofile.TrueColor)
			if got, want := stringifyColor(theme.ReasoningFg), stringifyColor(theme.MutedText); got != want {
				t.Fatalf("reasoning fg = %q, want muted text %q", got, want)
			}
		})
	}
}

func TestValidateThemeAcceptsSupportedPalettes(t *testing.T) {
	for _, name := range []string{"dark", "light", "nord", "solarized", "dracula"} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("CAELIS_THEME", name)
			theme := ResolveThemeFromOptions(false, colorprofile.TrueColor)
			if issues := ValidateTheme(theme); len(issues) != 0 {
				t.Fatalf("ValidateTheme(%s) issues = %#v", name, issues)
			}
		})
	}
}

func TestValidateThemeSkipsNoColorPalette(t *testing.T) {
	theme := ResolveThemeFromOptions(true, colorprofile.NoTTY)
	if issues := ValidateTheme(theme); len(issues) != 0 {
		t.Fatalf("ValidateTheme(no-color) issues = %#v, want none", issues)
	}
}

func TestResolveThemeWithBackgroundColorBlendsAdaptiveSurfaces(t *testing.T) {
	dark := ResolveThemeWithBackgroundColor(color.RGBA{A: 255}, false, colorprofile.TrueColor)
	if !dark.IsDark {
		t.Fatal("expected black terminal background to select dark theme")
	}
	if got := stringifyColor(dark.ModalBg); got != "#141414" {
		t.Fatalf("dark modal bg = %q", got)
	}
	if got := stringifyColor(dark.UserBg); got != "#1f1f1f" {
		t.Fatalf("dark user bg = %q", got)
	}

	light := ResolveThemeWithBackgroundColor(color.RGBA{R: 255, G: 255, B: 255, A: 255}, false, colorprofile.TrueColor)
	if light.IsDark {
		t.Fatal("expected white terminal background to select light theme")
	}
	if got := stringifyColor(light.UserBg); got != "#f6f6f6" {
		t.Fatalf("light user bg = %q", got)
	}
	if got := stringifyColor(light.CodeBg); got != "#f1f1f1" {
		t.Fatalf("light code bg = %q", got)
	}
}

func TestAdaptiveDefaultThemeDisablesRichBackgroundsForANSI(t *testing.T) {
	theme := ResolveThemeWithState(true, false, colorprofile.ANSI)
	if theme.DiffAddBg != nil || theme.DiffRemoveBg != nil || theme.CodeBg != nil || theme.CommandActive != nil {
		t.Fatalf("expected ANSI theme to avoid rich backgrounds, got add=%v del=%v code=%v selection=%v", theme.DiffAddBg, theme.DiffRemoveBg, theme.CodeBg, theme.CommandActive)
	}
	if got := stringifyColor(theme.Focus); got != "6" {
		t.Fatalf("expected ANSI focus cyan, got %q", got)
	}
	if got := stringifyColor(theme.Success); got != "2" {
		t.Fatalf("expected ANSI success green, got %q", got)
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
