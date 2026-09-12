package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type themePickerState struct {
	previous string
	prompt   *promptState
}

func (m *Model) resolveSelectedTheme() {
	m.applyTheme(tuikit.ResolveSelectedTheme(m.themeName, m.terminalBg, m.terminalDark, m.noColor, m.colorProfile))
}

// Theme commands are intercepted before turn admission and never become a
// Session message, completion-catalog mutation, or process-wide environment write.
func (m *Model) submitThemeCommand(line string) tea.Cmd {
	fields := strings.Fields(line)
	options := tuikit.ThemeOptions(m.colorProfile, m.noColor)
	if len(fields) > 1 {
		name, ok := tuikit.NormalizeThemeName(strings.Join(fields[1:], " "))
		supported := false
		for _, option := range options {
			supported = supported || option.Name == name
		}
		if !ok || !supported {
			return m.showHint("Theme unavailable. Use /theme to see supported choices.", hintOptions{
				priority: HintPriorityHigh, clearOnMessage: true, clearAfter: copyHintDuration,
			})
		}
		m.themeName = name
		m.resetComposerAfterOverlayOpen()
		m.resolveSelectedTheme()
		return nil
	}
	if m.activePrompt != nil {
		return nil
	}
	m.resetComposerAfterOverlayOpen()
	choices := make([]promptChoice, 0, len(options))
	index := 0
	for i, option := range options {
		choices = append(choices, promptChoice{label: option.Label, value: option.Name, detail: option.Detail})
		if option.Name == m.themeName {
			index = i
		}
	}
	prompt := &promptState{
		title:   "Theme",
		choices: choices, choiceIndex: index,
	}
	m.themePicker = &themePickerState{previous: m.themeName, prompt: prompt}
	m.activePrompt = prompt
	m.ensureViewportLayout()
	m.syncViewportContent()
	return nil
}

func (m *Model) previewThemeSelection() {
	picker := m.themePicker
	if picker == nil || m.activePrompt != picker.prompt {
		return
	}
	p := picker.prompt
	if p.choiceIndex < 0 || p.choiceIndex >= len(p.choices) {
		return
	}
	name := p.choices[p.choiceIndex].value
	if m.themeName != name {
		m.themeName = name
		m.resolveSelectedTheme()
	}
}

func (m *Model) finishThemeSelection(line string, err error) {
	picker := m.themePicker
	if picker == nil || picker.prompt != m.activePrompt {
		return
	}
	m.themePicker = nil
	m.themeName = picker.previous
	if err == nil {
		for _, choice := range picker.prompt.choices {
			if choice.value == line {
				m.themeName = line
				break
			}
		}
	}
	m.resolveSelectedTheme()
}

func (m *Model) closeThemePickerForProfileChange() {
	if picker := m.themePicker; picker != nil {
		m.themeName = picker.previous
		m.themePicker = nil
		if m.activePrompt == picker.prompt {
			m.finishPrompt("", nil)
		}
	}
}
