package tuikit

import "charm.land/lipgloss/v2"

type ComponentStyles struct {
	Status   StatusStyles
	Tool     ToolStyles
	Approval ApprovalStyles
	Composer ComposerStyles
}

type StatusStyles struct {
	Bar  lipgloss.Style
	Text lipgloss.Style
}

type ToolStyles struct {
	Normal lipgloss.Style
	Error  lipgloss.Style
}

type ApprovalStyles struct {
	Title  lipgloss.Style
	Risk   lipgloss.Style
	Detail lipgloss.Style
}

type ComposerStyles struct {
	Focused lipgloss.Style
	Blurred lipgloss.Style
	Help    lipgloss.Style
}

func (t Theme) ComponentStyles() ComponentStyles {
	return ComponentStyles{
		Status: StatusStyles{
			Bar:  t.StatusStyle(),
			Text: t.TextStyle(),
		},
		Tool: ToolStyles{
			Normal: t.TextStyle(),
			Error:  t.ErrorStyle(),
		},
		Approval: ApprovalStyles{
			Title:  t.TitleStyle(),
			Risk:   t.WarnStyle(),
			Detail: t.TextStyle(),
		},
		Composer: ComposerStyles{
			Focused: t.ComposerStyle(true),
			Blurred: t.ComposerStyle(false),
			Help:    t.HelpHintTextStyle(),
		},
	}
}
