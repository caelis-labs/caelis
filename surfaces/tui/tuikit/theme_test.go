package tuikit

import (
	"fmt"
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
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
	if got := stringifyColor(theme.TextPrimary); got != "#4c4f69" {
		t.Fatalf("expected light theme body text to use explicit high-contrast foreground, got %q", got)
	}
	if got := stringifyColor(theme.Focus); got != "#7287fd" {
		t.Fatalf("expected light-theme focus accent, got %q", got)
	}
	if got := stringifyColor(theme.PanelBorder); got != "#ccd0da" {
		t.Fatalf("expected light-theme border, got %q", got)
	}
}

func TestResolveThemeFromOptionsUsesCOLORFGBGForAutoBackground(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CAELIS_THEME", "auto")
	t.Setenv("COLORTERM", "truecolor")
	t.Setenv("COLORFGBG", "0;15")

	light := ResolveThemeFromOptions(false, colorprofile.TrueColor)
	if light.IsDark {
		t.Fatal("expected COLORFGBG white background to select light theme")
	}

	t.Setenv("COLORFGBG", "15;0")
	dark := ResolveThemeFromOptions(false, colorprofile.TrueColor)
	if !dark.IsDark {
		t.Fatal("expected COLORFGBG black background to select dark theme")
	}
}

func TestTerminalColorIndexIsDarkCoversANSI256(t *testing.T) {
	tests := []struct {
		name  string
		index int
		want  bool
	}{
		{name: "black", index: 0, want: true},
		{name: "white", index: 15, want: false},
		{name: "dark gray ramp", index: 235, want: true},
		{name: "light gray ramp", index: 255, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := terminalColorIndexIsDark(tt.index)
			if !ok {
				t.Fatalf("terminalColorIndexIsDark(%d) ok = false", tt.index)
			}
			if got != tt.want {
				t.Fatalf("terminalColorIndexIsDark(%d) = %v, want %v", tt.index, got, tt.want)
			}
		})
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

func TestAdaptiveDefaultThemeUsesExplicitBodyAndSemanticAccents(t *testing.T) {
	theme := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	if got := stringifyColor(theme.TextPrimary); got != "#cdd6f4" {
		t.Fatalf("expected default text to use explicit graphite foreground, got %q", got)
	}
	if got := stringifyColor(theme.AssistantFg); got != "#cdd6f4" {
		t.Fatalf("expected assistant text to match body foreground, got %q", got)
	}
	if got := stringifyColor(theme.ReasoningFg); got != "#7f849c" {
		t.Fatalf("expected reasoning text to use low-contrast theme color, got %q", got)
	}
	if got := stringifyColor(theme.ToolFg); got != "#89dceb" {
		t.Fatalf("expected tool meta to use cyan focus color, got %q", got)
	}
	if got := stringifyColor(theme.Accent); got != "#cba6f7" {
		t.Fatalf("expected agent/model accent color, got %q", got)
	}
	if got := stringifyColor(theme.TranscriptRail); got != "#313244" {
		t.Fatalf("expected subtle transcript rail, got %q", got)
	}
}

func TestDefaultLightDarkPalettesExposeModernSemanticColors(t *testing.T) {
	dark := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	if dark.AppBg != nil {
		t.Fatalf("dark app bg = %v", dark.AppBg)
	}
	if got := stringifyColor(dark.Focus); got != "#b4befe" {
		t.Fatalf("dark focus = %q", got)
	}
	if got := stringifyColor(dark.CodeBlockFg); got != "#cdd6f4" {
		t.Fatalf("dark code block fg = %q", got)
	}
	if got := stringifyColor(dark.CodeBlockBg); got != "#1e1e2e" {
		t.Fatalf("dark code block bg = %q", got)
	}
	if got := stringifyColor(dark.CodeBg); got != "#181825" {
		t.Fatalf("dark inline code bg = %q", got)
	}

	light := ResolveThemeWithState(false, false, colorprofile.TrueColor)
	if light.AppBg != nil {
		t.Fatalf("light app bg = %v", light.AppBg)
	}
	if got := stringifyColor(light.ToolFg); got != "#04a5e5" {
		t.Fatalf("light tool fg = %q", got)
	}
	if got := stringifyColor(light.CodeBlockFg); got != "#4c4f69" {
		t.Fatalf("light code block fg = %q", got)
	}
	if got := stringifyColor(light.CodeBlockBg); got != "#eff1f5" {
		t.Fatalf("light code block bg = %q", got)
	}
	if got := stringifyColor(light.CodeBg); got != "#eff1f5" {
		t.Fatalf("light inline code bg = %q", got)
	}
}

func TestTokensIncludeToolAndMarkdownSemantics(t *testing.T) {
	theme := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	tokens := theme.Tokens()
	if got := stringifyColor(tokens.ToolName.GetForeground()); got != "#b4befe" {
		t.Fatalf("tool name token foreground = %q", got)
	}
	if got, want := stringifyColor(tokens.MarkdownHeading.GetForeground()), stringifyColor(theme.TextPrimary); got != want {
		t.Fatalf("markdown heading token foreground = %q, want body text %q", got, want)
	}
	if got := tokens.MarkdownHeading.GetBackground(); colorIsPresent(got) {
		t.Fatalf("markdown heading token background = %v, want none", got)
	}
	if got := stringifyColor(tokens.MarkdownInlineCode.GetForeground()); got != "#b4befe" {
		t.Fatalf("inline code token foreground = %q", got)
	}
	if got := tokens.MarkdownInlineCode.GetBackground(); colorIsPresent(got) {
		t.Fatalf("inline code token background = %v, want none", got)
	}
	if got, want := stringifyColor(tokens.MarkdownTableHead.GetForeground()), stringifyColor(theme.TextPrimary); got != want {
		t.Fatalf("markdown table header token foreground = %q, want body text %q", got, want)
	}
	if got := tokens.MarkdownTableHead.GetBackground(); colorIsPresent(got) {
		t.Fatalf("markdown table header token background = %v, want none", got)
	}
	if got := stringifyColor(tokens.MarkdownTableEdge.GetForeground()); got != "#313244" {
		t.Fatalf("table edge token foreground = %q", got)
	}
	if got := stringifyColor(tokens.TextSecondary.GetForeground()); got != "#a6adc8" {
		t.Fatalf("secondary text token foreground = %q", got)
	}
}

func TestInlineCodeUsesForegroundOnlyAcrossColorProfiles(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256, colorprofile.ANSI} {
		t.Run(fmt.Sprint(profile), func(t *testing.T) {
			theme := ResolveThemeWithState(true, false, profile)
			if got := theme.MarkdownInlineCodeStyle().GetBackground(); colorIsPresent(got) {
				t.Fatalf("inline code background = %v, want nil", got)
			}
			if got := theme.MarkdownInlineCodeStyle().GetForeground(); !colorIsPresent(got) {
				t.Fatal("inline code should keep a foreground color")
			}
		})
	}
}

