package tuikit

import (
	"image/color"

	"github.com/charmbracelet/colorprofile"
)

const (
	CatppuccinMochaChromaTheme = "catppuccin-mocha"
	CatppuccinLatteChromaTheme = "catppuccin-latte"

	// The Glamour adapter registers these named Chroma styles from the same
	// semantic palettes used by shell and inline-code rendering.
	caelisDuskChromaTheme = "caelis-dusk"
	caelisDawnChromaTheme = "caelis-dawn"
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
	switch theme.Name {
	case "caelis-dusk", "caelis-dawn":
		return caelisSyntaxPalette(theme)
	case "catppuccin-mocha", "catppuccin-latte":
		return catppuccinSyntaxPalette(theme.IsDark, theme.Profile)
	default:
		return semanticSyntaxPalette(theme)
	}
}

// applySyntaxColors keeps inline and block code colors aligned with the
// selected theme instead of leaking the default palette into named themes.
func applySyntaxColors(theme *Theme) {
	if theme == nil || theme.NoColor {
		return
	}
	palette := SyntaxPaletteForTheme(*theme)
	theme.CodeFg = palette.Keyword
	theme.CodeBg = palette.InlineBackground
	theme.CodeBlockFg = palette.Text
	theme.CodeBlockBg = palette.Background
	theme.CodeSurface = palette.Background
}

func caelisSyntaxPalette(theme Theme) SyntaxPalette {
	profile := theme.Profile
	if profile == colorprofile.Unknown {
		profile = colorprofile.TrueColor
	}
	if theme.IsDark {
		return SyntaxPalette{
			ChromaTheme:      caelisDuskChromaTheme,
			Text:             syntaxColor(profile, "#d8dce5", "253", "7"),
			Background:       firstColor(theme.ModalBg, syntaxColor(profile, "#171a21", "234", "")),
			InlineBackground: firstColor(theme.TranscriptPillBg, syntaxColor(profile, "#1d222c", "235", "")),
			Comment:          syntaxColor(profile, "#7f8899", "244", "8"),
			Keyword:          syntaxColor(profile, "#a5b4d4", "146", "4"),
			Function:         syntaxColor(profile, "#9aade0", "111", "4"),
			String:           syntaxColor(profile, "#92b79a", "108", "2"),
			Number:           syntaxColor(profile, "#c6a477", "179", "3"),
			Operator:         syntaxColor(profile, "#8baeaa", "109", "6"),
			Path:             syntaxColor(profile, "#9aade0", "111", "4"),
			Variable:         syntaxColor(profile, "#b5a1a5", "145", "5"),
			Deleted:          syntaxColor(profile, "#e1848c", "174", "1"),
			Inserted:         syntaxColor(profile, "#7fb58a", "108", "2"),
		}
	}
	return SyntaxPalette{
		ChromaTheme:      caelisDawnChromaTheme,
		Text:             syntaxColor(profile, "#2d3440", "236", "0"),
		Background:       firstColor(theme.ModalBg, syntaxColor(profile, "#f5f6f7", "255", "")),
		InlineBackground: firstColor(theme.TranscriptPillBg, syntaxColor(profile, "#eff1f3", "254", "")),
		Comment:          syntaxColor(profile, "#707888", "243", "8"),
		Keyword:          syntaxColor(profile, "#596d9d", "61", "4"),
		Function:         syntaxColor(profile, "#496aa7", "25", "4"),
		String:           syntaxColor(profile, "#4f765a", "29", "2"),
		Number:           syntaxColor(profile, "#8b6734", "130", "3"),
		Operator:         syntaxColor(profile, "#476f6b", "30", "6"),
		Path:             syntaxColor(profile, "#496aa7", "25", "4"),
		Variable:         syntaxColor(profile, "#805e66", "95", "1"),
		Deleted:          syntaxColor(profile, "#b73a4a", "160", "1"),
		Inserted:         syntaxColor(profile, "#2f7d48", "28", "2"),
	}
}

func semanticSyntaxPalette(theme Theme) SyntaxPalette {
	chromaTheme := CatppuccinMochaChromaTheme
	switch theme.Name {
	case "nord":
		chromaTheme = "nord"
	case "solarized":
		chromaTheme = "solarized-dark"
	case "dracula":
		chromaTheme = "dracula"
	default:
		if !theme.IsDark {
			chromaTheme = CatppuccinLatteChromaTheme
		}
	}
	return SyntaxPalette{
		ChromaTheme:      chromaTheme,
		Text:             theme.TextPrimary,
		Background:       theme.ModalBg,
		InlineBackground: firstColor(theme.TranscriptPillBg, theme.ModalBg),
		Comment:          firstColor(theme.MutedText, theme.TextSecondary),
		Keyword:          firstColor(theme.Accent, theme.TextPrimary),
		Function:         firstColor(theme.Focus, theme.TextPrimary),
		String:           firstColor(theme.Success, theme.TextPrimary),
		Number:           firstColor(theme.Warning, theme.TextPrimary),
		Operator:         firstColor(theme.ToolFg, theme.TextSecondary),
		Path:             firstColor(theme.LinkFg, theme.TextPrimary),
		Variable:         firstColor(theme.SecondaryText, theme.TextPrimary),
		Deleted:          firstColor(theme.DiffRemoveFg, theme.Error),
		Inserted:         firstColor(theme.DiffAddFg, theme.Success),
	}
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
