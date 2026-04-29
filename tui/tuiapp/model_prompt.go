package tuiapp

import (
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// ---------------------------------------------------------------------------
// External prompt handling
// ---------------------------------------------------------------------------

func (m *Model) enqueuePrompt(req PromptRequestMsg) {
	if req.Response == nil {
		return
	}
	if m.activePrompt == nil {
		m.activePrompt = newPromptState(req)
		return
	}
	m.pendingPrompt = append(m.pendingPrompt, req)
}

func (m *Model) finishPrompt(line string, err error) {
	if m.activePrompt == nil {
		return
	}
	resp := m.activePrompt.response
	if resp != nil {
		resp <- PromptResponse{Line: line, Err: err}
	}
	if len(m.pendingPrompt) == 0 {
		m.activePrompt = nil
		m.ensureViewportLayout()
		m.syncViewportContent()
		return
	}
	next := m.pendingPrompt[0]
	m.pendingPrompt = m.pendingPrompt[1:]
	m.activePrompt = newPromptState(next)
	m.ensureViewportLayout()
	m.syncViewportContent()
}

func (m *Model) handlePromptKey(msg tea.KeyMsg) tea.Cmd {
	if m.activePrompt == nil {
		return nil
	}
	if len(m.activePrompt.choices) > 0 {
		return m.handlePromptChoiceKey(msg)
	}
	switch msg.String() {
	case "ctrl+c", "esc":
		m.finishPrompt("", errors.New(PromptErrInterrupt))
		return nil
	case "ctrl+d":
		if len(m.activePrompt.input) == 0 {
			m.finishPrompt("", errors.New(PromptErrEOF))
			return nil
		}
		if m.activePrompt.cursor < len(m.activePrompt.input) {
			m.activePrompt.input = append(m.activePrompt.input[:m.activePrompt.cursor], m.activePrompt.input[m.activePrompt.cursor+1:]...)
		}
		return nil
	case "enter":
		m.finishPrompt(strings.TrimSpace(string(m.activePrompt.input)), nil)
		return nil
	case "left":
		if m.activePrompt.cursor > 0 {
			m.activePrompt.cursor--
		}
		return nil
	case "right":
		if m.activePrompt.cursor < len(m.activePrompt.input) {
			m.activePrompt.cursor++
		}
		return nil
	case "home", "ctrl+a":
		m.activePrompt.cursor = 0
		return nil
	case "end", "ctrl+e":
		m.activePrompt.cursor = len(m.activePrompt.input)
		return nil
	case "backspace":
		if m.activePrompt.cursor > 0 {
			m.activePrompt.input = append(m.activePrompt.input[:m.activePrompt.cursor-1], m.activePrompt.input[m.activePrompt.cursor:]...)
			m.activePrompt.cursor--
		}
		return nil
	case "delete":
		if m.activePrompt.cursor >= 0 && m.activePrompt.cursor < len(m.activePrompt.input) {
			m.activePrompt.input = append(m.activePrompt.input[:m.activePrompt.cursor], m.activePrompt.input[m.activePrompt.cursor+1:]...)
		}
		return nil
	case "ctrl+u":
		m.activePrompt.input = m.activePrompt.input[:0]
		m.activePrompt.cursor = 0
		return nil
	}
	if text := msg.Key().Text; text != "" {
		for _, r := range text {
			head := append([]rune(nil), m.activePrompt.input[:m.activePrompt.cursor]...)
			head = append(head, r)
			m.activePrompt.input = append(head, m.activePrompt.input[m.activePrompt.cursor:]...)
			m.activePrompt.cursor++
		}
	}
	return nil
}

func (m *Model) handlePromptPaste(msg tea.PasteMsg) tea.Cmd {
	if m.activePrompt == nil {
		return nil
	}
	text := msg.String()
	if text == "" {
		return nil
	}
	if len(m.activePrompt.choices) > 0 && m.activePrompt.filterable {
		for _, r := range text {
			head := append([]rune(nil), m.activePrompt.filter[:m.activePrompt.cursor]...)
			head = append(head, r)
			m.activePrompt.filter = append(head, m.activePrompt.filter[m.activePrompt.cursor:]...)
			m.activePrompt.cursor++
		}
		m.clampPromptChoiceIndex()
		return nil
	}
	if len(m.activePrompt.choices) > 0 {
		return nil
	}
	for _, r := range text {
		head := append([]rune(nil), m.activePrompt.input[:m.activePrompt.cursor]...)
		head = append(head, r)
		m.activePrompt.input = append(head, m.activePrompt.input[m.activePrompt.cursor:]...)
		m.activePrompt.cursor++
	}
	return nil
}

func newPromptState(req PromptRequestMsg) *promptState {
	state := &promptState{
		title:              strings.TrimSpace(req.Title),
		prompt:             req.Prompt,
		details:            append([]PromptDetail(nil), req.Details...),
		secret:             req.Secret,
		response:           req.Response,
		filterable:         req.Filterable,
		multiSelect:        req.MultiSelect,
		allowFreeformInput: req.AllowFreeformInput,
		selected:           map[string]struct{}{},
	}
	if state.title == "" {
		state.title = strings.TrimSpace(req.Prompt)
	}
	if req.Secret {
		return state
	}
	if len(req.Choices) > 0 {
		state.choices = make([]promptChoice, 0, len(req.Choices))
		for _, choice := range req.Choices {
			label := strings.TrimSpace(choice.Label)
			value := strings.TrimSpace(choice.Value)
			if label == "" {
				label = value
			}
			if value == "" {
				value = label
			}
			if value == "" {
				continue
			}
			state.choices = append(state.choices, promptChoice{
				label:         label,
				value:         value,
				detail:        strings.TrimSpace(choice.Detail),
				alwaysVisible: choice.AlwaysVisible,
			})
		}
		for _, selected := range req.SelectedChoices {
			selected = strings.TrimSpace(selected)
			if selected == "" {
				continue
			}
			state.selected[selected] = struct{}{}
		}
		state.choiceIndex = promptChoiceIndexByValue(state.choices, req.DefaultChoice)
		clampPromptChoiceWindow(state, len(state.choices))
		return state
	}
	if choices, idx, ok := parsePromptChoices(req.Prompt); ok {
		state.choices = choices
		state.choiceIndex = idx
		clampPromptChoiceWindow(state, len(state.choices))
	}
	return state
}

func promptChoiceIndexByValue(choices []promptChoice, value string) int {
	target := strings.TrimSpace(value)
	if target == "" {
		return 0
	}
	for i, choice := range choices {
		if choice.value == target {
			return i
		}
	}
	return 0
}

func parsePromptChoices(prompt string) ([]promptChoice, int, bool) {
	normalized := strings.ToLower(strings.Join(strings.Fields(prompt), " "))
	if strings.Contains(normalized, "[y] allow") &&
		strings.Contains(normalized, "[a] always") &&
		strings.Contains(normalized, "[n] deny") {
		return []promptChoice{
			{label: "allow", value: "y"},
			{label: "always", value: "a"},
			{label: "deny", value: "n"},
		}, 2, true
	}
	if strings.Contains(normalized, "once") &&
		strings.Contains(normalized, "session") &&
		strings.Contains(normalized, "cancel") {
		return []promptChoice{
			{label: "once", value: "y"},
			{label: "session", value: "a"},
			{label: "cancel", value: "n"},
		}, 2, true
	}
	return nil, 0, false
}

func (m *Model) handlePromptChoiceKey(msg tea.KeyMsg) tea.Cmd {
	if m.activePrompt == nil || len(m.activePrompt.choices) == 0 {
		return nil
	}
	visible := m.visiblePromptChoices()
	if len(visible) == 0 {
		m.activePrompt.choiceIndex = 0
	}
	switch msg.String() {
	case "ctrl+c", "esc":
		m.finishPrompt("", errors.New(PromptErrInterrupt))
		return nil
	case "ctrl+d":
		m.finishPrompt("", errors.New(PromptErrEOF))
		return nil
	case "left":
		if m.activePrompt.filterable && m.activePrompt.cursor > 0 {
			m.activePrompt.cursor--
		}
		return nil
	case "right":
		if m.activePrompt.filterable && m.activePrompt.cursor < len(m.activePrompt.filter) {
			m.activePrompt.cursor++
		}
		return nil
	case "home", "ctrl+a":
		if m.activePrompt.filterable {
			m.activePrompt.cursor = 0
		}
		return nil
	case "end", "ctrl+e":
		if m.activePrompt.filterable {
			m.activePrompt.cursor = len(m.activePrompt.filter)
		}
		return nil
	case "backspace":
		if m.activePrompt.filterable && m.activePrompt.cursor > 0 {
			m.activePrompt.filter = append(m.activePrompt.filter[:m.activePrompt.cursor-1], m.activePrompt.filter[m.activePrompt.cursor:]...)
			m.activePrompt.cursor--
			m.clampPromptChoiceIndex()
		}
		return nil
	case "delete":
		if m.activePrompt.filterable && m.activePrompt.cursor >= 0 && m.activePrompt.cursor < len(m.activePrompt.filter) {
			m.activePrompt.filter = append(m.activePrompt.filter[:m.activePrompt.cursor], m.activePrompt.filter[m.activePrompt.cursor+1:]...)
			m.clampPromptChoiceIndex()
		}
		return nil
	case "ctrl+u":
		if m.activePrompt.filterable {
			m.activePrompt.filter = m.activePrompt.filter[:0]
			m.activePrompt.cursor = 0
			m.clampPromptChoiceIndex()
		}
		return nil
	case "up", "k", "shift+tab", "backtab":
		if len(visible) > 0 {
			m.activePrompt.choiceIndex = wrapSelectionIndex(m.activePrompt.choiceIndex, len(visible), -1)
			m.syncPromptChoiceWindow()
		}
		return nil
	case "down", "j", "tab":
		if len(visible) > 0 {
			m.activePrompt.choiceIndex = wrapSelectionIndex(m.activePrompt.choiceIndex, len(visible), 1)
			m.syncPromptChoiceWindow()
		}
		return nil
	case " ", "space":
		if !m.activePrompt.multiSelect {
			return nil
		}
		visible = m.visiblePromptChoices()
		if len(visible) == 0 {
			return nil
		}
		choice := visible[m.activePrompt.choiceIndex]
		if _, ok := m.activePrompt.selected[choice.value]; ok {
			delete(m.activePrompt.selected, choice.value)
		} else {
			m.activePrompt.selected[choice.value] = struct{}{}
		}
		return nil
	case "enter":
		visible = m.visiblePromptChoices()
		if m.activePrompt.multiSelect && len(m.activePrompt.selected) == 0 {
			filterValue := strings.TrimSpace(string(m.activePrompt.filter))
			if custom, ok := firstAlwaysVisibleChoice(m.activePrompt.choices); ok && (filterValue == "" || len(visible) == 0 || promptChoicesOnlyAlwaysVisible(visible)) {
				m.finishPrompt(custom.value, nil)
				return nil
			}
		}
		if len(visible) == 0 {
			return nil
		}
		if m.activePrompt.multiSelect {
			if len(m.activePrompt.selected) == 0 {
				m.activePrompt.selected[visible[m.activePrompt.choiceIndex].value] = struct{}{}
			}
			m.finishPrompt(strings.Join(m.selectedPromptChoices(), ","), nil)
			return nil
		}
		choice := visible[m.activePrompt.choiceIndex]
		m.finishPrompt(choice.value, nil)
		return nil
	}
	if text := msg.Key().Text; text != "" {
		if m.activePrompt.filterable {
			for _, r := range text {
				head := append([]rune(nil), m.activePrompt.filter[:m.activePrompt.cursor]...)
				head = append(head, r)
				m.activePrompt.filter = append(head, m.activePrompt.filter[m.activePrompt.cursor:]...)
				m.activePrompt.cursor++
			}
			m.clampPromptChoiceIndex()
			return nil
		}
		key := strings.ToLower(strings.TrimSpace(text))
		for _, choice := range visible {
			if choice.value == key {
				m.finishPrompt(choice.value, nil)
				return nil
			}
		}
	}
	return nil
}

func (m *Model) visiblePromptChoices() []promptChoice {
	if m.activePrompt == nil || len(m.activePrompt.choices) == 0 {
		return nil
	}
	if !m.activePrompt.filterable {
		return m.activePrompt.choices
	}
	query := strings.ToLower(strings.TrimSpace(string(m.activePrompt.filter)))
	if query == "" {
		return m.activePrompt.choices
	}
	out := make([]promptChoice, 0, len(m.activePrompt.choices))
	pinned := make([]promptChoice, 0, 1)
	for _, choice := range m.activePrompt.choices {
		if choice.alwaysVisible {
			pinned = append(pinned, choice)
		}
		text := strings.ToLower(strings.TrimSpace(choice.label + " " + choice.value + " " + choice.detail))
		if strings.Contains(text, query) {
			out = append(out, choice)
		}
	}
	if len(pinned) == 0 {
		return out
	}
	seen := make(map[string]struct{}, len(out))
	for _, choice := range out {
		seen[choice.value] = struct{}{}
	}
	for _, choice := range pinned {
		if _, ok := seen[choice.value]; ok {
			continue
		}
		out = append(out, choice)
	}
	return out
}

func promptChoicesOnlyAlwaysVisible(choices []promptChoice) bool {
	if len(choices) == 0 {
		return false
	}
	for _, choice := range choices {
		if !choice.alwaysVisible {
			return false
		}
	}
	return true
}

func firstAlwaysVisibleChoice(choices []promptChoice) (promptChoice, bool) {
	for _, choice := range choices {
		if choice.alwaysVisible {
			return choice, true
		}
	}
	return promptChoice{}, false
}

func (m *Model) clampPromptChoiceIndex() {
	if m.activePrompt == nil {
		return
	}
	visible := m.visiblePromptChoices()
	if len(visible) == 0 {
		m.activePrompt.choiceIndex = 0
		m.activePrompt.scrollOffset = 0
		return
	}
	if m.activePrompt.choiceIndex >= len(visible) {
		m.activePrompt.choiceIndex = len(visible) - 1
	}
	if m.activePrompt.choiceIndex < 0 {
		m.activePrompt.choiceIndex = 0
	}
	m.syncPromptChoiceWindow()
}

func (m *Model) selectedPromptChoices() []string {
	if m.activePrompt == nil || len(m.activePrompt.selected) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.activePrompt.selected))
	for _, choice := range m.activePrompt.choices {
		if _, ok := m.activePrompt.selected[choice.value]; ok {
			out = append(out, choice.value)
		}
	}
	return out
}