func TestSelectionStyleUsesExplicitPaletteWhenAvailable(t *testing.T) {
	theme := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	style := theme.SelectionStyle()
	if got := stringifyColor(style.GetForeground()); got != "#cdd6f4" {
		t.Fatalf("selection foreground = %q", got)
	}
	if got := stringifyColor(style.GetBackground()); got != "#585b70" {
		t.Fatalf("selection background = %q", got)
	}

	ansi := ResolveThemeWithState(true, false, colorprofile.ANSI)
	if !ansi.SelectionStyle().GetReverse() {
		t.Fatal("expected ANSI selection style to fall back to reverse video")
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
	if got := stringifyColor(dark.UserBg); got != "#141414" {
		t.Fatalf("dark user bg = %q", got)
	}

	light := ResolveThemeWithBackgroundColor(color.RGBA{R: 255, G: 255, B: 255, A: 255}, false, colorprofile.TrueColor)
	if light.IsDark {
		t.Fatal("expected white terminal background to select light theme")
	}
	if got := stringifyColor(light.UserBg); got != "#f6f6f6" {
		t.Fatalf("light user bg = %q", got)
	}
	if got := stringifyColor(light.CodeBg); got != "#eff1f5" {
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

func colorIsPresent(value color.Color) bool {
	if value == nil {
		return false
	}
	if _, ok := value.(lipgloss.NoColor); ok {
		return false
	}
	return true
}
