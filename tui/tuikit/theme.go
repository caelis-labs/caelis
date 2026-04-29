package tuikit

import (
	"image/color"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

type Theme struct {
	Name    string
	IsDark  bool
	NoColor bool
	Profile colorprofile.Profile

	AppBg          color.Color
	PanelBorder    color.Color
	PanelTitle     color.Color
	TextPrimary    color.Color
	TextSecondary  color.Color
	SecondaryText  color.Color
	MutedText      color.Color
	Info           color.Color
	Success        color.Color
	Warning        color.Color
	Error          color.Color
	Accent         color.Color
	Focus          color.Color
	ModalBg        color.Color
	StatusBg       color.Color
	StatusText     color.Color
	CommandBg      color.Color
	CommandActive  color.Color
	CommandText    color.Color
	CommandSubText color.Color

	// Line-level semantic colors (conversation / tool / diff).
	AssistantFg        color.Color
	ReasoningFg        color.Color
	UserFg             color.Color
	UserBg             color.Color
	UserPrefixFg       color.Color
	UserMentionFg      color.Color
	ToolFg             color.Color
	DiffAddFg          color.Color
	DiffRemoveFg       color.Color
	DiffHeaderFg       color.Color
	DiffHunkFg         color.Color
	DiffAddBg          color.Color
	DiffAddStrongBg    color.Color
	DiffRemoveBg       color.Color
	DiffRemoveStrongBg color.Color
	DiffLineNoFg       color.Color
	DiffGutterFg       color.Color
	DiffPanelBorder    color.Color
	SectionFg          color.Color
	KeyLabelFg         color.Color
	NoteFg             color.Color

	// Input area
	PromptFg     color.Color
	CursorFg     color.Color
	ScrollHintFg color.Color

	// Inline layout
	InputBarBg          color.Color
	InputBarFg          color.Color
	ToolOutputBg        color.Color
	HelpHintFg          color.Color
	SpinnerFg           color.Color
	SeparatorFg         color.Color
	RoleBorderFg        color.Color // left border for role sections
	NewMsgBg            color.Color // "new messages" indicator
	ComposerBorder      color.Color
	ComposerBorderFocus color.Color
	ScrollbarTrack      color.Color
	ScrollbarThumb      color.Color
	LinkFg              color.Color
	CodeFg              color.Color
	CodeBg              color.Color
	CodeBlockFg         color.Color
	CodeBlockBg         color.Color
	TranscriptRail      color.Color
	TranscriptShell     color.Color
	TranscriptPillBg    color.Color
	CodeSurface         color.Color
	TableHeaderBg       color.Color
	TableBorder         color.Color

	// Resolved semantic tokens — lazily populated via Tokens().
	tokens *Tokens
}

// Tokens returns the resolved semantic design tokens for this theme.
// The result is cached after the first call.
func (t *Theme) Tokens() Tokens {
	if t.tokens != nil {
		return *t.tokens
	}
	tok := resolveTokens(*t)
	t.tokens = &tok
	return tok
}

// InvalidateTokens clears the cached tokens, forcing a re-resolve on the
// next Tokens() call. Call this after mutating theme colors (e.g. accent
// override, theme switch).
func (t *Theme) InvalidateTokens() {
	t.tokens = nil
}

func DefaultTheme() Theme {
	return ResolveThemeFromEnv()
}

func ResolveThemeFromEnv() Theme {
	return resolveTheme(themeResolveOptions{noColor: noColorRequested()})
}

func ResolveThemeForBackground(isDark bool) Theme {
	return resolveTheme(themeResolveOptions{
		backgroundKnown: true,
		backgroundDark:  isDark,
		noColor:         noColorRequested(),
	})
}

func ResolveThemeFromOptions(noColor bool, profile colorprofile.Profile) Theme {
	return resolveTheme(themeResolveOptions{
		noColor:           noColor,
		colorProfileKnown: profile != colorprofile.Unknown,
		colorProfile:      profile,
	})
}

func ResolveThemeWithState(isDark bool, noColor bool, profile colorprofile.Profile) Theme {
	return resolveTheme(themeResolveOptions{
		backgroundKnown:   true,
		backgroundDark:    isDark,
		noColor:           noColor,
		colorProfileKnown: profile != colorprofile.Unknown,
		colorProfile:      profile,
	})
}

func ThemeUsesAutoBackground() bool {
	name := strings.ToLower(strings.TrimSpace(os.Getenv("CAELIS_THEME")))
	return name == "" || name == "auto" || name == "default"
}

type themeResolveOptions struct {
	backgroundKnown   bool
	backgroundDark    bool
	colorProfileKnown bool
	colorProfile      colorprofile.Profile
	noColor           bool
}

func resolveTheme(opts themeResolveOptions) Theme {
	profile := resolvedColorProfile(opts)
	useTrueColor := profile == colorprofile.TrueColor
	name := strings.ToLower(strings.TrimSpace(os.Getenv("CAELIS_THEME")))
	theme := namedTheme(name, useTrueColor, resolvedDarkBackground(opts))
	theme.Profile = profile
	theme.NoColor = opts.noColor
	if accent := strings.TrimSpace(os.Getenv("CAELIS_ACCENT")); accent != "" {
		theme.Accent = lipgloss.Color(accent)
		theme.Focus = lipgloss.Color(accent)
		theme.ComposerBorderFocus = lipgloss.Color(accent)
		theme.LinkFg = lipgloss.Color(accent)
	}
	if opts.noColor {
		theme = stripThemeColors(theme)
	}
	return theme
}

func noColorRequested() bool {
	value, ok := os.LookupEnv("NO_COLOR")
	return ok && strings.TrimSpace(value) != ""
}

func resolvedColorProfile(opts themeResolveOptions) colorprofile.Profile {
	if opts.noColor {
		return colorprofile.NoTTY
	}
	if opts.colorProfileKnown && opts.colorProfile != colorprofile.Unknown {
		return opts.colorProfile
	}
	if supportsTrueColor() {
		return colorprofile.TrueColor
	}
	if supportsANSI256() {
		return colorprofile.ANSI256
	}
	return colorprofile.ANSI
}

func resolvedDarkBackground(opts themeResolveOptions) bool {
	if opts.backgroundKnown {
		return opts.backgroundDark
	}
	return lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
}

func supportsTrueColor() bool {
	colorterm := strings.ToLower(strings.TrimSpace(os.Getenv("COLORTERM")))
	if strings.Contains(colorterm, "truecolor") || strings.Contains(colorterm, "24bit") {
		return true
	}
	term := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
	return strings.Contains(term, "truecolor") || strings.Contains(term, "24bit") || strings.Contains(term, "direct")
}

func supportsANSI256() bool {
	term := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
	return strings.Contains(term, "256color")
}

func stripThemeColors(theme Theme) Theme {
	theme.NoColor = true
	theme.Profile = colorprofile.NoTTY
	theme.AppBg = nil
	theme.PanelBorder = nil
	theme.PanelTitle = nil
	theme.TextPrimary = nil
	theme.TextSecondary = nil
	theme.SecondaryText = nil
	theme.MutedText = nil
	theme.Info = nil
	theme.Success = nil
	theme.Warning = nil
	theme.Error = nil
	theme.Accent = nil
	theme.Focus = nil
	theme.ModalBg = nil
	theme.StatusBg = nil
	theme.StatusText = nil
	theme.CommandBg = nil
	theme.CommandActive = nil
	theme.CommandText = nil
	theme.CommandSubText = nil
	theme.AssistantFg = nil
	theme.ReasoningFg = nil
	theme.UserFg = nil
	theme.UserBg = nil
	theme.UserPrefixFg = nil
	theme.UserMentionFg = nil
	theme.ToolFg = nil
	theme.DiffAddFg = nil
	theme.DiffRemoveFg = nil
	theme.DiffHeaderFg = nil
	theme.DiffHunkFg = nil
	theme.DiffAddBg = nil
	theme.DiffAddStrongBg = nil
	theme.DiffRemoveBg = nil
	theme.DiffRemoveStrongBg = nil
	theme.DiffLineNoFg = nil
	theme.DiffGutterFg = nil
	theme.DiffPanelBorder = nil
	theme.SectionFg = nil
	theme.KeyLabelFg = nil
	theme.NoteFg = nil
	theme.PromptFg = nil
	theme.CursorFg = nil
	theme.ScrollHintFg = nil
	theme.InputBarBg = nil
	theme.InputBarFg = nil
	theme.ToolOutputBg = nil
	theme.HelpHintFg = nil
	theme.SpinnerFg = nil
	theme.SeparatorFg = nil
	theme.RoleBorderFg = nil
	theme.NewMsgBg = nil
	theme.ComposerBorder = nil
	theme.ComposerBorderFocus = nil
	theme.ScrollbarTrack = nil
	theme.ScrollbarThumb = nil
	theme.LinkFg = nil
	theme.CodeFg = nil
	theme.CodeBg = nil
	theme.CodeBlockFg = nil
	theme.CodeBlockBg = nil
	theme.TranscriptRail = nil
	theme.TranscriptShell = nil
	theme.TranscriptPillBg = nil
	theme.CodeSurface = nil
	theme.TableHeaderBg = nil
	theme.TableBorder = nil
	return theme
}

func namedTheme(name string, trueColor bool, darkBackground bool) Theme {
	switch name {
	case "", "auto", "default":
		if darkBackground {
			return defaultThemeVariant(trueColor)
		}
		return defaultLightThemeVariant(trueColor)
	case "dark":
		return defaultThemeVariant(trueColor)
	case "light":
		return defaultLightThemeVariant(trueColor)
	case "nord":
		return nordTheme(trueColor)
	case "solarized":
		return solarizedTheme(trueColor)
	case "dracula":
		return draculaTheme(trueColor)
	default:
		if darkBackground {
			return defaultThemeVariant(trueColor)
		}
		return defaultLightThemeVariant(trueColor)
	}
}

func themeColor(trueColor bool, rich string, fallback string) color.Color {
	if trueColor || fallback == "" {
		return lipgloss.Color(rich)
	}
	return lipgloss.Color(fallback)
}

func defaultThemeVariant(trueColor bool) Theme {
	return Theme{
		Name:           "dark",
		IsDark:         true,
		AppBg:          themeColor(trueColor, "#0f1117", "233"),
		PanelBorder:    themeColor(trueColor, "#333b49", "240"),
		PanelTitle:     themeColor(trueColor, "#f4f7fb", "255"),
		TextPrimary:    themeColor(trueColor, "#e8edf4", "255"),
		TextSecondary:  themeColor(trueColor, "#a8b3c5", "248"),
		SecondaryText:  themeColor(trueColor, "#c6d0df", "250"),
		MutedText:      themeColor(trueColor, "#7a8599", "245"),
		Info:           themeColor(trueColor, "#7dd3fc", "117"),
		Success:        themeColor(trueColor, "#5ee787", "78"),
		Warning:        themeColor(trueColor, "#f4bf4f", "221"),
		Error:          themeColor(trueColor, "#ff6b6b", "203"),
		Accent:         themeColor(trueColor, "#7aa2f7", "111"),
		Focus:          themeColor(trueColor, "#8bd5ff", "117"),
		ModalBg:        themeColor(trueColor, "#151922", "234"),
		StatusBg:       themeColor(trueColor, "#11141b", "233"),
		StatusText:     themeColor(trueColor, "#d7deea", "252"),
		CommandBg:      themeColor(trueColor, "#11141b", "233"),
		CommandActive:  themeColor(trueColor, "#20283a", "236"),
		CommandText:    themeColor(trueColor, "#f4f7fb", "255"),
		CommandSubText: themeColor(trueColor, "#9aa6ba", "247"),

		AssistantFg:        themeColor(trueColor, "#9ece6a", "114"),
		ReasoningFg:        themeColor(trueColor, "#7f8ba3", "245"),
		UserFg:             themeColor(trueColor, "#f4f7fb", "255"),
		UserBg:             themeColor(trueColor, "#151922", "234"),
		UserPrefixFg:       themeColor(trueColor, "#ffffff", "255"),
		UserMentionFg:      themeColor(trueColor, "#8bd5ff", "117"),
		ToolFg:             themeColor(trueColor, "#8bd5ff", "117"),
		DiffAddFg:          themeColor(trueColor, "#5ee787", "78"),
		DiffRemoveFg:       themeColor(trueColor, "#ff7b72", "210"),
		DiffHeaderFg:       themeColor(trueColor, "#a8b3c5", "248"),
		DiffHunkFg:         themeColor(trueColor, "#c6d0df", "250"),
		DiffAddBg:          themeColor(trueColor, "#173423", "22"),
		DiffAddStrongBg:    themeColor(trueColor, "#225f37", "29"),
		DiffRemoveBg:       themeColor(trueColor, "#3a2028", "52"),
		DiffRemoveStrongBg: themeColor(trueColor, "#762d39", "88"),
		DiffLineNoFg:       themeColor(trueColor, "#748097", "245"),
		DiffGutterFg:       themeColor(trueColor, "#9aa6ba", "247"),
		DiffPanelBorder:    themeColor(trueColor, "#394352", "240"),
		SectionFg:          themeColor(trueColor, "#f4f7fb", "255"),
		KeyLabelFg:         themeColor(trueColor, "#c6d0df", "250"),
		NoteFg:             themeColor(trueColor, "#8f9aad", "246"),
		PromptFg:           themeColor(trueColor, "#8bd5ff", "117"),
		CursorFg:           themeColor(trueColor, "#ffffff", "255"),
		ScrollHintFg:       themeColor(trueColor, "#f4bf4f", "221"),

		InputBarBg:          themeColor(trueColor, "#0f1117", "233"),
		InputBarFg:          themeColor(trueColor, "#e8edf4", "255"),
		ToolOutputBg:        themeColor(trueColor, "#151922", "234"),
		HelpHintFg:          themeColor(trueColor, "#8f9aad", "246"),
		SpinnerFg:           themeColor(trueColor, "#8bd5ff", "117"),
		SeparatorFg:         themeColor(trueColor, "#293241", "238"),
		RoleBorderFg:        themeColor(trueColor, "#333b49", "240"),
		NewMsgBg:            themeColor(trueColor, "#1d2737", "236"),
		ComposerBorder:      themeColor(trueColor, "#333b49", "240"),
		ComposerBorderFocus: themeColor(trueColor, "#8bd5ff", "117"),
		ScrollbarTrack:      themeColor(trueColor, "#1a202b", "234"),
		ScrollbarThumb:      themeColor(trueColor, "#7a8599", "245"),
		LinkFg:              themeColor(trueColor, "#8bd5ff", "117"),
		CodeFg:              themeColor(trueColor, "#f4bf4f", "221"),
		CodeBg:              themeColor(trueColor, "#20283a", "236"),
		CodeBlockFg:         themeColor(trueColor, "#d7deea", "252"),
		CodeBlockBg:         themeColor(trueColor, "#171c26", "234"),
		TranscriptRail:      themeColor(trueColor, "#465268", "240"),
		TranscriptShell:     themeColor(trueColor, "#293241", "238"),
		TranscriptPillBg:    themeColor(trueColor, "#20283a", "236"),
		CodeSurface:         themeColor(trueColor, "#171c26", "234"),
		TableHeaderBg:       themeColor(trueColor, "#20283a", "236"),
		TableBorder:         themeColor(trueColor, "#59657a", "242"),
	}
}

func defaultLightThemeVariant(trueColor bool) Theme {
	return Theme{
		Name:           "light",
		IsDark:         false,
		AppBg:          themeColor(trueColor, "#fbfcfe", "255"),
		PanelBorder:    themeColor(trueColor, "#c8d2df", "252"),
		PanelTitle:     themeColor(trueColor, "#111827", "235"),
		TextPrimary:    themeColor(trueColor, "#1f2937", "236"),
		TextSecondary:  themeColor(trueColor, "#526071", "240"),
		SecondaryText:  themeColor(trueColor, "#364152", "239"),
		MutedText:      themeColor(trueColor, "#748094", "243"),
		Info:           themeColor(trueColor, "#2563eb", "25"),
		Success:        themeColor(trueColor, "#188a42", "28"),
		Warning:        themeColor(trueColor, "#b86b00", "130"),
		Error:          themeColor(trueColor, "#c2410c", "166"),
		Accent:         themeColor(trueColor, "#2563eb", "25"),
		Focus:          themeColor(trueColor, "#0284c7", "32"),
		ModalBg:        themeColor(trueColor, "#ffffff", "231"),
		StatusBg:       themeColor(trueColor, "#f3f6fb", "255"),
		StatusText:     themeColor(trueColor, "#1f2937", "236"),
		CommandBg:      themeColor(trueColor, "#ffffff", "231"),
		CommandActive:  themeColor(trueColor, "#e7f0ff", "195"),
		CommandText:    themeColor(trueColor, "#111827", "235"),
		CommandSubText: themeColor(trueColor, "#526071", "240"),

		AssistantFg:        themeColor(trueColor, "#188a42", "28"),
		ReasoningFg:        themeColor(trueColor, "#6b7280", "242"),
		UserFg:             themeColor(trueColor, "#111827", "235"),
		UserBg:             themeColor(trueColor, "#ffffff", "231"),
		UserPrefixFg:       themeColor(trueColor, "#0f172a", "235"),
		UserMentionFg:      themeColor(trueColor, "#0f766e", "30"),
		ToolFg:             themeColor(trueColor, "#0f766e", "30"),
		DiffAddFg:          themeColor(trueColor, "#188a42", "28"),
		DiffRemoveFg:       themeColor(trueColor, "#c2410c", "166"),
		DiffHeaderFg:       themeColor(trueColor, "#526071", "240"),
		DiffHunkFg:         themeColor(trueColor, "#1f2937", "236"),
		DiffAddBg:          themeColor(trueColor, "#e8f7ed", "194"),
		DiffAddStrongBg:    themeColor(trueColor, "#c7ead2", "151"),
		DiffRemoveBg:       themeColor(trueColor, "#fff1ed", "224"),
		DiffRemoveStrongBg: themeColor(trueColor, "#ffd8cc", "216"),
		DiffLineNoFg:       themeColor(trueColor, "#748094", "243"),
		DiffGutterFg:       themeColor(trueColor, "#64748b", "243"),
		DiffPanelBorder:    themeColor(trueColor, "#c8d2df", "252"),
		SectionFg:          themeColor(trueColor, "#111827", "235"),
		KeyLabelFg:         themeColor(trueColor, "#364152", "239"),
		NoteFg:             themeColor(trueColor, "#6b7280", "242"),
		PromptFg:           themeColor(trueColor, "#0284c7", "32"),
		CursorFg:           themeColor(trueColor, "#111827", "235"),
		ScrollHintFg:       themeColor(trueColor, "#b86b00", "130"),

		InputBarBg:          themeColor(trueColor, "#ffffff", "231"),
		InputBarFg:          themeColor(trueColor, "#1f2937", "236"),
		ToolOutputBg:        themeColor(trueColor, "#f8fafc", "255"),
		HelpHintFg:          themeColor(trueColor, "#748094", "243"),
		SpinnerFg:           themeColor(trueColor, "#0284c7", "32"),
		SeparatorFg:         themeColor(trueColor, "#d7dee8", "252"),
		RoleBorderFg:        themeColor(trueColor, "#c8d2df", "252"),
		NewMsgBg:            themeColor(trueColor, "#e7f0ff", "195"),
		ComposerBorder:      themeColor(trueColor, "#c8d2df", "252"),
		ComposerBorderFocus: themeColor(trueColor, "#0284c7", "32"),
		ScrollbarTrack:      themeColor(trueColor, "#e2e8f0", "254"),
		ScrollbarThumb:      themeColor(trueColor, "#8a98ab", "245"),
		LinkFg:              themeColor(trueColor, "#2563eb", "25"),
		CodeFg:              themeColor(trueColor, "#9a3412", "130"),
		CodeBg:              themeColor(trueColor, "#fff7ed", "230"),
		CodeBlockFg:         themeColor(trueColor, "#263241", "236"),
		CodeBlockBg:         themeColor(trueColor, "#f3f6fb", "255"),
		TranscriptRail:      themeColor(trueColor, "#b7c3d2", "250"),
		TranscriptShell:     themeColor(trueColor, "#c8d2df", "252"),
		TranscriptPillBg:    themeColor(trueColor, "#e7eef7", "254"),
		CodeSurface:         themeColor(trueColor, "#f3f6fb", "255"),
		TableHeaderBg:       themeColor(trueColor, "#eef4fb", "254"),
		TableBorder:         themeColor(trueColor, "#a7b3c4", "249"),
	}
}

func nordTheme(trueColor bool) Theme {
	theme := defaultThemeVariant(trueColor)
	theme.Name = "nord"
	theme.AppBg = themeColor(trueColor, "#2e3440", "236")
	theme.PanelBorder = themeColor(trueColor, "#4c566a", "240")
	theme.PanelTitle = themeColor(trueColor, "#eceff4", "255")
	theme.TextPrimary = themeColor(trueColor, "#eceff4", "255")
	theme.TextSecondary = themeColor(trueColor, "#d8dee9", "252")
	theme.SecondaryText = themeColor(trueColor, "#d8dee9", "252")
	theme.MutedText = themeColor(trueColor, "#a7b0c0", "248")
	theme.Info = themeColor(trueColor, "#d8dee9", "252")
	theme.Success = themeColor(trueColor, "#a3be8c", "108")
	theme.Warning = themeColor(trueColor, "#ebcb8b", "223")
	theme.Error = themeColor(trueColor, "#bf616a", "131")
	theme.Accent = themeColor(trueColor, "#88c0d0", "110")
	theme.Focus = themeColor(trueColor, "#81a1c1", "110")
	theme.ModalBg = themeColor(trueColor, "#3b4252", "237")
	theme.StatusBg = themeColor(trueColor, "#2e3440", "236")
	theme.StatusText = themeColor(trueColor, "#d8dee9", "252")
	theme.AssistantFg = themeColor(trueColor, "#a3be8c", "108")
	theme.ReasoningFg = themeColor(trueColor, "#81a1c1", "110")
	theme.ToolFg = themeColor(trueColor, "#88c0d0", "110")
	theme.DiffAddBg = themeColor(trueColor, "#314236", "23")
	theme.DiffAddStrongBg = themeColor(trueColor, "#45604e", "59")
	theme.DiffRemoveBg = themeColor(trueColor, "#4a3037", "52")
	theme.DiffRemoveStrongBg = themeColor(trueColor, "#6a3f4a", "95")
	theme.ComposerBorder = themeColor(trueColor, "#4c566a", "240")
	theme.ComposerBorderFocus = themeColor(trueColor, "#81a1c1", "110")
	theme.ScrollbarTrack = themeColor(trueColor, "#3b4252", "237")
	theme.ScrollbarThumb = themeColor(trueColor, "#81a1c1", "110")
	theme.LinkFg = themeColor(trueColor, "#88c0d0", "110")
	theme.CodeBg = themeColor(trueColor, "#3b4252", "237")
	theme.CodeBlockBg = themeColor(trueColor, "#2b303b", "236")
	theme.TranscriptRail = themeColor(trueColor, "#81a1c1", "110")
	theme.TranscriptShell = themeColor(trueColor, "#434c5e", "239")
	theme.TranscriptPillBg = themeColor(trueColor, "#3b4252", "237")
	theme.CodeSurface = themeColor(trueColor, "#343b48", "237")
	theme.TableHeaderBg = themeColor(trueColor, "#3b4252", "237")
	theme.TableBorder = themeColor(trueColor, "#81a1c1", "110")
	return theme
}

func solarizedTheme(trueColor bool) Theme {
	theme := defaultThemeVariant(trueColor)
	theme.Name = "solarized"
	theme.AppBg = themeColor(trueColor, "#002b36", "235")
	theme.PanelBorder = themeColor(trueColor, "#586e75", "242")
	theme.PanelTitle = themeColor(trueColor, "#fdf6e3", "230")
	theme.TextPrimary = themeColor(trueColor, "#eee8d5", "254")
	theme.TextSecondary = themeColor(trueColor, "#93a1a1", "245")
	theme.SecondaryText = themeColor(trueColor, "#b7c0bc", "250")
	theme.MutedText = themeColor(trueColor, "#839496", "244")
	theme.Info = themeColor(trueColor, "#93a1a1", "245")
	theme.Success = themeColor(trueColor, "#859900", "100")
	theme.Warning = themeColor(trueColor, "#b58900", "136")
	theme.Error = themeColor(trueColor, "#dc322f", "160")
	theme.Accent = themeColor(trueColor, "#2aa198", "36")
	theme.Focus = themeColor(trueColor, "#268bd2", "32")
	theme.ModalBg = themeColor(trueColor, "#073642", "236")
	theme.StatusBg = themeColor(trueColor, "#002b36", "235")
	theme.StatusText = themeColor(trueColor, "#93a1a1", "245")
	theme.AssistantFg = themeColor(trueColor, "#859900", "100")
	theme.ReasoningFg = themeColor(trueColor, "#6c71c4", "61")
	theme.ToolFg = themeColor(trueColor, "#2aa198", "36")
	theme.DiffAddBg = themeColor(trueColor, "#173d1c", "22")
	theme.DiffAddStrongBg = themeColor(trueColor, "#2f5f2f", "29")
	theme.DiffRemoveBg = themeColor(trueColor, "#4a1f1c", "52")
	theme.DiffRemoveStrongBg = themeColor(trueColor, "#7a2d24", "88")
	theme.ComposerBorder = themeColor(trueColor, "#586e75", "242")
	theme.ComposerBorderFocus = themeColor(trueColor, "#268bd2", "32")
	theme.ScrollbarTrack = themeColor(trueColor, "#073642", "236")
	theme.ScrollbarThumb = themeColor(trueColor, "#586e75", "242")
	theme.LinkFg = themeColor(trueColor, "#268bd2", "32")
	theme.CodeFg = themeColor(trueColor, "#cb4b16", "166")
	theme.CodeBg = themeColor(trueColor, "#073642", "236")
	theme.CodeBlockBg = themeColor(trueColor, "#062f3a", "236")
	theme.TranscriptRail = themeColor(trueColor, "#2aa198", "36")
	theme.TranscriptShell = themeColor(trueColor, "#31545e", "238")
	theme.TranscriptPillBg = themeColor(trueColor, "#073642", "236")
	theme.CodeSurface = themeColor(trueColor, "#073642", "236")
	theme.TableHeaderBg = themeColor(trueColor, "#073642", "236")
	theme.TableBorder = themeColor(trueColor, "#2aa198", "36")
	return theme
}

func draculaTheme(trueColor bool) Theme {
	theme := defaultThemeVariant(trueColor)
	theme.Name = "dracula"
	theme.AppBg = themeColor(trueColor, "#282a36", "236")
	theme.PanelBorder = themeColor(trueColor, "#6272a4", "61")
	theme.PanelTitle = themeColor(trueColor, "#f8f8f2", "255")
	theme.TextPrimary = themeColor(trueColor, "#f8f8f2", "255")
	theme.TextSecondary = themeColor(trueColor, "#bd93f9", "141")
	theme.SecondaryText = themeColor(trueColor, "#d7c2ff", "183")
	theme.MutedText = themeColor(trueColor, "#9580c2", "104")
	theme.Info = themeColor(trueColor, "#8be9fd", "123")
	theme.Success = themeColor(trueColor, "#50fa7b", "84")
	theme.Warning = themeColor(trueColor, "#ffb86c", "215")
	theme.Error = themeColor(trueColor, "#ff5555", "203")
	theme.Accent = themeColor(trueColor, "#ff79c6", "212")
	theme.Focus = themeColor(trueColor, "#8be9fd", "123")
	theme.ModalBg = themeColor(trueColor, "#1f2130", "235")
	theme.StatusBg = themeColor(trueColor, "#282a36", "236")
	theme.StatusText = themeColor(trueColor, "#f8f8f2", "255")
	theme.AssistantFg = themeColor(trueColor, "#50fa7b", "84")
	theme.ReasoningFg = themeColor(trueColor, "#bd93f9", "141")
	theme.ToolFg = themeColor(trueColor, "#8be9fd", "123")
	theme.DiffAddBg = themeColor(trueColor, "#21392a", "22")
	theme.DiffAddStrongBg = themeColor(trueColor, "#2f5f43", "29")
	theme.DiffRemoveBg = themeColor(trueColor, "#4a232d", "52")
	theme.DiffRemoveStrongBg = themeColor(trueColor, "#7d3243", "89")
	theme.ComposerBorder = themeColor(trueColor, "#6272a4", "61")
	theme.ComposerBorderFocus = themeColor(trueColor, "#ff79c6", "212")
	theme.ScrollbarTrack = themeColor(trueColor, "#1f2130", "235")
	theme.ScrollbarThumb = themeColor(trueColor, "#6272a4", "61")
	theme.LinkFg = themeColor(trueColor, "#8be9fd", "123")
	theme.CodeBg = themeColor(trueColor, "#343746", "237")
	theme.CodeBlockBg = themeColor(trueColor, "#21222c", "235")
	theme.TranscriptRail = themeColor(trueColor, "#8be9fd", "123")
	theme.TranscriptShell = themeColor(trueColor, "#44475a", "239")
	theme.TranscriptPillBg = themeColor(trueColor, "#343746", "237")
	theme.CodeSurface = themeColor(trueColor, "#2b2d39", "236")
	theme.TableHeaderBg = themeColor(trueColor, "#343746", "237")
	theme.TableBorder = themeColor(trueColor, "#6272a4", "61")
	return theme
}

func (t Theme) FrameStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(t.PanelBorder).
		Foreground(t.TextPrimary).
		Padding(0, 1)
}

