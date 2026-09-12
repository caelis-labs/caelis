package tuikit

import (
	"image/color"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/colorprofile"
)

const (
	CatppuccinMochaChromaTheme = "caelis-catppuccin-mocha"
	CatppuccinLatteChromaTheme = "caelis-catppuccin-latte"

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

// Community syntax is read from Chroma's pinned upstream styles. Shell tokens,
// Markdown and rich diffs therefore share one authoritative token mapping.
func semanticSyntaxPalette(theme Theme) SyntaxPalette {
	name := theme.Name
	switch name {
	case "solarized":
		name = "solarized-dark"
	case "nord", "dracula", "catppuccin-mocha", "catppuccin-latte":
	default:
		name = "catppuccin-mocha"
		if !theme.IsDark {
			name = "catppuccin-latte"
		}
	}
	style := communitySyntaxStyles[name]
	profile := theme.Profile
	if profile == colorprofile.Unknown {
		profile = colorprofile.TrueColor
	}
	c := func(token chroma.TokenType) color.Color {
		if theme.NoColor {
			return nil
		}
		value := style.Get(token).Colour
		if !value.IsSet() {
			return theme.TextPrimary
		}
		return profile.Convert(lipgloss.Color(value.String()))
	}
	return SyntaxPalette{
		ChromaTheme: style.Name, Text: c(chroma.Text), Background: theme.AppBg,
		InlineBackground: firstColor(theme.TranscriptPillBg, theme.ModalBg),
		Comment:          c(chroma.Comment), Keyword: c(chroma.Keyword), Function: c(chroma.NameFunction),
		String: c(chroma.LiteralString), Number: c(chroma.LiteralNumber), Operator: c(chroma.Operator),
		Path: theme.LinkFg, Variable: c(chroma.NameVariable),
		Deleted: c(chroma.GenericDeleted), Inserted: c(chroma.GenericInserted),
	}
}

func syntaxColor(profile colorprofile.Profile, rich, ansi256, ansi16 string) color.Color {
	return profileColor(profile, rich, ansi256, ansi16)
}
