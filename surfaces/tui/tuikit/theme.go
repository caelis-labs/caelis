package tuikit

import (
	"image/color"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

// Theme is the resolved set of colors consumed by TUI components. Theme
// variants are built from compact semantic palettes in theme_builder.go.
type Theme struct {
	Name    string
	IsDark  bool
	NoColor bool
	Profile colorprofile.Profile

	// TerminalBg retains the sampled terminal background for adaptive surfaces
	// and contrast validation. AppBg is the color actually painted by the TUI.
	TerminalBg       color.Color
	AppBg            color.Color
	PanelBorder      color.Color
	PanelTitle       color.Color
	TextPrimary      color.Color
	TextSecondary    color.Color
	SecondaryText    color.Color
	MutedText        color.Color
	Info             color.Color
	Success          color.Color
	Warning          color.Color
	Error            color.Color
	Accent           color.Color
	Focus            color.Color
	ModalBg          color.Color
	StatusBg         color.Color
	StatusText       color.Color
	CommandBg        color.Color
	CommandActive    color.Color
	CommandText      color.Color
	CommandSubText   color.Color
	SelectionFg      color.Color
	SelectionBg      color.Color
	InputSelectionFg color.Color
	InputSelectionBg color.Color

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

	// Input area.
	PromptFg     color.Color
	CursorFg     color.Color
	ScrollHintFg color.Color

	// Inline layout.
	InputBarFg          color.Color
	ComposerBg          color.Color
	ToolOutputBg        color.Color
	HelpHintFg          color.Color
	SpinnerFg           color.Color
	SeparatorFg         color.Color
	RoleBorderFg        color.Color
	NewMsgBg            color.Color
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
	var issues []ThemeIssue
	if theme.Profile != colorprofile.ANSI && theme.Profile != colorprofile.NoTTY {
		switch {
		case theme.UserBg == nil:
			issues = append(issues, ThemeIssue{Field: "UserBg", Message: "surface is required"})
		case theme.ComposerBg == nil:
			issues = append(issues, ThemeIssue{Field: "ComposerBg", Message: "surface is required"})
		case colorsEqual(theme.UserBg, theme.ComposerBg):
			issues = append(issues, ThemeIssue{Field: "ComposerBg", Message: "must differ from UserBg"})
		}
	}

	bg := validationBackground(theme)
	checks := []struct {
		field     string
		fg        color.Color
		bg        color.Color
		threshold float64
	}{
		{field: "TextPrimary", fg: theme.TextPrimary, bg: bg, threshold: 4.5},
		{field: "TextSecondary", fg: firstColor(theme.TextSecondary, theme.SecondaryText), bg: bg, threshold: 4.5},
		{field: "MutedText", fg: theme.MutedText, bg: bg, threshold: 4.5},
		{field: "MutedText/ModalBg", fg: theme.MutedText, bg: firstColor(theme.ModalBg, bg), threshold: 4.5},
		{field: "ReasoningFg", fg: theme.ReasoningFg, bg: bg, threshold: 4.5},
		{field: "HelpHintFg", fg: theme.HelpHintFg, bg: firstColor(theme.ComposerBg, bg), threshold: 4.5},
		{field: "LinkFg", fg: theme.LinkFg, bg: bg, threshold: 4.5},
		{field: "Accent", fg: theme.Accent, bg: bg, threshold: 4.5},
		{field: "ToolFg", fg: theme.ToolFg, bg: bg, threshold: 3.0},
		{field: "UserPrefixFg", fg: theme.UserPrefixFg, bg: firstColor(theme.UserBg, bg), threshold: 3.0},
		{field: "Focus", fg: theme.Focus, bg: bg, threshold: 3.0},
		{field: "ComposerBorderFocus", fg: theme.ComposerBorderFocus, bg: firstColor(theme.ComposerBg, bg), threshold: 3.0},
		{field: "Warning", fg: theme.Warning, bg: bg, threshold: 3.0},
		{field: "Error", fg: theme.Error, bg: bg, threshold: 3.0},
		{field: "Success", fg: theme.Success, bg: bg, threshold: 3.0},
		{field: "DiffAddFg", fg: theme.DiffAddFg, bg: firstColor(theme.DiffAddBg, bg), threshold: 3.0},
		{field: "DiffRemoveFg", fg: theme.DiffRemoveFg, bg: firstColor(theme.DiffRemoveBg, bg), threshold: 3.0},
		{field: "SelectionFg", fg: theme.SelectionFg, bg: theme.SelectionBg, threshold: 4.5},
		{field: "InputSelectionFg", fg: theme.InputSelectionFg, bg: theme.InputSelectionBg, threshold: 4.5},
	}
	for _, check := range checks {
		if check.fg == nil || check.bg == nil {
			continue
		}
		if contrastRatio(check.fg, check.bg) < check.threshold {
			issues = append(issues, ThemeIssue{Field: check.field, Message: "contrast below threshold"})
		}
	}
	return issues
}

// Tokens returns the resolved semantic design tokens for this theme.
func (t *Theme) Tokens() Tokens {
	if t.tokens != nil {
		return *t.tokens
	}
	tok := resolveTokens(*t)
	t.tokens = &tok
	return tok
}

// InvalidateTokens clears the cached tokens after a theme mutation.
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
	return name == "" || name == "auto" || name == "default" || name == "catppuccin" || name == "catppuccin-auto"
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
	theme.TerminalBg = resolvedBackgroundColor(opts)
	theme.NoColor = opts.noColor
	theme = ensureThemeTextContrast(theme)
	if accent := strings.TrimSpace(os.Getenv("CAELIS_ACCENT")); accent != "" {
		applyAccentOverride(&theme, lipgloss.Color(accent))
	}
	applySyntaxColors(&theme)
	if opts.noColor {
		return stripThemeColors(theme)
	}
	return theme
}