func (t Theme) StatusStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(t.StatusText).
		Padding(0, StatusInset)
}

func (t Theme) HintStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.TextSecondary)
}

func (t Theme) SecondaryTextStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.SecondaryText)
}

func (t Theme) MutedTextStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.MutedText)
}

func (t Theme) HintRowStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(t.TextSecondary).
		Padding(0, StatusInset)
}

func (t Theme) TextStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.TextPrimary)
}

func (t Theme) TitleStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Bold(true).
		Foreground(t.PanelTitle)
}

func (t Theme) ModalStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(t.ModalBg).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(t.Focus).
		Padding(1, 2)
}

func (t Theme) CommandActiveStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(t.CommandText).
		Bold(true).
		Underline(true).
		Padding(0, 1)
}

func (t Theme) CommandStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(t.CommandText).
		Padding(0, 1)
}

// ---------------------------------------------------------------------------
// Line-style rendering helpers
// ---------------------------------------------------------------------------

// AssistantStyle renders assistant text (green prefix).
func (t Theme) AssistantStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.AssistantFg)
}

// ReasoningStyle renders reasoning/thinking text (dimmed + italic).
func (t Theme) ReasoningStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.ReasoningFg).Italic(true)
}

// ToolStyle renders tool call/result prefixes.
func (t Theme) ToolStyle() lipgloss.Style {
	return t.Tokens().ToolIcon
}