func (m *Model) syncPromptChoiceWindow() {
	if m.activePrompt == nil {
		return
	}
	clampPromptChoiceWindow(m.activePrompt, len(m.visiblePromptChoices()))
}

func clampPromptChoiceWindow(state *promptState, visibleCount int) {
	if state == nil {
		return
	}
	const maxVisiblePromptChoices = 8
	if visibleCount <= 0 {
		state.choiceIndex = 0
		state.scrollOffset = 0
		return
	}
	if state.choiceIndex < 0 {
		state.choiceIndex = 0
	}
	if state.choiceIndex >= visibleCount {
		state.choiceIndex = visibleCount - 1
	}
	maxOffset := visibleCount - maxVisiblePromptChoices
	if maxOffset < 0 {
		maxOffset = 0
	}
	if state.scrollOffset > maxOffset {
		state.scrollOffset = maxOffset
	}
	if state.scrollOffset < 0 {
		state.scrollOffset = 0
	}
	if state.choiceIndex < state.scrollOffset {
		state.scrollOffset = state.choiceIndex
	}
	if state.choiceIndex >= state.scrollOffset+maxVisiblePromptChoices {
		state.scrollOffset = state.choiceIndex - maxVisiblePromptChoices + 1
	}
}

func wrapSelectionIndex(current int, count int, delta int) int {
	if count <= 0 {
		return 0
	}
	next := (current + delta) % count
	if next < 0 {
		next += count
	}
	return next
}