func applyAccentOverride(theme *Theme, accent color.Color) {
	if theme == nil || accent == nil {
		return
	}
	theme.Accent = accent
	theme.Focus = accent
	theme.PromptFg = accent
	theme.SpinnerFg = accent
	theme.ComposerBorderFocus = accent
	theme.LinkFg = accent
	theme.InputSelectionBg = accent
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
	if opts.noColor {
		return true
	}
	if dark, ok := darkBackgroundFromEnv(); ok {
		return dark
	}
	if runningUnderGoTest() {
		return true
	}
	if dark, ok := darkBackgroundFromPlatform(); ok {
		return dark
	}
	return lipgloss.HasDarkBackground(os.Stdin, os.Stdout)
}

func resolvedBackgroundColor(opts themeResolveOptions) color.Color {
	if opts.backgroundColorKnown {
		return opts.backgroundColor
	}
	return nil
}

func darkBackgroundFromEnv() (bool, bool) {
	colorfgbg := strings.TrimSpace(os.Getenv("COLORFGBG"))
	if colorfgbg == "" {
		return false, false
	}
	parts := strings.FieldsFunc(colorfgbg, func(r rune) bool {
		return r == ';' || r == ':'
	})
	for i := len(parts) - 1; i >= 0; i-- {
		part := strings.TrimSpace(parts[i])
		if part == "" {
			continue
		}
		index, err := strconv.Atoi(part)
		if err != nil {
			return false, false
		}
		return terminalColorIndexIsDark(index)
	}
	return false, false
}

func terminalColorIndexIsDark(index int) (bool, bool) {
	r, g, b, ok := terminalColorIndexRGB(index)
	if !ok {
		return false, false
	}
	luma := (0.2126 * float64(r)) + (0.7152 * float64(g)) + (0.0722 * float64(b))
	return luma < 140, true
}

func terminalColorIndexRGB(index int) (uint8, uint8, uint8, bool) {
	if index < 0 || index > 255 {
		return 0, 0, 0, false
	}
	if index < 16 {
		colors := [16][3]uint8{
			{0x00, 0x00, 0x00}, {0x80, 0x00, 0x00}, {0x00, 0x80, 0x00}, {0x80, 0x80, 0x00},
			{0x00, 0x00, 0x80}, {0x80, 0x00, 0x80}, {0x00, 0x80, 0x80}, {0xc0, 0xc0, 0xc0},
			{0x80, 0x80, 0x80}, {0xff, 0x00, 0x00}, {0x00, 0xff, 0x00}, {0xff, 0xff, 0x00},
			{0x00, 0x00, 0xff}, {0xff, 0x00, 0xff}, {0x00, 0xff, 0xff}, {0xff, 0xff, 0xff},
		}
		rgb := colors[index]
		return rgb[0], rgb[1], rgb[2], true
	}
	if index < 232 {
		levels := [6]uint8{0, 95, 135, 175, 215, 255}
		offset := index - 16
		return levels[offset/36], levels[(offset/6)%6], levels[offset%6], true
	}
	gray := uint8(8 + ((index - 232) * 10))
	return gray, gray, gray, true
}

func supportsTrueColor() bool {
	colorterm := strings.ToLower(strings.TrimSpace(os.Getenv("COLORTERM")))
	if strings.Contains(colorterm, "truecolor") || strings.Contains(colorterm, "24bit") {
		return true
	}
	term := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
	return strings.Contains(term, "truecolor") || strings.Contains(term, "24bit") || strings.Contains(term, "direct")
}

func runningUnderGoTest() bool {
	name := strings.ToLower(filepath.Base(os.Args[0]))
	if runtime.GOOS == "windows" {
		return strings.HasSuffix(name, ".test.exe")
	}
	return strings.HasSuffix(name, ".test")
}

func supportsANSI256() bool {
	return strings.Contains(strings.ToLower(strings.TrimSpace(os.Getenv("TERM"))), "256color")
}

func stripThemeColors(theme Theme) Theme {
	return Theme{
		Name:    theme.Name,
		IsDark:  theme.IsDark,
		NoColor: true,
		Profile: colorprofile.NoTTY,
	}
}

