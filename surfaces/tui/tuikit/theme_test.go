package tuikit

import (
	"fmt"
	"image/color"
	"slices"
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
	if got := stringifyColor(theme.PromptFg); got != "#ff9900" {
		t.Fatalf("expected prompt accent override, got %q", got)
	}
	if got := stringifyColor(theme.SpinnerFg); got != "#ff9900" {
		t.Fatalf("expected spinner accent override, got %q", got)
	}
	if got := stringifyColor(theme.InputSelectionBg); got != "#ff9900" {
		t.Fatalf("expected input selection accent override, got %q", got)
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
	if got := stringifyColor(theme.TextPrimary); got != "#242a35" {
		t.Fatalf("expected light theme body text to use explicit high-contrast foreground, got %q", got)
	}
	if got := stringifyColor(theme.Focus); got != "#315fbb" {
		t.Fatalf("expected light-theme focus accent, got %q", got)
	}
	if got := stringifyColor(theme.PanelBorder); got != "#cbd1dc" {
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

	t.Setenv("CAELIS_THEME", "catppuccin")
	if !ThemeUsesAutoBackground() {
		t.Fatal("expected adaptive Catppuccin theme to use background detection")
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
	if theme.Name != "caelis-dusk" {
		t.Fatalf("default dark theme name = %q, want caelis-dusk", theme.Name)
	}
	if got := stringifyColor(theme.TextPrimary); got != "#e7e9ee" {
		t.Fatalf("expected default text to use explicit graphite foreground, got %q", got)
	}
	if got := stringifyColor(theme.AssistantFg); got != "#e7e9ee" {
		t.Fatalf("expected assistant text to match body foreground, got %q", got)
	}
	if got := stringifyColor(theme.ReasoningFg); got != "#9da5b6" {
		t.Fatalf("expected reasoning text to use low-contrast theme color, got %q", got)
	}
	if got := stringifyColor(theme.ToolFg); got != "#65b8b0" {
		t.Fatalf("expected tool meta to use restrained teal, got %q", got)
	}
	if got := stringifyColor(theme.Accent); got != "#7c9cf5" {
		t.Fatalf("expected Caelis sky-blue accent color, got %q", got)
	}
	if got := stringifyColor(theme.TranscriptRail); got != "#434c5e" {
		t.Fatalf("expected subtle transcript rail, got %q", got)
	}
	if got := stringifyColor(theme.Success); got != "#7fb58a" {
		t.Fatalf("expected dark Success green, got %q", got)
	}
	if got := stringifyColor(theme.DiffAddFg); got != "#7fb58a" {
		t.Fatalf("expected dark DiffAddFg green, got %q", got)
	}
	if got := stringifyColor(theme.DiffRemoveFg); got != "#e1848c" {
		t.Fatalf("expected dark DiffRemoveFg red/pink, got %q", got)
	}
}

func TestExplicitCatppuccinThemeRemainsAvailable(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("CAELIS_THEME", "catppuccin-mocha")

	theme := ResolveThemeFromOptions(false, colorprofile.TrueColor)
	if theme.Name != "catppuccin-mocha" {
		t.Fatalf("theme name = %q", theme.Name)
	}
	if got := stringifyColor(theme.Accent); got != "#cba6f7" {
		t.Fatalf("Catppuccin accent = %q", got)
	}
	if got := SyntaxPaletteForTheme(theme).ChromaTheme; got != CatppuccinMochaChromaTheme {
		t.Fatalf("Catppuccin Chroma theme = %q", got)
	}
}

func TestNamedThemesAvoidLeakage(t *testing.T) {
	t.Setenv("NO_COLOR", "")

	// Nord
	t.Setenv("CAELIS_THEME", "nord")
	nord := ResolveThemeFromOptions(false, colorprofile.TrueColor)
	if got := stringifyColor(nord.DiffAddFg); got != "#a3be8c" {
		t.Fatalf("expected nord DiffAddFg, got %q", got)
	}
	if got := stringifyColor(nord.DiffRemoveFg); got != "#d08770" {
		t.Fatalf("expected nord DiffRemoveFg, got %q", got)
	}

	// Solarized
	t.Setenv("CAELIS_THEME", "solarized")
	solarized := ResolveThemeFromOptions(false, colorprofile.TrueColor)
	if got := stringifyColor(solarized.DiffAddFg); got != "#859900" {
		t.Fatalf("expected solarized DiffAddFg, got %q", got)
	}
	if got := stringifyColor(solarized.DiffRemoveFg); got != "#dc322f" {
		t.Fatalf("expected solarized DiffRemoveFg, got %q", got)
	}

	// Dracula
	t.Setenv("CAELIS_THEME", "dracula")
	dracula := ResolveThemeFromOptions(false, colorprofile.TrueColor)
	if got := stringifyColor(dracula.DiffAddFg); got != "#50fa7b" {
		t.Fatalf("expected dracula DiffAddFg, got %q", got)
	}
	if got := stringifyColor(dracula.DiffRemoveFg); got != "#ff5555" {
		t.Fatalf("expected dracula DiffRemoveFg, got %q", got)
	}
}

func TestNamedThemesSelectMatchingSyntaxPalettes(t *testing.T) {
	for name, want := range map[string]string{
		"nord":      "nord",
		"solarized": "solarized-dark",
		"dracula":   "dracula",
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv("NO_COLOR", "")
			t.Setenv("CAELIS_THEME", name)
			theme := ResolveThemeFromOptions(false, colorprofile.TrueColor)
			if got := SyntaxPaletteForTheme(theme).ChromaTheme; got != want {
				t.Fatalf("SyntaxPaletteForTheme(%s).ChromaTheme = %q, want %q", name, got, want)
			}
		})
	}
}

func TestDefaultLightDarkPalettesExposeModernSemanticColors(t *testing.T) {
	dark := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	if dark.AppBg != nil {
		t.Fatalf("dark app bg = %v", dark.AppBg)
	}
	if got := stringifyColor(dark.Focus); got != "#7c9cf5" {
		t.Fatalf("dark focus = %q", got)
	}
	if got := stringifyColor(dark.CodeBlockFg); got != "#d8dce5" {
		t.Fatalf("dark code block fg = %q", got)
	}
	if got := stringifyColor(dark.CodeBlockBg); got != "#171a21" {
		t.Fatalf("dark code block bg = %q", got)
	}
	if got := stringifyColor(dark.CodeBg); got != "#1d222c" {
		t.Fatalf("dark inline code bg = %q", got)
	}

	light := ResolveThemeWithState(false, false, colorprofile.TrueColor)
	if light.AppBg != nil {
		t.Fatalf("light app bg = %v", light.AppBg)
	}
	if got := stringifyColor(light.ToolFg); got != "#0f766e" {
		t.Fatalf("light tool fg = %q", got)
	}
	if got := stringifyColor(light.CodeBlockFg); got != "#2d3440" {
		t.Fatalf("light code block fg = %q", got)
	}
	if got := stringifyColor(light.CodeBlockBg); got != "#f5f6f7" {
		t.Fatalf("light code block bg = %q", got)
	}
	if got := stringifyColor(light.CodeBg); got != "#eff1f3" {
		t.Fatalf("light inline code bg = %q", got)
	}
}

func TestTokensIncludeToolAndMarkdownSemantics(t *testing.T) {
	theme := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	tokens := theme.Tokens()
	if got := stringifyColor(tokens.ToolName.GetForeground()); got != "#e7e9ee" {
		t.Fatalf("tool name token foreground = %q", got)
	}
	if got, want := stringifyColor(tokens.MarkdownHeading.GetForeground()), stringifyColor(theme.TextPrimary); got != want {
		t.Fatalf("markdown heading token foreground = %q, want body text %q", got, want)
	}
	if got := tokens.MarkdownHeading.GetBackground(); colorIsPresent(got) {
		t.Fatalf("markdown heading token background = %v, want none", got)
	}
	if got := stringifyColor(tokens.MarkdownInlineCode.GetForeground()); got != "#a5b4d4" {
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
	if got := stringifyColor(tokens.MarkdownTableEdge.GetForeground()); got != "#526079" {
		t.Fatalf("table edge token foreground = %q", got)
	}
	if got := stringifyColor(tokens.TextSecondary.GetForeground()); got != "#a8afbd" {
		t.Fatalf("secondary text token foreground = %q", got)
	}
	if got := stringifyColor(tokens.ToolErrorMark.GetForeground()); got != "#e1848c" {
		t.Fatalf("tool error mark foreground = %q", got)
	}
	if got := stringifyColor(tokens.ToolError.GetForeground()); got != "#a8afbd" {
		t.Fatalf("tool error body foreground = %q", got)
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
	if got := stringifyColor(style.GetForeground()); got != "#e7e9ee" {
		t.Fatalf("selection foreground = %q", got)
	}
	if got := stringifyColor(style.GetBackground()); got != "#2a3a5c" {
		t.Fatalf("selection background = %q", got)
	}

	ansi := ResolveThemeWithState(true, false, colorprofile.ANSI)
	if !ansi.SelectionStyle().GetReverse() {
		t.Fatal("expected ANSI selection style to fall back to reverse video")
	}
}

func TestInputSelectionStyleUsesDedicatedPalette(t *testing.T) {
	theme := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	style := theme.InputSelectionStyle()
	if got := stringifyColor(style.GetForeground()); got != "#11151d" {
		t.Fatalf("input selection foreground = %q", got)
	}
	if got := stringifyColor(style.GetBackground()); got != "#7c9cf5" {
		t.Fatalf("input selection background = %q", got)
	}

	ansi := ResolveThemeWithState(true, false, colorprofile.ANSI)
	ansiStyle := ansi.InputSelectionStyle()
	if !ansiStyle.GetReverse() && (!colorIsPresent(ansiStyle.GetForeground()) || !colorIsPresent(ansiStyle.GetBackground())) {
		t.Fatal("expected ANSI input selection style to use reverse video or explicit ANSI colors")
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
	for _, name := range []string{
		"dark",
		"light",
		"catppuccin-mocha",
		"catppuccin-latte",
		"nord",
		"solarized",
		"dracula",
	} {
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

func TestValidateThemeRequiresDistinctConversationSurfaces(t *testing.T) {
	theme := ResolveThemeWithState(true, false, colorprofile.TrueColor)
	theme.ComposerBg = theme.UserBg
	issues := ValidateTheme(theme)
	if !slices.ContainsFunc(issues, func(issue ThemeIssue) bool {
		return issue.Field == "ComposerBg" && issue.Message == "must differ from UserBg"
	}) {
		t.Fatalf("ValidateTheme(equal surfaces) issues = %#v", issues)
	}

	theme.ComposerBg = nil
	issues = ValidateTheme(theme)
	if !slices.ContainsFunc(issues, func(issue ThemeIssue) bool {
		return issue.Field == "ComposerBg" && issue.Message == "surface is required"
	}) {
		t.Fatalf("ValidateTheme(missing composer) issues = %#v", issues)
	}
}

func TestBuiltInThemesKeepConversationSurfacesDistinctAcrossRichProfiles(t *testing.T) {
	names := []string{
		"dark",
		"light",
		"catppuccin-mocha",
		"catppuccin-latte",
		"nord",
		"solarized",
		"dracula",
	}
	profiles := []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256}
	for _, name := range names {
		for _, profile := range profiles {
			t.Run(fmt.Sprintf("%s/%v", name, profile), func(t *testing.T) {
				t.Setenv("NO_COLOR", "")
				t.Setenv("CAELIS_THEME", name)
				theme := ResolveThemeFromOptions(false, profile)
				if theme.UserBg == nil || theme.ComposerBg == nil {
					t.Fatalf("surfaces = user:%v composer:%v, want both", theme.UserBg, theme.ComposerBg)
				}
				if colorsEqual(theme.UserBg, theme.ComposerBg) {
					t.Fatalf("surfaces share %q", stringifyColor(theme.UserBg))
				}
			})
		}
	}
}

func TestValidateAdaptiveThemeAgainstSampledTerminalBackgrounds(t *testing.T) {
	backgrounds := map[string]color.Color{
		"mid dark":   color.RGBA{R: 0x2e, G: 0x34, B: 0x40, A: 0xff},
		"soft light": color.RGBA{R: 0xee, G: 0xf0, B: 0xf3, A: 0xff},
	}
	for name, background := range backgrounds {
		t.Run(name, func(t *testing.T) {
			theme := ResolveThemeWithBackgroundColor(background, false, colorprofile.TrueColor)
			if issues := ValidateTheme(theme); len(issues) != 0 {
				t.Fatalf("ValidateTheme(%s) issues = %#v", name, issues)
			}
			if ratio := contrastRatio(theme.MutedText, theme.ModalBg); ratio < normalTextContrast {
				t.Fatalf("MutedText/ModalBg contrast = %.2f, want at least %.2f", ratio, normalTextContrast)
			}
		})
	}
}

func TestValidateThemeChecksMutedTextAgainstModalSurface(t *testing.T) {
	background := color.RGBA{R: 0x2e, G: 0x34, B: 0x40, A: 0xff}
	theme := ResolveThemeWithBackgroundColor(background, false, colorprofile.TrueColor)
	theme.MutedText = lipgloss.Color("#9da5b6")

	issues := ValidateTheme(theme)
	if !slices.ContainsFunc(issues, func(issue ThemeIssue) bool {
		return issue.Field == "MutedText/ModalBg" && issue.Message == "contrast below threshold"
	}) {
		t.Fatalf("ValidateTheme(low modal contrast) issues = %#v", issues)
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
	if got := stringifyColor(dark.ModalBg); got != "#0f0f0f" {
		t.Fatalf("dark modal bg = %q", got)
	}
	if got := stringifyColor(dark.UserBg); got != "#191615" {
		t.Fatalf("dark user bg = %q", got)
	}
	if got := stringifyColor(dark.ComposerBg); got != "#0f0f0f" {
		t.Fatalf("dark composer bg = %q", got)
	}

	light := ResolveThemeWithBackgroundColor(color.RGBA{R: 255, G: 255, B: 255, A: 255}, false, colorprofile.TrueColor)
	if light.IsDark {
		t.Fatal("expected white terminal background to select light theme")
	}
	if got := stringifyColor(light.UserBg); got != "#f4f1f1" {
		t.Fatalf("light user bg = %q", got)
	}
	if got := stringifyColor(light.ComposerBg); got != "#f7f7f7" {
		t.Fatalf("light composer bg = %q", got)
	}
	if got := stringifyColor(light.CodeBg); got != "#f0f0f0" {
		t.Fatalf("light code bg = %q", got)
	}
}

func TestAdaptiveDefaultThemeDisablesRichBackgroundsForANSI(t *testing.T) {
	theme := ResolveThemeWithState(true, false, colorprofile.ANSI)
	if theme.DiffAddBg != nil || theme.DiffRemoveBg != nil || theme.CodeBg != nil || theme.CommandActive != nil {
		t.Fatalf("expected ANSI theme to avoid rich backgrounds, got add=%v del=%v code=%v selection=%v", theme.DiffAddBg, theme.DiffRemoveBg, theme.CodeBg, theme.CommandActive)
	}
	if got := stringifyColor(theme.Focus); got != "4" {
		t.Fatalf("expected ANSI focus blue, got %q", got)
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
