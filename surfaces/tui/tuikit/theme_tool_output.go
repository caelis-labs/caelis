package tuikit

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// toolOutputColor derives quieter transcript roles from the selected palette.
// It never changes syntax colors; terminal conversion and the normal text
// contrast floor take precedence over the requested visual separation.
func toolOutputColor(t Theme, strength, contrast float64) color.Color {
	if t.NoColor {
		return nil
	}
	if t.Profile < colorprofile.ANSI256 {
		// ANSI palettes belong to the terminal; bright black may be unreadable.
		return t.TextSecondary
	}
	bg := validationBackground(t)
	br, bgc, bb, bok := rgb8(bg)
	fr, fg, fb, fok := rgb8(t.MutedText)
	if !bok || !fok {
		return t.MutedText
	}
	c := lipgloss.Color(hexColor(blendRGB([3]uint8{br, bgc, bb}, [3]uint8{fr, fg, fb}, strength)))
	return readablePaletteColor(t.Profile.Convert(c), t.Profile.Convert(bg), contrast, colorIsDark(bg), t.Profile)
}

// ToolOutputMetaStyle styles fold counts and structural marks below tool text.
func (t Theme) ToolOutputMetaStyle() lipgloss.Style {
	return t.Tokens().ToolOutputMeta
}