// ToolNameStyle renders tool names.
func (t Theme) ToolNameStyle() lipgloss.Style {
	return t.Tokens().ToolName
}

func (t Theme) ToolArgsStyle() lipgloss.Style {
	return t.Tokens().ToolArgs
}

func (t Theme) ToolResultStyle() lipgloss.Style {
	return t.Tokens().ToolResult
}

func (t Theme) ToolErrorStyle() lipgloss.Style {
	return t.Tokens().ToolError
}

func (t Theme) ToolOutputStyle() lipgloss.Style {
	return t.Tokens().ToolOutput
}

// UserStyle renders user messages in a subtle chat bubble-like background.
func (t Theme) UserStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.UserFg).Bold(true)
}

// UserPrefixStyle renders the leading "> " marker for user messages.
func (t Theme) UserPrefixStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.UserPrefixFg).Bold(true)
}

// UserMentionStyle renders @path mentions inside user messages.
func (t Theme) UserMentionStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.UserMentionFg).Bold(true)
}

// DiffAddStyle renders added lines in diffs (green).
func (t Theme) DiffAddStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.DiffAddFg)
}

// DiffRemoveStyle renders removed lines in diffs (red).
func (t Theme) DiffRemoveStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.DiffRemoveFg)
}

// DiffHeaderStyle renders diff headers (dimmed + bold).
func (t Theme) DiffHeaderStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.DiffHeaderFg).Bold(true)
}

