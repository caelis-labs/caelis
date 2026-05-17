package tuikit

import (
	"image/color"
	"math"
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
	SelectionFg    color.Color
	SelectionBg    color.Color

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

type ThemeIssue struct {
	Field   string
	Message string
}

func ValidateTheme(theme Theme) []ThemeIssue {
	if theme.NoColor {
		return nil
	}
	bg := validationBackground(theme)
	checks := []struct {
		field     string
		fg        color.Color
		bg        color.Color
		threshold float64
	}{
		{field: "TextPrimary", fg: theme.TextPrimary, bg: bg, threshold: 4.5},
		{field: "TextSecondary", fg: firstColor(theme.TextSecondary, theme.SecondaryText), bg: bg, threshold: 3.0},
		{field: "MutedText", fg: theme.MutedText, bg: bg, threshold: 3.0},
		{field: "Warning", fg: theme.Warning, bg: bg, threshold: 3.0},
		{field: "Error", fg: theme.Error, bg: bg, threshold: 3.0},
		{field: "Success", fg: theme.Success, bg: bg, threshold: 3.0},
		{field: "DiffAddFg", fg: theme.DiffAddFg, bg: firstColor(theme.DiffAddBg, bg), threshold: 3.0},
		{field: "DiffRemoveFg", fg: theme.DiffRemoveFg, bg: firstColor(theme.DiffRemoveBg, bg), threshold: 3.0},
		{field: "SelectionFg", fg: theme.SelectionFg, bg: theme.SelectionBg, threshold: 4.5},
	}
	var issues []ThemeIssue
	for _, check := range checks {
		if check.fg == nil || check.bg == nil {
			continue
		}
		if ratio := contrastRatio(check.fg, check.bg); ratio < check.threshold {
			issues = append(issues, ThemeIssue{
				Field:   check.field,
				Message: "contrast below threshold",
			})
		}
	}
	return issues
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

func ResolveThemeWithBackgroundColor(background color.Color, noColor bool, profile colorprofile.Profile) Theme {
	return resolveTheme(themeResolveOptions{
		backgroundKnown:      background != nil,
		backgroundDark:       colorIsDark(background),
		backgroundColorKnown: background != nil,
		backgroundColor:      background,
		noColor:              noColor,
		colorProfileKnown:    profile != colorprofile.Unknown,
		colorProfile:         profile,
	})
}

func ThemeUsesAutoBackground() bool {
	name := strings.ToLower(strings.TrimSpace(os.Getenv("CAELIS_THEME")))
	return name == "" || name == "auto" || name == "default"
}

type themeResolveOptions struct {
	backgroundKnown      bool
	backgroundDark       bool
	backgroundColorKnown bool
	backgroundColor      color.Color
	colorProfileKnown    bool
	colorProfile         colorprofile.Profile
	noColor              bool
}

func resolveTheme(opts themeResolveOptions) Theme {
	profile := resolvedColorProfile(opts)
	name := strings.ToLower(strings.TrimSpace(os.Getenv("CAELIS_THEME")))
	theme := namedTheme(name, profile, resolvedDarkBackground(opts), resolvedBackgroundColor(opts))
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

func resolvedBackgroundColor(opts themeResolveOptions) color.Color {
	if opts.backgroundColorKnown {
		return opts.backgroundColor
	}
	return nil
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
	theme.SelectionFg = nil
	theme.SelectionBg = nil
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

func namedTheme(name string, profile colorprofile.Profile, darkBackground bool, background color.Color) Theme {
	trueColor := profile == colorprofile.TrueColor
	switch name {
	case "", "auto", "default":
		return defaultAdaptiveThemeVariant(profile, darkBackground, background)
	case "dark":
		return defaultAdaptiveThemeVariant(profile, true, background)
	case "light":
		return defaultAdaptiveThemeVariant(profile, false, background)
	case "nord":
		return stripThemeBackgroundsForANSI(nordTheme(trueColor), profile)
	case "solarized":
		return stripThemeBackgroundsForANSI(solarizedTheme(trueColor), profile)
	case "dracula":
		return stripThemeBackgroundsForANSI(draculaTheme(trueColor), profile)
	default:
		return defaultAdaptiveThemeVariant(profile, darkBackground, background)
	}
}

func themeColor(trueColor bool, rich string, fallback string) color.Color {
	if trueColor || fallback == "" {
		return lipgloss.Color(rich)
	}
	return lipgloss.Color(fallback)
}

func profileColor(profile colorprofile.Profile, rich string, ansi256 string, ansi16 string) color.Color {
	switch profile {
	case colorprofile.TrueColor:
		if rich == "" {
			return nil
		}
		return lipgloss.Color(rich)
	case colorprofile.ANSI256:
		if ansi256 == "" {
			return nil
		}
		return lipgloss.Color(ansi256)
	case colorprofile.ANSI:
		if ansi16 == "" {
			return nil
		}
		return lipgloss.Color(ansi16)
	default:
		return nil
	}
}

func adaptiveBackgroundColor(profile colorprofile.Profile, terminal color.Color, dark bool, darkAlpha, lightAlpha float64, darkFallback, lightFallback, dark256, light256 string) color.Color {
	if profile == colorprofile.TrueColor {
		if r, g, b, ok := rgb8(terminal); ok {
			top := [3]uint8{0, 0, 0}
			alpha := lightAlpha
			if dark {
				top = [3]uint8{255, 255, 255}
				alpha = darkAlpha
			}
			return lipgloss.Color(hexColor(blendRGB([3]uint8{r, g, b}, top, alpha)))
		}
		if dark {
			return lipgloss.Color(darkFallback)
		}
		return lipgloss.Color(lightFallback)
	}
	if profile == colorprofile.ANSI256 {
		if dark {
			return lipgloss.Color(dark256)
		}
		return lipgloss.Color(light256)
	}
	return nil
}

func adaptiveTintColor(profile colorprofile.Profile, terminal color.Color, dark bool, darkTop, lightTop [3]uint8, darkAlpha, lightAlpha float64, darkFallback, lightFallback, dark256, light256 string) color.Color {
	if profile == colorprofile.TrueColor {
		if r, g, b, ok := rgb8(terminal); ok {
			top := lightTop
			alpha := lightAlpha
			if dark {
				top = darkTop
				alpha = darkAlpha
			}
			return lipgloss.Color(hexColor(blendRGB([3]uint8{r, g, b}, top, alpha)))
		}
		if dark {
			return lipgloss.Color(darkFallback)
		}
		return lipgloss.Color(lightFallback)
	}
	if profile == colorprofile.ANSI256 {
		if dark {
			return lipgloss.Color(dark256)
		}
		return lipgloss.Color(light256)
	}
	return nil
}

func blendRGB(base [3]uint8, top [3]uint8, alpha float64) [3]uint8 {
	return [3]uint8{
		blendChannel(base[0], top[0], alpha),
		blendChannel(base[1], top[1], alpha),
		blendChannel(base[2], top[2], alpha),
	}
}

func blendChannel(base uint8, top uint8, alpha float64) uint8 {
	value := (float64(top) * alpha) + (float64(base) * (1 - alpha))
	return uint8(math.Round(value))
}

func hexColor(rgb [3]uint8) string {
	const hex = "0123456789abcdef"
	out := []byte{'#', '0', '0', '0', '0', '0', '0'}
	for i, c := range rgb {
		out[1+i*2] = hex[c>>4]
		out[2+i*2] = hex[c&0x0f]
	}
	return string(out)
}

func rgb8(c color.Color) (uint8, uint8, uint8, bool) {
	if c == nil {
		return 0, 0, 0, false
	}
	r, g, b, _ := c.RGBA()
	return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8), true
}

func validationBackground(theme Theme) color.Color {
	if theme.AppBg != nil {
		return theme.AppBg
	}
	if theme.IsDark {
		return color.RGBA{A: 255}
	}
	return color.RGBA{R: 255, G: 255, B: 255, A: 255}
}

func firstColor(values ...color.Color) color.Color {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func contrastRatio(fg color.Color, bg color.Color) float64 {
	fgLum := relativeLuminance(fg)
	bgLum := relativeLuminance(bg)
	light := math.Max(fgLum, bgLum)
	dark := math.Min(fgLum, bgLum)
	return (light + 0.05) / (dark + 0.05)
}

func relativeLuminance(c color.Color) float64 {
	r, g, b, ok := rgb8(c)
	if !ok {
		return 0
	}
	return (0.2126 * linearRGB(float64(r)/255)) +
		(0.7152 * linearRGB(float64(g)/255)) +
		(0.0722 * linearRGB(float64(b)/255))
}

func linearRGB(v float64) float64 {
	if v <= 0.03928 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func colorIsDark(c color.Color) bool {
	r, g, b, ok := rgb8(c)
	if !ok {
		return true
	}
	luma := (0.2126 * float64(r)) + (0.7152 * float64(g)) + (0.0722 * float64(b))
	return luma < 140
}

func stripThemeBackgroundsForANSI(theme Theme, profile colorprofile.Profile) Theme {
	if profile != colorprofile.ANSI {
		return theme
	}
	theme.AppBg = nil
	theme.ModalBg = nil
	theme.StatusBg = nil
	theme.CommandBg = nil
	theme.CommandActive = nil
	theme.SelectionBg = nil
	theme.UserBg = nil
	theme.DiffAddBg = nil
	theme.DiffAddStrongBg = nil
	theme.DiffRemoveBg = nil
	theme.DiffRemoveStrongBg = nil
	theme.InputBarBg = nil
	theme.ToolOutputBg = nil
	theme.NewMsgBg = nil
	theme.CodeBg = nil
	theme.CodeBlockBg = nil
	theme.TranscriptPillBg = nil
	theme.CodeSurface = nil
	theme.TableHeaderBg = nil
	return theme
}

func defaultAdaptiveThemeVariant(profile colorprofile.Profile, dark bool, background color.Color) Theme {
	if dark {
		return adaptiveDarkThemeVariant(profile, background)
	}
	return adaptiveLightThemeVariant(profile, background)
}

func adaptiveDarkThemeVariant(profile colorprofile.Profile, background color.Color) Theme {
	surface1 := adaptiveBackgroundColor(profile, background, true, 0.08, 0, "#12161d", "", "234", "")
	surface2 := adaptiveBackgroundColor(profile, background, true, 0.12, 0, "#1b222d", "", "236", "")
	selection := adaptiveTintColor(profile, background, true, [3]uint8{130, 150, 170}, [3]uint8{}, 0.24, 0, "#2a3544", "", "240", "")
	addBg := adaptiveTintColor(profile, background, true, [3]uint8{87, 199, 133}, [3]uint8{}, 0.15, 0, "#1d3328", "", "22", "")
	addStrongBg := adaptiveTintColor(profile, background, true, [3]uint8{87, 199, 133}, [3]uint8{}, 0.24, 0, "#254935", "", "29", "")
	delBg := adaptiveTintColor(profile, background, true, [3]uint8{255, 107, 99}, [3]uint8{}, 0.16, 0, "#3e2422", "", "52", "")
	delStrongBg := adaptiveTintColor(profile, background, true, [3]uint8{255, 107, 99}, [3]uint8{}, 0.25, 0, "#57302c", "", "88", "")

	return Theme{
		Name:           "dark",
		IsDark:         true,
		AppBg:          nil,
		PanelBorder:    profileColor(profile, "#2a3342", "238", "8"),
		PanelTitle:     profileColor(profile, "#f5f7fb", "255", "7"),
		TextPrimary:    profileColor(profile, "#e6e8ee", "254", "7"),
		TextSecondary:  profileColor(profile, "#a4adbb", "250", "7"),
		SecondaryText:  profileColor(profile, "#a4adbb", "250", "7"),
		MutedText:      profileColor(profile, "#707989", "242", "8"),
		Info:           profileColor(profile, "#5bb8d7", "39", "6"),
		Success:        profileColor(profile, "#57c785", "35", "2"),
		Warning:        profileColor(profile, "#d6a94a", "214", "3"),
		Error:          profileColor(profile, "#ff6b63", "203", "1"),
		Accent:         profileColor(profile, "#b59cff", "141", "5"),
		Focus:          profileColor(profile, "#5bb8d7", "39", "6"),
		ModalBg:        surface1,
		StatusBg:       surface1,
		StatusText:     profileColor(profile, "#a4adbb", "250", "7"),
		CommandBg:      nil,
		CommandActive:  selection,
		CommandText:    profileColor(profile, "#e6e8ee", "254", "7"),
		CommandSubText: profileColor(profile, "#707989", "242", "8"),
		SelectionFg:    profileColor(profile, "#f5f7fb", "255", "7"),
		SelectionBg:    selection,

		AssistantFg:        profileColor(profile, "#e6e8ee", "254", "7"),
		ReasoningFg:        profileColor(profile, "#96a2b2", "246", "8"),
		UserFg:             profileColor(profile, "#f5f7fb", "255", "7"),
		UserBg:             surface2,
		UserPrefixFg:       profileColor(profile, "#5bb8d7", "39", "6"),
		UserMentionFg:      profileColor(profile, "#b59cff", "141", "5"),
		ToolFg:             profileColor(profile, "#5bb8d7", "39", "6"),
		DiffAddFg:          profileColor(profile, "#57c785", "35", "2"),
		DiffRemoveFg:       profileColor(profile, "#ff6b63", "203", "1"),
		DiffHeaderFg:       profileColor(profile, "#a4adbb", "250", "7"),
		DiffHunkFg:         profileColor(profile, "#b59cff", "141", "5"),
		DiffAddBg:          addBg,
		DiffAddStrongBg:    addStrongBg,
		DiffRemoveBg:       delBg,
		DiffRemoveStrongBg: delStrongBg,
		DiffLineNoFg:       profileColor(profile, "#707989", "242", "8"),
		DiffGutterFg:       profileColor(profile, "#96a2b2", "246", "8"),
		DiffPanelBorder:    profileColor(profile, "#2a3342", "238", "8"),
		SectionFg:          profileColor(profile, "#f5f7fb", "255", "7"),
		KeyLabelFg:         profileColor(profile, "#a4adbb", "250", "7"),
		NoteFg:             profileColor(profile, "#96a2b2", "246", "8"),
		PromptFg:           profileColor(profile, "#5bb8d7", "39", "6"),
		CursorFg:           profileColor(profile, "#f5f7fb", "255", "7"),
		ScrollHintFg:       profileColor(profile, "#d6a94a", "214", "3"),

		InputBarBg:          nil,
		InputBarFg:          profileColor(profile, "#e6e8ee", "254", "7"),
		ToolOutputBg:        nil,
		HelpHintFg:          profileColor(profile, "#707989", "242", "8"),
		SpinnerFg:           profileColor(profile, "#5bb8d7", "39", "6"),
		SeparatorFg:         profileColor(profile, "#2a3342", "238", "8"),
		RoleBorderFg:        profileColor(profile, "#2a3342", "238", "8"),
		NewMsgBg:            selection,
		ComposerBorder:      profileColor(profile, "#2a3342", "238", "8"),
		ComposerBorderFocus: profileColor(profile, "#5bb8d7", "39", "6"),
		ScrollbarTrack:      profileColor(profile, "#1c2430", "236", "8"),
		ScrollbarThumb:      profileColor(profile, "#687487", "244", "7"),
		LinkFg:              profileColor(profile, "#5bb8d7", "39", "6"),
		CodeFg:              profileColor(profile, "#d7c3ff", "183", "5"),
		CodeBg:              surface2,
		CodeBlockFg:         profileColor(profile, "#e6e8ee", "254", "7"),
		CodeBlockBg:         surface1,
		TranscriptRail:      profileColor(profile, "#252d3a", "238", "8"),
		TranscriptShell:     profileColor(profile, "#354052", "240", "8"),
		TranscriptPillBg:    nil,
		CodeSurface:         surface1,
		TableHeaderBg:       surface1,
		TableBorder:         profileColor(profile, "#4c5868", "242", "8"),
	}
}

func adaptiveLightThemeVariant(profile colorprofile.Profile, background color.Color) Theme {
	surface1 := adaptiveBackgroundColor(profile, background, false, 0, 0.035, "", "#f7f8fa", "", "255")
	surface2 := adaptiveBackgroundColor(profile, background, false, 0, 0.055, "", "#eef1f5", "", "255")
	selection := adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{47, 143, 175}, 0, 0.12, "", "#dceff5", "", "153")
	addBg := adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{87, 199, 133}, 0, 0.13, "", "#e2f5e8", "", "194")
	addStrongBg := adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{87, 199, 133}, 0, 0.22, "", "#c8ecd5", "", "157")
	delBg := adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{255, 107, 99}, 0, 0.10, "", "#fde9e7", "", "224")
	delStrongBg := adaptiveTintColor(profile, background, false, [3]uint8{}, [3]uint8{255, 107, 99}, 0, 0.18, "", "#f8d2cf", "", "217")

	return Theme{
		Name:           "light",
		IsDark:         false,
		AppBg:          nil,
		PanelBorder:    profileColor(profile, "#d6dde7", "252", "8"),
		PanelTitle:     profileColor(profile, "#111827", "235", "0"),
		TextPrimary:    profileColor(profile, "#172033", "235", "0"),
		TextSecondary:  profileColor(profile, "#4f5b6b", "240", "0"),
		SecondaryText:  profileColor(profile, "#4f5b6b", "240", "0"),
		MutedText:      profileColor(profile, "#737d8c", "243", "8"),
		Info:           profileColor(profile, "#2f8faf", "32", "6"),
		Success:        profileColor(profile, "#16894a", "28", "2"),
		Warning:        profileColor(profile, "#b7791f", "172", "3"),
		Error:          profileColor(profile, "#c93b33", "160", "1"),
		Accent:         profileColor(profile, "#6f5db8", "93", "5"),
		Focus:          profileColor(profile, "#2f8faf", "32", "6"),
		ModalBg:        surface1,
		StatusBg:       surface1,
		StatusText:     profileColor(profile, "#4f5b6b", "240", "0"),
		CommandBg:      nil,
		CommandActive:  selection,
		CommandText:    profileColor(profile, "#172033", "235", "0"),
		CommandSubText: profileColor(profile, "#737d8c", "243", "8"),
		SelectionFg:    profileColor(profile, "#172033", "235", "0"),
		SelectionBg:    selection,

		AssistantFg:        profileColor(profile, "#172033", "235", "0"),
		ReasoningFg:        profileColor(profile, "#687386", "243", "8"),
		UserFg:             profileColor(profile, "#172033", "235", "0"),
		UserBg:             surface1,
		UserPrefixFg:       profileColor(profile, "#2f8faf", "32", "6"),
		UserMentionFg:      profileColor(profile, "#6f5db8", "93", "5"),
		ToolFg:             profileColor(profile, "#2f8faf", "32", "6"),
		DiffAddFg:          profileColor(profile, "#16894a", "28", "2"),
		DiffRemoveFg:       profileColor(profile, "#c93b33", "160", "1"),
		DiffHeaderFg:       profileColor(profile, "#4f5b6b", "240", "0"),
		DiffHunkFg:         profileColor(profile, "#6f5db8", "93", "5"),
		DiffAddBg:          addBg,
		DiffAddStrongBg:    addStrongBg,
		DiffRemoveBg:       delBg,
		DiffRemoveStrongBg: delStrongBg,
		DiffLineNoFg:       profileColor(profile, "#737d8c", "243", "8"),
		DiffGutterFg:       profileColor(profile, "#687386", "243", "8"),
		DiffPanelBorder:    profileColor(profile, "#d6dde7", "252", "8"),
		SectionFg:          profileColor(profile, "#111827", "235", "0"),
		KeyLabelFg:         profileColor(profile, "#4f5b6b", "240", "0"),
		NoteFg:             profileColor(profile, "#687386", "243", "8"),
		PromptFg:           profileColor(profile, "#2f8faf", "32", "6"),
		CursorFg:           profileColor(profile, "#111827", "235", "0"),
		ScrollHintFg:       profileColor(profile, "#b7791f", "172", "3"),

		InputBarBg:          nil,
		InputBarFg:          profileColor(profile, "#172033", "235", "0"),
		ToolOutputBg:        nil,
		HelpHintFg:          profileColor(profile, "#737d8c", "243", "8"),
		SpinnerFg:           profileColor(profile, "#2f8faf", "32", "6"),
		SeparatorFg:         profileColor(profile, "#d6dde7", "252", "8"),
		RoleBorderFg:        profileColor(profile, "#d6dde7", "252", "8"),
		NewMsgBg:            selection,
		ComposerBorder:      profileColor(profile, "#d6dde7", "252", "8"),
		ComposerBorderFocus: profileColor(profile, "#2f8faf", "32", "6"),
		ScrollbarTrack:      profileColor(profile, "#e7ebf1", "254", "8"),
		ScrollbarThumb:      profileColor(profile, "#98a4b3", "247", "7"),
		LinkFg:              profileColor(profile, "#2f8faf", "32", "6"),
		CodeFg:              profileColor(profile, "#5f4aa2", "93", "5"),
		CodeBg:              surface2,
		CodeBlockFg:         profileColor(profile, "#172033", "235", "0"),
		CodeBlockBg:         surface1,
		TranscriptRail:      profileColor(profile, "#d6dde7", "252", "8"),
		TranscriptShell:     profileColor(profile, "#a8b3c2", "247", "8"),
		TranscriptPillBg:    nil,
		CodeSurface:         surface1,
		TableHeaderBg:       surface1,
		TableBorder:         profileColor(profile, "#9aa7b6", "247", "8"),
	}
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
		SelectionFg:    themeColor(trueColor, "#f8fafc", "255"),
		SelectionBg:    themeColor(trueColor, "#334155", "240"),

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
		SelectionFg:    themeColor(trueColor, "#0f172a", "235"),
		SelectionBg:    themeColor(trueColor, "#dbeafe", "153"),

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
	theme.ReasoningFg = theme.MutedText
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
	theme.ReasoningFg = theme.MutedText
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
	theme.ReasoningFg = theme.MutedText
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
