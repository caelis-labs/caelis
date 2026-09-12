package tuikit

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

const normalTextContrast = 4.5

// ensureThemeTextContrast preserves the authored palette when it already
// passes. Contrast corrections keep the hue and account for sampled surfaces
// and indexed-color conversion.
func ensureThemeTextContrast(theme Theme) Theme {
	if theme.NoColor || theme.Profile < colorprofile.ANSI256 {
		return theme
	}

	muted := theme.MutedText
	backgrounds := []color.Color{
		validationBackground(theme),
		firstColor(theme.ModalBg, validationBackground(theme)),
	}
	for _, background := range backgrounds {
		muted = readablePaletteColor(muted, background, normalTextContrast, theme.IsDark, theme.Profile)
	}
	theme.MutedText = muted
	theme.ReasoningFg = muted
	theme.CommandSubText = muted
	theme.NoteFg = muted
	if theme.AppBg != nil {
		// Preserve community hues; only darken/lighten colors that fail on the
		// actual surface, including after conversion to the terminal's palette.
		fix := func(fg *color.Color, bg color.Color, ratio float64) {
			*fg = readablePaletteColor(*fg, bg, ratio, theme.IsDark, theme.Profile)
		}
		fix(&theme.Accent, theme.AppBg, 4.5)
		fix(&theme.LinkFg, theme.AppBg, 4.5)
		fix(&theme.UserPrefixFg, theme.UserBg, 3)
		fix(&theme.Warning, theme.AppBg, 3)
		fix(&theme.Success, theme.AppBg, 3)
		fix(&theme.Error, theme.AppBg, 3)
		fix(&theme.HelpHintFg, theme.ComposerBg, 4.5)
		fix(&theme.ComposerBorderFocus, theme.ComposerBg, 3)
		theme.InputSelectionFg = readablePaletteColor(theme.InputSelectionFg, theme.InputSelectionBg, 4.5, colorIsDark(theme.InputSelectionBg), theme.Profile)
		fix(&theme.DiffAddFg, theme.DiffAddBg, 3)
		fix(&theme.DiffRemoveFg, theme.DiffRemoveBg, 3)
	}
	return theme
}

// ReadableTextColor preserves foreground unless it needs a contrast correction
// on background. A nil or inherited background uses the painted or sampled base.
// Indexed colors are checked after conversion; ANSI and NO_COLOR stay unchanged.
func (t Theme) ReadableTextColor(foreground, background color.Color) color.Color {
	if t.NoColor {
		return nil
	}
	profile := t.Profile
	if profile == colorprofile.Unknown {
		profile = colorprofile.TrueColor
	}
	if profile < colorprofile.ANSI256 {
		return foreground
	}
	if _, inherited := background.(lipgloss.NoColor); inherited {
		background = nil
	}
	foreground = profile.Convert(foreground)
	background = profile.Convert(firstColor(background, validationBackground(t)))
	return readablePaletteColor(foreground, background, normalTextContrast, colorIsDark(background), profile)
}

func readablePaletteColor(fg, bg color.Color, threshold float64, dark bool, profile colorprofile.Profile) color.Color {
	if fg == nil || bg == nil {
		return fg
	}
	if contrastRatio(fg, bg) >= threshold {
		return fg
	}
	r, g, b, ok := rgb8(fg)
	if !ok {
		return fg
	}
	target := [3]uint8{}
	if dark {
		target = [3]uint8{255, 255, 255}
	}
	endpoint := profile.Convert(color.RGBA{R: target[0], G: target[1], B: target[2], A: 255})
	if contrastRatio(endpoint, bg) < threshold {
		for i := range target {
			target[i] = 255 - target[i]
		}
	}
	for step := 1; step <= 100; step++ {
		candidate := profile.Convert(lipgloss.Color(hexColor(blendRGB([3]uint8{r, g, b}, target, float64(step)/100))))
		if contrastRatio(candidate, bg) >= threshold {
			return candidate
		}
	}
	return fg
}