// DiffHunkStyle renders diff hunk headers (@@ ... @@) in blue.
func (t Theme) DiffHunkStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.DiffHunkFg).Bold(true)
}

// DiffLineNoStyle renders diff line numbers.
func (t Theme) DiffLineNoStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.DiffLineNoFg)
}

// DiffGutterStyle renders diff markers/gutters.
func (t Theme) DiffGutterStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.DiffGutterFg)
}

// DiffPanelBorderStyle renders split-view separator lines.
func (t Theme) DiffPanelBorderStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.DiffPanelBorder)
}

// WarnStyle renders warning text (yellow).
func (t Theme) WarnStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.Warning)
}

// ErrorStyle renders error text (red).
func (t Theme) ErrorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.Error)
}

// NoteStyle renders note text (dimmed).
func (t Theme) NoteStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.NoteFg)
}

func (t Theme) TranscriptRailStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.TranscriptRail)
}

func (t Theme) TranscriptShellStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.TranscriptShell)
}

func (t Theme) TranscriptMetaStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.MutedText)
}

func (t Theme) TranscriptLabelStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.SecondaryText).Bold(true)
}

func (t Theme) TranscriptPillStyle(tone string) lipgloss.Style {
	style := lipgloss.NewStyle().
		Foreground(t.SecondaryText).
		Bold(true)
	switch strings.ToLower(strings.TrimSpace(tone)) {
	case "success":
		return style.Foreground(t.Success)
	case "warning":
		return style.Foreground(t.Warning)
	case "error":
		return style.Foreground(t.Error)
	case "accent":
		return style.Foreground(t.Accent)
	default:
		return style.Foreground(t.SecondaryText)
	}
}

