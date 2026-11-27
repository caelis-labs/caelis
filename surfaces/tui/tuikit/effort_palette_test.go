package tuikit

import (
	"image/color"
	"testing"

	"github.com/charmbracelet/colorprofile"
)

func TestEffortBlockColorProfilesAndContrast(t *testing.T) {
	for _, dark := range []bool{true, false} {
		theme := ResolveThemeWithState(dark, false, colorprofile.TrueColor)
		for _, bg := range []color.Color{theme.ModalBg, theme.SelectionBg} {
			var colors []color.Color
			for step := range 6 {
				c := theme.EffortBlockColor(step, 6, bg)
				if c == nil {
					t.Fatalf("TrueColor dark=%t step=%d returned nil", dark, step)
				}
				if bg != nil && contrastRatio(c, bg) < 4.5 {
					t.Fatalf("TrueColor dark=%t step=%d contrast=%.2f < 4.5 on bg", dark, step, contrastRatio(c, bg))
				}
				colors = append(colors, c)
			}
			r0, g0, b0, _ := colors[0].RGBA()
			r5, g5, b5, _ := colors[5].RGBA()
			if r0 == r5 && g0 == g5 && b0 == b5 {
				t.Fatalf("dark=%t gradient endpoints did not vary", dark)
			}
		}

		ansi256 := ResolveThemeWithState(dark, false, colorprofile.ANSI256)
		if c := ansi256.EffortBlockColor(2, 5, ansi256.ModalBg); c == nil {
			t.Fatalf("ANSI256 dark=%t returned nil", dark)
		}
	}

	ansi16 := ResolveThemeWithState(true, false, colorprofile.ANSI)
	if c := ansi16.EffortBlockColor(0, 3, ansi16.ModalBg); c != nil {
		t.Fatalf("ANSI16 returned non-nil color %v", c)
	}
	noColor := ResolveThemeWithState(true, true, colorprofile.TrueColor)
	if c := noColor.EffortBlockColor(0, 3, nil); c != nil {
		t.Fatalf("NO_COLOR returned non-nil color %v", c)
	}
}
