package tuiapp

import (
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func newPromptTextarea(theme tuikit.Theme) textarea.Model {
	ta := textarea.New()
	ta.Placeholder = ""
	ta.Prompt = "> "
	ta.SetPromptFunc(2, func(info textarea.PromptInfo) string {
		if info.LineNumber == 0 {
			return "> "
		}
		return "  "
	})
	ta.CharLimit = 0
	ta.SetWidth(80)
	ta.SetHeight(1)
	ta.MaxHeight = maxInputBarRows
	ta.ShowLineNumbers = false
	ta.SetVirtualCursor(false)
	applyPromptTextareaStyles(&ta, theme)
	ta.Focus()

	return ta
}

func applyPromptTextareaStyles(editor *textarea.Model, theme tuikit.Theme) {
	styles := editor.Styles()
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Focused.Base = lipgloss.NewStyle()
	styles.Focused.Prompt = theme.PromptStyle()
	styles.Focused.Text = theme.TextStyle()
	styles.Focused.Placeholder = theme.HelpHintTextStyle()
	styles.Blurred.CursorLine = lipgloss.NewStyle()
	styles.Blurred.Base = lipgloss.NewStyle()
	styles.Blurred.Prompt = theme.PromptStyle()
	styles.Blurred.Text = theme.TextStyle()
	styles.Blurred.Placeholder = theme.HelpHintTextStyle()
	styles.Cursor.Color = theme.CursorFg
	styles.Cursor.Shape = tea.CursorBar
	styles.Cursor.Blink = true
	editor.SetStyles(styles)
}