func (t Theme) CodeSurfaceStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(t.CodeBlockFg).
		Background(t.CodeSurface)
}

func (t Theme) TableHeaderStyle() lipgloss.Style {
	return t.MarkdownTableHeaderStyle()
}

func (t Theme) TableBorderStyle() lipgloss.Style {
	return t.MarkdownTableBorderStyle()
}

func (t Theme) MarkdownHeadingStyle() lipgloss.Style {
	return t.Tokens().MarkdownHeading
}

func (t Theme) MarkdownLinkStyle() lipgloss.Style {
	return t.Tokens().MarkdownLink
}

func (t Theme) MarkdownInlineCodeStyle() lipgloss.Style {
	return t.Tokens().MarkdownInlineCode
}

func (t Theme) MarkdownCodeBlockStyle() lipgloss.Style {
	return t.Tokens().MarkdownCodeBlock
}

func (t Theme) MarkdownQuoteStyle() lipgloss.Style {
	return t.Tokens().MarkdownQuote
}

func (t Theme) MarkdownTableHeaderStyle() lipgloss.Style {
	return t.Tokens().MarkdownTableHead
}

func (t Theme) MarkdownTableBorderStyle() lipgloss.Style {
	return t.Tokens().MarkdownTableEdge
}

func (t Theme) MarkdownRuleStyle() lipgloss.Style {
	return t.Tokens().MarkdownRule
}

