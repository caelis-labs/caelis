package tuikit

import (
	"image/color"

	"github.com/charmbracelet/colorprofile"
)

// Theme data validation is test support. Production resolves themes through
// ResolveSelectedTheme or ResolveThemeFromOptions; no shipped path validates a
// resolved theme, so the contrast and surface checks live beside the tests that
// assert built-in palettes stay readable.

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
		case colorsEqual(theme.AppBg, theme.ComposerBg):
			issues = append(issues, ThemeIssue{Field: "ComposerBg", Message: "must differ from AppBg"})
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

func colorsEqual(a, b color.Color) bool {
	ar, ag, ab, aok := rgb8(a)
	br, bg, bb, bok := rgb8(b)
	return aok && bok && ar == br && ag == bg && ab == bb
}
