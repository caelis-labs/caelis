package tuikit

import "image/color"

// themePalette contains semantic foreground colors. Theme variants should
// describe intent here instead of copying the complete Theme field table.
type themePalette struct {
	Name   string
	IsDark bool

	TextPrimary   color.Color
	TextSecondary color.Color
	Muted         color.Color
	Info          color.Color
	Success       color.Color
	Warning       color.Color
	Danger        color.Color
	Accent        color.Color
	Focus         color.Color
	UserAccent    color.Color
	Tool          color.Color
	Border        color.Color
	BorderStrong  color.Color
	DiffHunk      color.Color
	DiffRemove    color.Color
	DiffLineNo    color.Color
	DiffGutter    color.Color
	Cursor        color.Color
	Scrollbar     color.Color
}

// themeSurfaces contains semantic background colors. User and Composer are
// deliberately separate: a theme must opt into both surfaces explicitly.
type themeSurfaces struct {
	App              color.Color
	Base             color.Color
	Raised           color.Color
	User             color.Color
	Composer         color.Color
	Selection        color.Color
	SelectionText    color.Color
	OnAccent         color.Color
	DiffAdd          color.Color
	DiffAddStrong    color.Color
	DiffRemove       color.Color
	DiffRemoveStrong color.Color
}

// themeFrom is the single mapping from semantic palette roles to Theme fields.
// Syntax colors are intentionally absent; applySyntaxColors is their only
// writer after a theme has been resolved.
func themeFrom(p themePalette, s themeSurfaces) Theme {
	return Theme{
		Name:             p.Name,
		IsDark:           p.IsDark,
		AppBg:            s.App,
		PanelBorder:      p.Border,
		PanelTitle:       p.TextPrimary,
		TextPrimary:      p.TextPrimary,
		TextSecondary:    p.TextSecondary,
		SecondaryText:    p.TextSecondary,
		MutedText:        p.Muted,
		Info:             p.Info,
		Success:          p.Success,
		Warning:          p.Warning,
		Error:            p.Danger,
		Accent:           p.Accent,
		Focus:            p.Focus,
		ModalBg:          s.Base,
		StatusBg:         s.Base,
		StatusText:       p.TextSecondary,
		CommandActive:    s.Selection,
		CommandText:      p.TextPrimary,
		CommandSubText:   p.Muted,
		SelectionFg:      firstColor(s.SelectionText, p.TextPrimary),
		SelectionBg:      s.Selection,
		InputSelectionFg: s.OnAccent,
		InputSelectionBg: p.Focus,

		AssistantFg:        p.TextPrimary,
		ReasoningFg:        p.Muted,
		UserFg:             p.TextPrimary,
		UserBg:             s.User,
		UserPrefixFg:       p.UserAccent,
		UserMentionFg:      p.UserAccent,
		ToolFg:             p.Tool,
		DiffAddFg:          p.Success,
		DiffRemoveFg:       firstColor(p.DiffRemove, p.Danger),
		DiffHeaderFg:       p.TextSecondary,
		DiffHunkFg:         firstColor(p.DiffHunk, p.Accent),
		DiffAddBg:          s.DiffAdd,
		DiffAddStrongBg:    s.DiffAddStrong,
		DiffRemoveBg:       s.DiffRemove,
		DiffRemoveStrongBg: s.DiffRemoveStrong,
		DiffLineNoFg:       firstColor(p.DiffLineNo, p.Muted),
		DiffGutterFg:       firstColor(p.DiffGutter, p.TextSecondary),
		DiffPanelBorder:    firstColor(p.BorderStrong, p.Border),
		SectionFg:          p.TextPrimary,
		KeyLabelFg:         p.TextSecondary,
		NoteFg:             p.Muted,

		PromptFg:     p.Focus,
		CursorFg:     firstColor(p.Cursor, p.TextPrimary),
		ScrollHintFg: p.Warning,

		InputBarFg:          p.TextPrimary,
		ComposerBg:          s.Composer,
		HelpHintFg:          p.TextSecondary,
		SpinnerFg:           p.Focus,
		SeparatorFg:         p.Border,
		RoleBorderFg:        firstColor(p.BorderStrong, p.Border),
		NewMsgBg:            s.Selection,
		ComposerBorder:      firstColor(p.BorderStrong, p.Border),
		ComposerBorderFocus: p.Focus,
		ScrollbarTrack:      s.Raised,
		ScrollbarThumb:      firstColor(p.Scrollbar, p.BorderStrong, p.Border),
		LinkFg:              p.Accent,
		TranscriptRail:      p.Border,
		TranscriptShell:     firstColor(p.BorderStrong, p.Border),
		TranscriptPillBg:    s.Raised,
		TableHeaderBg:       s.Base,
		TableBorder:         firstColor(p.BorderStrong, p.Border),
	}
}
