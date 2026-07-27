package tuikit

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

const normalTextContrast = 4.5

// ensureThemeTextContrast preserves the authored palette when it already
// passes, and only shifts the muted family when a sampled terminal background
// produces a lower-contrast adaptive surface.
func ensureThemeTextContrast(theme Theme) Theme {
	if theme.NoColor || theme.Profile != colorprofile.TrueColor {
		return theme
	}

	muted := theme.MutedText
	backgrounds := []color.Color{
		validationBackground(theme),
		firstColor(theme.ModalBg, validationBackground(theme)),
	}
	for _, background := range backgrounds {
		muted = ensureForegroundContrast(muted, background, normalTextContrast, theme.IsDark)
	}
	if colorsEqual(muted, theme.MutedText) {
		return theme
	}

	theme.MutedText = muted
	theme.ReasoningFg = muted
	theme.CommandSubText = muted
	theme.NoteFg = muted
	return theme
}

func ensureForegroundContrast(foreground, background color.Color, threshold float64, darkBackground bool) color.Color {
	if foreground == nil || background == nil || contrastRatio(foreground, background) >= threshold {
		return foreground
	}
	r, g, b, ok := rgb8(foreground)
	if !ok {
		return foreground
	}
	target := [3]uint8{}
	if darkBackground {
		target = [3]uint8{255, 255, 255}
	}
	base := [3]uint8{r, g, b}
	for step := 1; step <= 100; step++ {
		candidate := lipgloss.Color(hexColor(blendRGB(base, target, float64(step)/100)))
		if contrastRatio(candidate, background) >= threshold {
			return candidate
		}
	}
	return foreground
}
