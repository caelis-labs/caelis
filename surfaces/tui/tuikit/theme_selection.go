package tuikit

import (
	"image/color"
	"strings"

	"github.com/charmbracelet/colorprofile"
)

// ThemeOption is a TUI-local choice; it is not an Agent command or setting.
type ThemeOption struct {
	Name   string
	Label  string
	Detail string
}

// ThemeOptions lists only palettes supported by the terminal's color profile.
// Named palettes paint their own background, so both light and dark variants
// remain readable regardless of the terminal's default background.
func ThemeOptions(profile colorprofile.Profile, noColor bool) []ThemeOption {
	options := []ThemeOption{{"auto", "Terminal", "Follow terminal background"}}
	if noColor || profile < colorprofile.ANSI256 {
		return options
	}
	return append(options,
		ThemeOption{"catppuccin", "Catppuccin Auto", "Mocha / Latte · follow terminal brightness"},
		ThemeOption{"catppuccin-mocha", "Catppuccin Mocha", "Dark"},
		ThemeOption{"catppuccin-latte", "Catppuccin Latte", "Light"},
		ThemeOption{"nord", "Nord", "Dark"},
		ThemeOption{"dracula", "Dracula", "Dark"},
	)
}

// NormalizeThemeName accepts the documented environment names and short aliases.
// Interactive callers reject unknown names rather than silently resetting a theme.
func NormalizeThemeName(name string) (string, bool) {
	switch name = strings.ToLower(strings.TrimSpace(name)); name {
	case "", "auto", "default", "terminal":
		return "auto", true
	case "catppuccin", "catppuccin-auto":
		return "catppuccin", true
	case "mocha", "catppuccin-dark", "catppuccin-mocha":
		return "catppuccin-mocha", true
	case "latte", "catppuccin-light", "catppuccin-latte":
		return "catppuccin-latte", true
	case "nord", "dracula", "solarized", "dark", "light":
		return name, true
	default:
		return "", false
	}
}

// ResolveSelectedTheme resolves an instance-local selection against the sampled
// terminal state. It never changes environment variables or Control settings.
func ResolveSelectedTheme(name string, background color.Color, dark bool, noColor bool, profile colorprofile.Profile) Theme {
	name, _ = NormalizeThemeName(name)
	return resolveTheme(themeResolveOptions{
		name:            &name,
		backgroundKnown: true, backgroundDark: dark,
		backgroundColorKnown: background != nil, backgroundColor: background,
		noColor: noColor, colorProfileKnown: profile != colorprofile.Unknown, colorProfile: profile,
	})
}
