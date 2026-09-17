package tuiapp

import (
	"runtime"
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
	Update        key.Binding
	Back          key.Binding
	OverlayScroll key.Binding
	OverlayClose  key.Binding
	PageUp        key.Binding
	PageDown      key.Binding
	HalfPageUp    key.Binding
	HalfPageDown  key.Binding
	Quit          key.Binding
	PaneFocus     key.Binding
	PaneToggle    key.Binding
	PaneAgents    key.Binding
	PaneLayout    key.Binding
}

func defaultKeyMap(isWSL bool) appKeyMap {
	return defaultKeyMapForPlatform(runtime.GOOS, isWSL)
}

func defaultKeyMapForPlatform(goos string, isWSL bool) appKeyMap {
	imagePasteKeys := imagePasteKeysForPlatform(goos, isWSL)
	imagePasteHelp := imagePasteKeys[0]
	textPasteKeys := textPasteKeysForPlatform(goos, isWSL)
	textPasteHelp := "paste"
	if len(textPasteKeys) > 0 {
		textPasteHelp = textPasteKeys[0]
	}
	return appKeyMap{
		Send:          key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "send")),
		InsertNewline: key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"), key.WithHelp("Ctrl+J", "Newline")),
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
		Update:        key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("ctrl+u", "update")),
		Back:          key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		OverlayScroll: key.NewBinding(key.WithKeys("up", "down"), key.WithHelp("↑/↓", "scroll")),
		OverlayClose:  key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		PageUp:        key.NewBinding(key.WithKeys("pgup"), key.WithHelp("pgup", "scroll")),
		PageDown:      key.NewBinding(key.WithKeys("pgdown"), key.WithHelp("pgdn", "scroll")),
		HalfPageUp:    key.NewBinding(key.WithKeys("shift+pgup"), key.WithHelp("shift+pgup", "½ scroll")),
		HalfPageDown:  key.NewBinding(key.WithKeys("shift+pgdown"), key.WithHelp("shift+pgdn", "½ scroll")),
		Quit:          key.NewBinding(key.WithKeys("ctrl+c"), key.WithHelp("ctrl+c", "quit")),
		PaneFocus:     key.NewBinding(key.WithKeys("f6", "shift+f6"), key.WithHelp("F6", "Focus")),
		PaneToggle:    key.NewBinding(key.WithKeys("f7"), key.WithHelp("F7", "Hide")),
		PaneAgents:    key.NewBinding(key.WithKeys("ctrl+g"), key.WithHelp("Ctrl+G", "Agents")),
		PaneLayout:    key.NewBinding(key.WithKeys("ctrl+l"), key.WithHelp("Ctrl+L", "Layout")),
	}
}

func imagePasteKeysForPlatform(goos string, isWSL bool) []string {
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "windows":
		return []string{"ctrl+alt+v"}
	default:
		if isWSL {
			return []string{"ctrl+alt+v"}
		}
		return []string{"ctrl+v"}
	}
}

func textPasteKeysForPlatform(goos string, isWSL bool) []string {
	switch strings.ToLower(strings.TrimSpace(goos)) {
	case "windows":
		return []string{"ctrl+v", "ctrl+shift+v", "shift+insert"}
	case "darwin":
		return []string{"cmd+v", "super+v", "ctrl+shift+v", "shift+insert"}
	default:
		if isWSL {
			return []string{"ctrl+v", "ctrl+shift+v", "shift+insert"}
		}
		return []string{"ctrl+shift+v", "shift+insert", "super+v", "cmd+v"}
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