// LogBlockStyle renders log/tool output lines with a subtle left border
// to visually separate them from narrative assistant text.
func (t Theme) LogBlockStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(t.TextSecondary).
		PaddingLeft(1)
}

// SectionStyle renders section headers (bold).
func (t Theme) SectionStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.SectionFg).Bold(true)
}

// KeyLabelStyle renders key labels in key-value pairs.
func (t Theme) KeyLabelStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.KeyLabelFg)
}

// PromptStyle renders the input prompt marker.
func (t Theme) PromptStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.PromptFg).Bold(true)
}

// ScrollHintIndicator renders scroll hint text.
func (t Theme) ScrollHintStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.ScrollHintFg)
}

func ComposeFooter(width int, left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if width <= 0 {
		return ""
	}
	if left == "" && right == "" {
		return strings.Repeat(" ", width)
	}
	if left == "" {
		if len(right) >= width {
			return right[len(right)-width:]
		}
		return strings.Repeat(" ", width-len(right)) + right
	}
	if right == "" {
		if len(left) >= width {
			return left[:width]
		}
		return left + strings.Repeat(" ", width-len(left))
	}
	if len(left)+len(right)+1 <= width {
		return left + strings.Repeat(" ", width-len(left)-len(right)) + right
	}
	maxLeft := width - len(right) - 1
	if maxLeft < 0 {
		maxLeft = 0
	}
	if len(left) > maxLeft {
		left = left[:maxLeft]
	}
	gap := width - len(left) - len(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}

// ---------------------------------------------------------------------------
// Inline layout styles
// ---------------------------------------------------------------------------

// InputBarStyle renders the input bar background.
func (t Theme) InputBarStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(t.InputBarFg).
		Padding(0, 0)
}

