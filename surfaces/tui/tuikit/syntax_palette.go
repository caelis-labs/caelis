package tuikit

import (
	"image/color"

	"github.com/charmbracelet/colorprofile"
)

const (
	CatppuccinMochaChromaTheme = "catppuccin-mocha"
	CatppuccinLatteChromaTheme = "catppuccin-latte"
)

type SyntaxPalette struct {
	ChromaTheme      string
	Text             color.Color
	Background       color.Color
	InlineBackground color.Color
	Comment          color.Color
	Keyword          color.Color
	Function         color.Color
	String           color.Color
	Number           color.Color
	Operator         color.Color
	Path             color.Color
	Variable         color.Color
	Deleted          color.Color
	Inserted         color.Color
}

func SyntaxPaletteForTheme(theme Theme) SyntaxPalette {
	return catppuccinSyntaxPalette(theme.IsDark, theme.Profile)
}

// applyCatppuccinCodeColors is the single source of truth for adaptive-theme inline code color.
func applyCatppuccinCodeColors(theme *Theme) {
	if theme == nil || theme.NoColor {
		return
	}
	palette := SyntaxPaletteForTheme(*theme)
	if theme.IsDark {
		theme.CodeFg = syntaxColor(theme.Profile, "#b4befe", "147", "5")
	} else {
		theme.CodeFg = syntaxColor(theme.Profile, "#7287fd", "63", "5")
	}
	theme.CodeBg = palette.InlineBackground
	theme.CodeBlockFg = palette.Text
	theme.CodeBlockBg = palette.Background
	theme.CodeSurface = palette.Background
}

func catppuccinSyntaxPalette(dark bool, profile colorprofile.Profile) SyntaxPalette {
	if profile == colorprofile.Unknown {
		profile = colorprofile.TrueColor
	}
	if dark {
		return SyntaxPalette{
			ChromaTheme:      CatppuccinMochaChromaTheme,
			Text:             syntaxColor(profile, "#cdd6f4", "189", "7"),
			Background:       syntaxColor(profile, "#1e1e2e", "235", ""),
			InlineBackground: syntaxColor(profile, "#181825", "", ""),
			Comment:          syntaxColor(profile, "#6c7086", "242", "8"),
			Keyword:          syntaxColor(profile, "#cba6f7", "183", "5"),
			Function:         syntaxColor(profile, "#89b4fa", "111", "6"),
			String:           syntaxColor(profile, "#a6e3a1", "151", "2"),
			Number:           syntaxColor(profile, "#fab387", "216", "3"),
			Operator:         syntaxColor(profile, "#89dceb", "117", "6"),
			Path:             syntaxColor(profile, "#89b4fa", "111", "6"),
			Variable:         syntaxColor(profile, "#f5e0dc", "224", "5"),
			Deleted:          syntaxColor(profile, "#f38ba8", "211", "1"),
			Inserted:         syntaxColor(profile, "#a6e3a1", "151", "2"),
		}
	}
	return SyntaxPalette{
		ChromaTheme:      CatppuccinLatteChromaTheme,
		Text:             syntaxColor(profile, "#4c4f69", "60", "0"),
		Background:       syntaxColor(profile, "#eff1f5", "255", ""),
		InlineBackground: syntaxColor(profile, "#eff1f5", "", ""),
		Comment:          syntaxColor(profile, "#9ca0b0", "247", "8"),
		Keyword:          syntaxColor(profile, "#8839ef", "93", "5"),
		Function:         syntaxColor(profile, "#1e66f5", "33", "4"),
		String:           syntaxColor(profile, "#40a02b", "70", "2"),
		Number:           syntaxColor(profile, "#fe640b", "202", "3"),
		Operator:         syntaxColor(profile, "#04a5e5", "39", "6"),
		Path:             syntaxColor(profile, "#1e66f5", "33", "4"),
		Variable:         syntaxColor(profile, "#dc8a78", "174", "5"),
		Deleted:          syntaxColor(profile, "#d20f39", "160", "1"),
		Inserted:         syntaxColor(profile, "#40a02b", "70", "2"),
	}
}

func syntaxColor(profile colorprofile.Profile, rich, ansi256, ansi16 string) color.Color {
	return profileColor(profile, rich, ansi256, ansi16)
}
