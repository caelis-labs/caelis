package tuikit

import (
	"image/color"
	"slices"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/charmbracelet/colorprofile"
)

func TestSelectableThemesReadableOnLightAndDarkTerminals(t *testing.T) {
	t.Setenv("CAELIS_ACCENT", "")
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256} {
		for _, background := range []color.Color{color.Black, color.White} {
			for _, option := range ThemeOptions(profile, false) {
				theme := ResolveSelectedTheme(option.Name, background, colorIsDark(background), false, profile)
				if issues := ValidateTheme(theme); len(issues) != 0 {
					t.Errorf("%s / %s on %v: %+v", option.Name, profile, background, issues)
				}
				if option.Name != "auto" && theme.AppBg == nil {
					t.Errorf("%s leaves background unpainted", option.Name)
				}
			}
		}
	}
	for _, profile := range []colorprofile.Profile{colorprofile.ANSI, colorprofile.NoTTY} {
		if options := ThemeOptions(profile, false); len(options) != 1 || options[0].Name != "auto" {
			t.Fatalf("unsupported color choices = %+v", options)
		}
		theme := ResolveSelectedTheme("nord", color.White, false, false, profile)
		if theme.IsDark || theme.AppBg != nil {
			t.Fatal("low-color fallback is not light-terminal aware")
		}
	}
	if options := ThemeOptions(colorprofile.TrueColor, true); len(options) != 1 {
		t.Fatal("NO_COLOR offered invisible palette variants")
	}
}

func TestThemeOptionsEmphasizeNames(t *testing.T) {
	options := ThemeOptions(colorprofile.TrueColor, false)
	var names []string
	for _, option := range options {
		names = append(names, option.Name)
	}
	want := []string{"auto", "catppuccin", "catppuccin-latte", "catppuccin-mocha", "dracula", "nord"}
	if !slices.Equal(names, want) {
		t.Fatalf("theme order = %v, want %v", names, want)
	}
	for i, label := range []string{"Terminal", "Catppuccin", "Catppuccin Latte", "Catppuccin Mocha", "Dracula", "Nord"} {
		if options[i].Label != label {
			t.Errorf("theme %s label = %q, want %q", options[i].Name, options[i].Label, label)
		}
	}
}

func TestValidateThemeRejectsInvisibleComposer(t *testing.T) {
	theme := ResolveSelectedTheme("dracula", nil, true, false, colorprofile.TrueColor)
	theme.ComposerBg = theme.AppBg
	if !slices.ContainsFunc(ValidateTheme(theme), func(issue ThemeIssue) bool {
		return issue.Field == "ComposerBg" && issue.Message == "must differ from AppBg"
	}) {
		t.Fatal("composer indistinguishable from page background must be rejected")
	}
}

func TestCommunityShellAndMarkdownUseSameReadableSyntaxSource(t *testing.T) {
	for _, name := range []string{"catppuccin-mocha", "catppuccin-latte", "nord", "dracula"} {
		theme := ResolveSelectedTheme(name, nil, true, false, colorprofile.TrueColor)
		palette := SyntaxPaletteForTheme(theme)
		style := styles.Get(palette.ChromaTheme)
		for _, pair := range []struct {
			color color.Color
			token chroma.TokenType
		}{
			{palette.Keyword, chroma.Keyword}, {palette.Function, chroma.NameFunction}, {palette.Comment, chroma.Comment}, {palette.String, chroma.LiteralString}, {palette.Number, chroma.LiteralNumber},
		} {
			if got := stringifyColor(pair.color); got != style.Get(pair.token).Colour.String() {
				t.Fatalf("%s syntax sources disagree for %s", name, pair.token)
			}
			if ratio := contrastRatio(pair.color, theme.AppBg); ratio < 4.5 {
				t.Fatalf("%s %s contrast = %.2f", name, pair.token, ratio)
			}
		}
	}
}
