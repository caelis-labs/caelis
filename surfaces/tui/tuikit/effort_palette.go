package tuikit

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

var (
	effortSpectrumDark = [6][3]uint8{
		{0x74, 0xc7, 0xec}, // Sky
		{0xa6, 0xe3, 0xa1}, // Green
		{0xf9, 0xe2, 0xaf}, // Gold
		{0xfa, 0xb3, 0x87}, // Orange
		{0xf3, 0x8b, 0xa8}, // Rose
		{0xcb, 0xa6, 0xf7}, // Mauve
	}
	effortSpectrumLight = [6][3]uint8{
		{0x04, 0xa5, 0xe5}, // Sky
		{0x40, 0xa0, 0x2b}, // Green
		{0xdf, 0x8e, 0x1d}, // Amber
		{0xfe, 0x64, 0x0b}, // Orange
		{0xd2, 0x0f, 0x39}, // Rose
		{0x88, 0x39, 0xef}, // Purple
	}
	effortSpectrumANSI256Dark  = [6]string{"39", "78", "221", "209", "204", "177"}
	effortSpectrumANSI256Light = [6]string{"32", "28", "172", "166", "161", "92"}
)

// EffortBlockColor returns a rainbow-gradient foreground color for segment step
// (0-indexed) out of total filled effort blocks, adapted for readability on bg.
func (t Theme) EffortBlockColor(step, total int, bg color.Color) color.Color {
	if t.NoColor || t.Profile == colorprofile.ANSI || total <= 0 {
		return nil
	}
	if step < 0 {
		step = 0
	}
	if step >= total {
		step = total - 1
	}
	profile := t.Profile
	if profile == colorprofile.Unknown {
		profile = colorprofile.TrueColor
	}
	surface := bg
	if _, inherited := surface.(lipgloss.NoColor); inherited {
		surface = nil
	}
	dark := t.IsDark
	if surface != nil {
		dark = colorIsDark(profile.Convert(surface))
	}

	var raw color.Color
	if profile == colorprofile.ANSI256 {
		stops := effortSpectrumANSI256Light
		if dark {
			stops = effortSpectrumANSI256Dark
		}
		idx := 0
		if total > 1 {
			idx = step * (len(stops) - 1) / (total - 1)
		}
		raw = lipgloss.Color(stops[idx])
	} else {
		stops := effortSpectrumLight
		if dark {
			stops = effortSpectrumDark
		}
		pos := 0.0
		if total > 1 {
			pos = float64(step) * float64(len(stops)-1) / float64(total-1)
		}
		lo := int(pos)
		hi := min(len(stops)-1, lo+1)
		alpha := pos - float64(lo)
		raw = lipgloss.Color(hexColor(blendRGB(stops[lo], stops[hi], alpha)))
	}
	return t.ReadableTextColor(raw, firstColor(surface, t.ModalBg, validationBackground(t)))
}