func (t Theme) ComposerStyle(focused bool) lipgloss.Style {
	style := lipgloss.NewStyle().
		Foreground(t.InputBarFg).
		BorderStyle(lipgloss.NormalBorder()).
		BorderLeft(true).
		BorderForeground(t.ComposerBorder)
	if focused {
		return style.BorderForeground(t.ComposerBorderFocus).PaddingLeft(1)
	}
	return style.PaddingLeft(0)
}

// HelpHintTextStyle renders help hint text (dimmed shortcut labels).
func (t Theme) HelpHintTextStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.HelpHintFg)
}

// SpinnerStyle renders the spinner indicator.
func (t Theme) SpinnerStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.SpinnerFg)
}

// SeparatorStyle renders horizontal separators.
func (t Theme) SeparatorStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.SeparatorFg)
}

// NewMsgIndicatorStyle renders the "new messages" indicator.
func (t Theme) NewMsgIndicatorStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(t.Warning).
		Bold(true).
		Padding(0, 1)
}

func (t Theme) ScrollbarTrackStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.ScrollbarTrack)
}

func (t Theme) ScrollbarThumbStyle() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(t.ScrollbarThumb)
}

func (t Theme) LinkStyle() lipgloss.Style {
	return t.MarkdownLinkStyle()
}
