package tuiapp

import (
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type appKeyMap struct {
	Send          key.Binding
	InsertNewline key.Binding
	Queue         key.Binding
	Interrupt     key.Binding
	Mode          key.Binding
	HistoryPrev   key.Binding
	HistoryNext   key.Binding
	ChoosePrev    key.Binding
	ChooseNext    key.Binding
	Accept        key.Binding
	Complete      key.Binding
	ImagePaste    key.Binding
	TextPaste     key.Binding
	Clear         key.Binding
	Back          key.Binding
	OverlayScroll key.Binding
	OverlayClose  key.Binding
	PageUp        key.Binding
	PageDown      key.Binding
	HalfPageUp    key.Binding
	HalfPageDown  key.Binding
	Quit          key.Binding
}

type helpBindings struct {
	short []key.Binding
	full  [][]key.Binding
}

func (h helpBindings) ShortHelp() []key.Binding {
	return h.short
}

func (h helpBindings) FullHelp() [][]key.Binding {
	return h.full
}

func defaultKeyMap(isWSL bool) appKeyMap {
	imagePasteKeys := []string{"ctrl+v"}
	imagePasteHelp := "ctrl+v"
	textPasteKeys := []string{"super+v", "cmd+v", "ctrl+shift+v", "shift+insert"}
	textPasteHelp := "paste"
	if isWSL {
		imagePasteKeys = []string{"ctrl+alt+v"}
		imagePasteHelp = "ctrl+alt+v"
		textPasteKeys = []string{"ctrl+v", "ctrl+shift+v", "shift+insert"}
		textPasteHelp = "ctrl+v"
	}
	return appKeyMap{
		Send:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		InsertNewline: key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"), key.WithHelp("shift+enter", "newline")),
		Queue:         key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "queue")),
		Interrupt:     key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "interrupt")),
		Mode:          key.NewBinding(key.WithKeys("shift+tab", "backtab", "ctrl+o"), key.WithHelp("shift+tab", "mode")),
		HistoryPrev:   key.NewBinding(key.WithKeys("up"), key.WithHelp("↑", "history")),
		HistoryNext:   key.NewBinding(key.WithKeys("down"), key.WithHelp("↓", "draft")),
		ChoosePrev:    key.NewBinding(key.WithKeys("up"), key.WithHelp("↑/↓", "select")),
		ChooseNext:    key.NewBinding(key.WithKeys("down"), key.WithHelp("↑/↓", "select")),
		Accept:        key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "apply")),
		Complete:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "fill")),
		ImagePaste:    key.NewBinding(key.WithKeys(imagePasteKeys...), key.WithHelp(imagePasteHelp, "image")),
		TextPaste:     key.NewBinding(key.WithKeys(textPasteKeys...), key.WithHelp(textPasteHelp, "text")),
		Clear:         key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "clear")),
		Back:          key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		OverlayScroll: key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑/↓", "scroll")),
		OverlayClose:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		PageUp:        key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "scroll")),
		PageDown:      key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "scroll")),
		HalfPageUp:    key.NewBinding(key.WithKeys("shift+pgup"), key.WithHelp("shift+pgup", "½ scroll")),
		HalfPageDown:  key.NewBinding(key.WithKeys("shift+pgdown"), key.WithHelp("shift+pgdn", "½ scroll")),
		Quit:          key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
	}
}

func matchesInsertNewlineKey(msg tea.KeyMsg, binding key.Binding) bool {
	if key.Matches(msg, binding) {
		return true
	}
	switch msg.String() {
	case "shift+enter", "ctrl+j":
		return true
	default:
		return false
	}
}

func matchesModeKey(msg tea.KeyMsg, binding key.Binding) bool {
	if key.Matches(msg, binding) {
		return true
	}
	switch msg.String() {
	case "shift+tab", "backtab", "ctrl+o":
		return true
	default:
		return false
	}
}

func configureHelpStyles(m *help.Model, themeColors interface {
	KeyLabelStyle() lipgloss.Style
	HelpHintTextStyle() lipgloss.Style
	TextStyle() lipgloss.Style
}) {
	if m == nil {
		return
	}
	m.ShortSeparator = "  "
	m.FullSeparator = "   "
	m.Ellipsis = "…"
	styles := m.Styles
	styles.ShortKey = themeColors.KeyLabelStyle().Bold(true)
	styles.ShortDesc = themeColors.HelpHintTextStyle()
	styles.ShortSeparator = themeColors.HelpHintTextStyle()
	styles.FullKey = themeColors.KeyLabelStyle().Bold(true)
	styles.FullDesc = themeColors.TextStyle()
	styles.FullSeparator = themeColors.HelpHintTextStyle()
	styles.Ellipsis = themeColors.HelpHintTextStyle()
	m.Styles = styles
}

func enabledBindings(bindings ...key.Binding) []key.Binding {
	out := make([]key.Binding, 0, len(bindings))
	for _, binding := range bindings {
		if binding.Enabled() {
			out = append(out, binding)
		}
	}
	return out
}

func (m *Model) currentFooterHelp() helpBindings {
	if m.activePrompt != nil {
		if len(m.activePrompt.choices) > 0 {
			return helpBindings{
				short: enabledBindings(m.keys.Accept, m.keys.Back),
				full: [][]key.Binding{
					enabledBindings(m.keys.Accept, m.keys.Back),
				},
			}
		}
		return helpBindings{
			short: enabledBindings(m.keys.Accept, m.keys.Back),
			full: [][]key.Binding{
				enabledBindings(m.keys.Accept, m.keys.Back),
			},
		}
	}
	if m.btwOverlay != nil {
		return helpBindings{
			short: enabledBindings(m.keys.OverlayClose),
			full: [][]key.Binding{
				enabledBindings(m.keys.OverlayClose),
			},
		}
	}
	if m.showPalette || len(m.resumeCandidates) > 0 || m.slashArgActive || len(m.slashCandidates) > 0 || len(m.mentionCandidates) > 0 || len(m.skillCandidates) > 0 {
		return helpBindings{
			short: enabledBindings(m.keys.Back),
			full: [][]key.Binding{
				enabledBindings(m.keys.Back),
			},
		}
	}
	if m.running {
		return helpBindings{
			short: enabledBindings(m.keys.Mode, m.keys.Interrupt),
			full: [][]key.Binding{
				enabledBindings(m.keys.Mode, m.keys.Interrupt),
			},
		}
	}
	return helpBindings{
		short: enabledBindings(m.keys.Mode),
		full: [][]key.Binding{
			enabledBindings(m.keys.Mode),
		},
	}
}

func (m *Model) overlayHintText(label string) string {
	label = strings.TrimSpace(label)
	bindings := enabledBindings(m.keys.ChoosePrev, m.keys.Accept, m.keys.Complete)
	if label == "" {
		return m.help.ShortHelpView(bindings)
	}
	helpText := m.help.ShortHelpView(bindings)
	if helpText == "" {
		return label
	}
	return label + "  " + helpText
}
