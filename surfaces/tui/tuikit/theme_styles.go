package tuikit

import (
	"strings"

	"charm.land/lipgloss/v2"
)

func (t Theme) FrameStyle() lipgloss.Style {
	return fgStyle(t.TextPrimary).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(t.PanelBorder).
		Padding(0, 1)
}

func (t Theme) StatusStyle() lipgloss.Style {
	return fgStyle(t.StatusText).Padding(0, StatusInset)
}

func (t Theme) HintStyle() lipgloss.Style {
	return quietStyle(t, t.TextSecondary)
}

func (t Theme) SecondaryTextStyle() lipgloss.Style {
	return quietStyle(t, t.SecondaryText)
}

func (t Theme) MutedTextStyle() lipgloss.Style {
	return quietStyle(t, t.MutedText)
}

func (t Theme) HintRowStyle() lipgloss.Style {
	return quietStyle(t, t.TextSecondary).Padding(0, StatusInset)
}

func (t Theme) TextStyle() lipgloss.Style {
	return fgStyle(t.TextPrimary)
}

func (t Theme) TitleStyle() lipgloss.Style {
	return fgStyle(t.PanelTitle).Bold(true)
}

func (t Theme) ModalStyle() lipgloss.Style {
	return withBg(lipgloss.NewStyle(), t.ModalBg).
		BorderStyle(lipgloss.RoundedBorder()).
		BorderForeground(t.Focus).
		Padding(1, 2)
}

func (t Theme) CommandActiveStyle() lipgloss.Style {
	return withBg(fgStyle(t.Focus), t.CommandActive).
		Bold(true).
		Padding(0, 1)
}

func (t Theme) CommandStyle() lipgloss.Style {
	return fgStyle(t.CommandText).Padding(0, 1)
}

func (t Theme) SelectionStyle() lipgloss.Style {
	if t.SelectionFg == nil || t.SelectionBg == nil {
		return lipgloss.NewStyle().Reverse(true)
	}
	return lipgloss.NewStyle().
		Foreground(t.SelectionFg).
		Background(t.SelectionBg)
}

// ---------------------------------------------------------------------------
// Line-style rendering helpers
// ---------------------------------------------------------------------------

// AssistantStyle renders assistant text.
func (t Theme) AssistantStyle() lipgloss.Style {
	return fgStyle(t.AssistantFg)
}

// ReasoningStyle renders reasoning/thinking text in muted contrast.
func (t Theme) ReasoningStyle() lipgloss.Style {
	return quietStyle(t, t.ReasoningFg)
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
	return withBg(fgStyle(t.UserFg).Bold(true), t.UserBg)
}

// UserPrefixStyle renders the leading "> " marker for user messages.
func (t Theme) UserPrefixStyle() lipgloss.Style {
	return fgStyle(t.UserPrefixFg).Bold(true)
}

// UserMentionStyle renders @path mentions inside user messages.
func (t Theme) UserMentionStyle() lipgloss.Style {
	return fgStyle(t.UserMentionFg).Bold(true)
}

// DiffAddStyle renders added lines in diffs (green).
func (t Theme) DiffAddStyle() lipgloss.Style {
	return fgStyle(t.DiffAddFg)
}

// DiffRemoveStyle renders removed lines in diffs (red).
func (t Theme) DiffRemoveStyle() lipgloss.Style {
	return fgStyle(t.DiffRemoveFg)
}

// DiffHeaderStyle renders diff headers (dimmed + bold).
func (t Theme) DiffHeaderStyle() lipgloss.Style {
	return quietStyle(t, t.DiffHeaderFg).Bold(true)
}

// DiffHunkStyle renders diff hunk headers (@@ ... @@) in blue.
func (t Theme) DiffHunkStyle() lipgloss.Style {
	return fgStyle(t.DiffHunkFg).Bold(true)
}

// DiffLineNoStyle renders diff line numbers.
func (t Theme) DiffLineNoStyle() lipgloss.Style {
	return quietStyle(t, t.DiffLineNoFg)
}

// DiffGutterStyle renders diff markers/gutters.
func (t Theme) DiffGutterStyle() lipgloss.Style {
	return quietStyle(t, t.DiffGutterFg)
}

// DiffPanelBorderStyle renders split-view separator lines.
func (t Theme) DiffPanelBorderStyle() lipgloss.Style {
	return fgStyle(t.DiffPanelBorder)
}

// WarnStyle renders warning text (yellow).
func (t Theme) WarnStyle() lipgloss.Style {
	return fgStyle(t.Warning)
}

// ErrorStyle renders error text (red).
func (t Theme) ErrorStyle() lipgloss.Style {
	return fgStyle(t.Error)
}

// NoteStyle renders note text (dimmed).
func (t Theme) NoteStyle() lipgloss.Style {
	return quietStyle(t, t.NoteFg)
}

func (t Theme) TranscriptRailStyle() lipgloss.Style {
	return fgStyle(t.TranscriptRail)
}

func (t Theme) TranscriptShellStyle() lipgloss.Style {
	return fgStyle(t.TranscriptShell)
}

func (t Theme) TranscriptMetaStyle() lipgloss.Style {
	return quietStyle(t, t.MutedText)
}

func (t Theme) TranscriptLabelStyle() lipgloss.Style {
	return quietStyle(t, t.SecondaryText).Bold(true)
}

func (t Theme) TranscriptPillStyle(tone string) lipgloss.Style {
	style := quietStyle(t, t.SecondaryText).Bold(true)
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
		return style
	}
}

func (t Theme) CodeSurfaceStyle() lipgloss.Style {
	return withBg(fgStyle(t.CodeBlockFg), t.CodeSurface)
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
	return quietStyle(t, t.TextSecondary).PaddingLeft(1)
}

// SectionStyle renders section headers (bold).
func (t Theme) SectionStyle() lipgloss.Style {
	return fgStyle(t.SectionFg).Bold(true)
}

// KeyLabelStyle renders key labels in key-value pairs.
func (t Theme) KeyLabelStyle() lipgloss.Style {
	return quietStyle(t, t.KeyLabelFg)
}

// PromptStyle renders the input prompt marker.
func (t Theme) PromptStyle() lipgloss.Style {
	return fgStyle(t.PromptFg).Bold(true)
}

// ScrollHintIndicator renders scroll hint text.
func (t Theme) ScrollHintStyle() lipgloss.Style {
	return fgStyle(t.ScrollHintFg)
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
	return fgStyle(t.InputBarFg).Padding(0, 0)
}

func (t Theme) ComposerStyle(focused bool) lipgloss.Style {
	style := fgStyle(t.InputBarFg).
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
	return quietStyle(t, t.HelpHintFg)
}

// SpinnerStyle renders the spinner indicator.
func (t Theme) SpinnerStyle() lipgloss.Style {
	return fgStyle(t.SpinnerFg)
}

// SeparatorStyle renders horizontal separators.
func (t Theme) SeparatorStyle() lipgloss.Style {
	return fgStyle(t.SeparatorFg)
}

// NewMsgIndicatorStyle renders the "new messages" indicator.
func (t Theme) NewMsgIndicatorStyle() lipgloss.Style {
	return withBg(fgStyle(t.Warning), t.NewMsgBg).
		Bold(true).
		Padding(0, 1)
}

func (t Theme) ScrollbarTrackStyle() lipgloss.Style {
	return fgStyle(t.ScrollbarTrack)
}

func (t Theme) ScrollbarThumbStyle() lipgloss.Style {
	return fgStyle(t.ScrollbarThumb)
}

func (t Theme) LinkStyle() lipgloss.Style {
	return t.MarkdownLinkStyle()
}
