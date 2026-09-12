package tuikit

import (
	"image/color"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

func TestDiffSurfacesKeepLineAndIntralineHierarchy(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256} {
		for _, background := range []color.Color{color.Black, color.White} {
			for _, option := range ThemeOptions(profile, false) {
				theme := ResolveSelectedTheme(option.Name, background, colorIsDark(background), false, profile)
				base := validationBackground(theme)
				for _, pair := range [][2]color.Color{{theme.DiffAddBg, theme.DiffAddStrongBg}, {theme.DiffRemoveBg, theme.DiffRemoveStrongBg}} {
					if colorsEqual(pair[0], base) || colorsEqual(pair[0], pair[1]) {
						t.Errorf("%s/%s lost diff surface separation: %v", theme.Name, profile, pair)
					}
					if profile == colorprofile.TrueColor {
						minimum := 1.2
						if theme.IsDark {
							minimum = 1.3
						}
						if ratio := contrastRatio(pair[0], base); ratio < minimum {
							t.Errorf("%s line tint too faint: %.2f", theme.Name, ratio)
						}
						if contrastRatio(pair[1], base) <= contrastRatio(pair[0], base) {
							t.Errorf("%s intraline tint must be stronger than line tint", theme.Name)
						}
					}
				}
			}
		}
	}
}

func TestReadableTextColorUsesFinalDiffSurface(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256} {
		for _, name := range []string{"catppuccin-mocha", "catppuccin-latte", "nord", "dracula"} {
			theme := ResolveSelectedTheme(name, nil, true, false, profile)
			palette := SyntaxPaletteForTheme(theme)
			for _, fg := range []color.Color{palette.Text, palette.Comment, palette.Keyword, palette.String, palette.Number} {
				for _, bg := range []color.Color{theme.AppBg, theme.DiffAddBg, theme.DiffRemoveBg, theme.DiffAddStrongBg, theme.DiffRemoveStrongBg} {
					got := theme.ReadableTextColor(fg, bg)
					if ratio := contrastRatio(got, bg); ratio < normalTextContrast {
						t.Errorf("%s/%s final text contrast %.2f", name, profile, ratio)
					}
					if contrastRatio(fg, bg) >= normalTextContrast && !colorsEqual(got, fg) {
						t.Errorf("%s changed an already readable syntax color", name)
					}
				}
			}
		}
	}
	light := ResolveSelectedTheme("catppuccin-latte", color.Black, true, false, colorprofile.TrueColor)
	for _, background := range []color.Color{nil, lipgloss.NoColor{}} {
		got := light.ReadableTextColor(light.TextPrimary, background)
		if !colorsEqual(got, light.TextPrimary) {
			t.Fatal("inherited background must use the painted light base, not black")
		}
	}
	fg := lipgloss.Color("#808080")
	if got := (Theme{NoColor: true}).ReadableTextColor(fg, color.Black); got != nil {
		t.Fatal("NO_COLOR introduced a foreground")
	}
	if got := (Theme{Profile: colorprofile.ANSI}).ReadableTextColor(fg, color.Black); !colorsEqual(got, fg) {
		t.Fatal("ANSI text must retain the terminal's palette")
	}
}