func namedTheme(name string, profile colorprofile.Profile, darkBackground bool, background color.Color) Theme {
	switch name {
	case "", "auto", "default":
		return defaultAdaptiveThemeVariant(profile, darkBackground, background)
	case "dark":
		return defaultAdaptiveThemeVariant(profile, true, background)
	case "light":
		return defaultAdaptiveThemeVariant(profile, false, background)
	case "catppuccin", "catppuccin-auto":
		return catppuccinAdaptiveThemeVariant(profile, darkBackground, background)
	case "catppuccin-dark", "catppuccin-mocha":
		return catppuccinAdaptiveThemeVariant(profile, true, background)
	case "catppuccin-light", "catppuccin-latte":
		return catppuccinAdaptiveThemeVariant(profile, false, background)
	case "nord":
		return stripThemeBackgroundsForANSI(nordTheme(profile), profile)
	case "solarized":
		return stripThemeBackgroundsForANSI(solarizedTheme(profile), profile)
	case "dracula":
		return stripThemeBackgroundsForANSI(draculaTheme(profile), profile)
	default:
		return defaultAdaptiveThemeVariant(profile, darkBackground, background)
	}
}

func profileColor(profile colorprofile.Profile, rich, ansi256, ansi16 string) color.Color {
	switch profile {
	case colorprofile.TrueColor:
		if rich != "" {
			return lipgloss.Color(rich)
		}
	case colorprofile.ANSI256:
		if ansi256 != "" {
			return lipgloss.Color(ansi256)
		}
	case colorprofile.ANSI:
		if ansi16 != "" {
			return lipgloss.Color(ansi16)
		}
	}
	return nil
}

func adaptiveBackgroundColor(profile colorprofile.Profile, terminal color.Color, dark bool, darkAlpha, lightAlpha float64, darkFallback, lightFallback, dark256, light256 string) color.Color {
	if profile == colorprofile.TrueColor {
		if r, g, b, ok := rgb8(terminal); ok {
			top := [3]uint8{}
			alpha := lightAlpha
			if dark {
				top = [3]uint8{255, 255, 255}
				alpha = darkAlpha
			}
			return lipgloss.Color(hexColor(blendRGB([3]uint8{r, g, b}, top, alpha)))
		}
		if dark {
			return profileColor(profile, darkFallback, "", "")
		}
		return profileColor(profile, lightFallback, "", "")
	}
	if dark {
		return profileColor(profile, "", dark256, "")
	}
	return profileColor(profile, "", light256, "")
}

func adaptiveTintColor(profile colorprofile.Profile, terminal color.Color, dark bool, darkTop, lightTop [3]uint8, darkAlpha, lightAlpha float64, darkFallback, lightFallback, dark256, light256 string) color.Color {
	if profile == colorprofile.TrueColor {
		if r, g, b, ok := rgb8(terminal); ok {
			top, alpha := lightTop, lightAlpha
			if dark {
				top, alpha = darkTop, darkAlpha
			}
			return lipgloss.Color(hexColor(blendRGB([3]uint8{r, g, b}, top, alpha)))
		}
		if dark {
			return profileColor(profile, darkFallback, "", "")
		}
		return profileColor(profile, lightFallback, "", "")
	}
	if dark {
		return profileColor(profile, "", dark256, "")
	}
	return profileColor(profile, "", light256, "")
}

func blendRGB(base [3]uint8, top [3]uint8, alpha float64) [3]uint8 {
	return [3]uint8{
		blendChannel(base[0], top[0], alpha),
		blendChannel(base[1], top[1], alpha),
		blendChannel(base[2], top[2], alpha),
	}
}

func blendChannel(base, top uint8, alpha float64) uint8 {
	return uint8(math.Round((float64(top) * alpha) + (float64(base) * (1 - alpha))))
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

func colorsEqual(a, b color.Color) bool {
	ar, ag, ab, aok := rgb8(a)
	br, bg, bb, bok := rgb8(b)
	return aok && bok && ar == br && ag == bg && ab == bb
}

func validationBackground(theme Theme) color.Color {
	if theme.AppBg != nil {
		return theme.AppBg
	}
	if theme.TerminalBg != nil {
		return theme.TerminalBg
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

func contrastRatio(fg, bg color.Color) float64 {
	fgLum, bgLum := relativeLuminance(fg), relativeLuminance(bg)
	return (math.Max(fgLum, bgLum) + 0.05) / (math.Min(fgLum, bgLum) + 0.05)
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
	return (0.2126*float64(r))+(0.7152*float64(g))+(0.0722*float64(b)) < 140
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
	theme.InputSelectionBg = nil
	theme.UserBg = nil
	theme.ComposerBg = nil
	theme.DiffAddBg = nil
	theme.DiffAddStrongBg = nil
	theme.DiffRemoveBg = nil
	theme.DiffRemoveStrongBg = nil
	theme.ToolOutputBg = nil
	theme.NewMsgBg = nil
	theme.CodeBg = nil
	theme.CodeBlockBg = nil
	theme.TranscriptPillBg = nil
	theme.CodeSurface = nil
	theme.TableHeaderBg = nil
	return theme
}
