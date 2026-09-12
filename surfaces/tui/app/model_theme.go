package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/uipreferences"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

type themePickerState struct {
	previous          string
	prompt            *promptState
	hoverIndex        int
	pressedName       string
	geometry          themePickerGeometry
	composition       centeredOverlayCache
	previewGeneration uint64
}

func (m *Model) resolveSelectedTheme() {
	m.applyTheme(tuikit.ResolveSelectedTheme(m.themeName, m.terminalBg, m.terminalDark, m.noColor, m.colorProfile))
}

// A late startup load changes the cancel target, not an in-progress preview.
func (m *Model) applySavedTheme(saved string) {
	name, ok := tuikit.NormalizeThemeName(saved)
	if !ok {
		name = "auto"
	}
	if picker := m.themePicker; picker != nil {
		picker.previous = name
		if picker.previewGeneration != 0 {
			return
		}
		picker.prompt.choiceIndex = 0
		for i, choice := range picker.prompt.choices {
			if choice.value == name {
				picker.prompt.choiceIndex = i
				break
			}
		}
	}
	if m.themeName != name {
		m.themeName = name
		m.resolveSelectedTheme()
	}
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
		return m.setUIPreferences(uipreferences.Preferences{Theme: name})
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
	m.themePicker = &themePickerState{previous: m.themeName, prompt: prompt, hoverIndex: -1}
	m.activePrompt = prompt
	m.ensureViewportLayout()
	m.syncViewportContent()
	return nil
}

// Selection paints immediately. Coalesce rapid navigation before retheming the
// transcript, and bind every delayed preview to the picker that requested it.
const themePreviewDelay = 180 * time.Millisecond

type themePreviewMsg struct {
	picker     *themePickerState
	generation uint64
}

func (m *Model) handleThemePickerKey(msg tea.KeyMsg) tea.Cmd {
	// Generic choice prompts also accept a whole choice value as committed text.
	// Theme previews require explicit Enter confirmation, including with IME input.
	switch msg.String() {
	case "up", "down", "enter", "esc", "ctrl+c", "ctrl+d":
	default:
		return nil
	}
	picker := m.themePicker
	before := picker.prompt.choiceIndex
	picker.hoverIndex = -1
	picker.pressedName = ""
	cmd := m.handlePromptChoiceKey(msg)
	if m.themePicker == picker && picker.prompt.choiceIndex != before {
		return tea.Batch(cmd, m.scheduleThemePreview())
	}
	return cmd
}

func (m *Model) scheduleThemePreview() tea.Cmd {
	picker := m.themePicker
	picker.previewGeneration++
	if picker.prompt.choices[picker.prompt.choiceIndex].value == m.themeName {
		return nil
	}
	msg := themePreviewMsg{picker: picker, generation: picker.previewGeneration}
	return tea.Tick(themePreviewDelay, func(time.Time) tea.Msg { return msg })
}

func (m *Model) applyThemePreview(msg themePreviewMsg) {
	picker := m.themePicker
	if picker == nil || picker != msg.picker || m.activePrompt != picker.prompt || picker.previewGeneration != msg.generation {
		return
	}
	name := picker.prompt.choices[picker.prompt.choiceIndex].value
	if m.themeName != name {
		m.themeName = name
		m.resolveSelectedTheme()
	}
}

// Finish the local choice before the prompt is dismissed. The caller applies
// any color change after replacing the prompt, avoiding an intermediate layout.
func (m *Model) finishThemeSelection(line string, err error) (bool, tea.Cmd) {
	picker := m.themePicker
	if picker == nil || picker.prompt != m.activePrompt {
		return false, nil
	}
	m.themePicker = nil
	name := picker.previous
	var cmd tea.Cmd
	if err == nil {
		for _, choice := range picker.prompt.choices {
			if choice.value == line {
				name = line
				cmd = m.setUIPreferences(uipreferences.Preferences{Theme: name})
				break
			}
		}
	}
	if m.themeName == name {
		return false, cmd
	}
	m.themeName = name
	return true, cmd
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
