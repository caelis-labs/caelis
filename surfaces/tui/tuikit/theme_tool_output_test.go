package tuikit

import (
	"image/color"
	"testing"

	"github.com/charmbracelet/colorprofile"
)

func TestToolOutputHierarchyRemainsReadableAcrossThemes(t *testing.T) {
	for _, profile := range []colorprofile.Profile{colorprofile.TrueColor, colorprofile.ANSI256} {
		for _, name := range []string{"auto", "catppuccin-mocha", "catppuccin-latte", "nord", "dracula"} {
			for _, background := range []color.Color{color.Black, color.White} {
				theme := ResolveSelectedTheme(name, background, background == color.Black, false, profile)
				bg := validationBackground(theme)
				output := contrastRatio(theme.ToolOutputStyle().GetForeground(), bg)
				meta := contrastRatio(theme.ToolOutputMetaStyle().GetForeground(), bg)
				body := contrastRatio(theme.TextPrimary, bg)
				if output < 4.5 || meta < 4.5 || meta > output || output > body {
					t.Fatalf("%s/%v: body %.2f output %.2f meta %.2f", name, profile, body, output, meta)
				}
			}
		}
	}
	plain := ResolveThemeWithState(true, true, colorprofile.TrueColor)
	if plain.ToolOutputStyle().Render("result") != "result" || plain.ToolOutputMetaStyle().Render("... +24 lines") != "... +24 lines" {
		t.Fatal("NO_COLOR emitted styling")
	}
	ansi := ResolveThemeWithState(true, false, colorprofile.ANSI)
	if !colorsEqual(ansi.ToolOutputStyle().GetForeground(), ansi.TextSecondary) || !colorsEqual(ansi.ToolOutputMetaStyle().GetForeground(), ansi.TextSecondary) {
		t.Fatal("ANSI output should retain the terminal's readable secondary color")
	}
}
