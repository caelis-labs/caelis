package tuikit

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Catppuccin roles follow https://github.com/catppuccin/catppuccin/blob/main/docs/style-guide.md.
// The full palette includes Base, so a fixed Mocha choice never inherits a light
// terminal background. Auto chooses a flavour using terminal brightness.
func catppuccinAdaptiveThemeVariant(profile colorprofile.Profile, dark bool, _ color.Color) Theme {
	colors := []string{
		"#eff1f5", "#e6e9ef", "#ccd0da", "#bcc0cc", "#4c4f69", "#5c5f77", "#6c6f85",
		"#1e66f5", "#7287fd", "#8839ef", "#40a02b", "#df8e1d", "#d20f39", "#179299", "#dc8a78",
	}
	name := "catppuccin-latte"
	if dark {
		name = "catppuccin-mocha"
		colors = []string{
			"#1e1e2e", "#181825", "#313244", "#45475a", "#cdd6f4", "#bac2de", "#a6adc8",
			"#89b4fa", "#b4befe", "#cba6f7", "#a6e3a1", "#f9e2af", "#f38ba8", "#94e2d5", "#f5e0dc",
		}
	}
	c := func(i int) color.Color { return profile.Convert(lipgloss.Color(colors[i])) }
	return themeFrom(themePalette{
		Name: name, IsDark: dark, TextPrimary: c(4), TextSecondary: c(5), Muted: c(6),
		Info: c(7), Success: c(10), Warning: c(11), Danger: c(12), Accent: c(7), Focus: c(9),
		UserAccent: c(14), Tool: c(13), Border: c(2), BorderStrong: c(3), DiffHunk: c(9),
		DiffLineNo: c(6), Cursor: c(14), Scrollbar: c(6), AgentMessageSent: c(7), AgentMessageReceived: c(13),
	}, themeSurfaces{
		App: c(0), Base: c(1), Raised: c(2), User: c(2), Composer: c(1),
		Selection: c(2), SelectionText: c(4), OnAccent: c(0),
		DiffAdd:          paletteTint(profile, colors[0], colors[10], .15),
		DiffAddStrong:    paletteTint(profile, colors[0], colors[10], .25),
		DiffRemove:       paletteTint(profile, colors[0], colors[12], .15),
		DiffRemoveStrong: paletteTint(profile, colors[0], colors[12], .25),
	})
}

// Terminals lack alpha; precompose semantic diff colors over the palette base.
// Catppuccin's diff guide specifies 10–20% line tint and 15–25% changed text tint.
func paletteTint(profile colorprofile.Profile, base, foreground string, opacity float64) color.Color {
	r, g, b, _ := rgb8(lipgloss.Color(base))
	fr, fg, fb, _ := rgb8(lipgloss.Color(foreground))
	return profile.Convert(lipgloss.Color(hexColor(blendRGB([3]uint8{r, g, b}, [3]uint8{fr, fg, fb}, opacity))))
}
