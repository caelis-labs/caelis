package tuiapp

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func (m *Model) clearSlashArg() {
	m.clearWizard()
}

func (m *Model) openSlashArgPicker(command string) {
	cmd := strings.ToLower(strings.TrimSpace(command))
	if cmd == "" {
		return
	}
	// Check if this command has a registered wizard definition.
	if def := m.findWizard(cmd); def != nil {
		m.startWizard(def)
		return
	}
	// Fallback: simple single-step slash-arg (no wizard).
	m.clearMention()
	m.clearSkill()
	m.clearResume()
	m.clearSlashCompletion()
	m.slashArgActive = true
	m.slashArgCommand = cmd
	m.slashArgIndex = 0
	m.wizard = nil
	m.setInputText("/" + cmd + " ")
	m.syncTextareaFromInput()
	m.updateSlashArgCandidates()
}

func (m *Model) activateSlashArgPickerFromInput(command string) {
	cmd := strings.ToLower(strings.TrimSpace(command))
	if cmd == "" {
		return
	}
	if m.slashArgActive && strings.TrimSpace(m.slashArgCommand) == cmd && !m.isWizardActive() {
		m.updateSlashArgCandidates()
		return
	}
	m.clearMention()
	m.clearSkill()
	m.clearResume()
	m.clearSlashCompletion()
	m.slashArgActive = true
	m.slashArgCommand = cmd
	m.slashArgIndex = 0
	m.wizard = nil
	m.updateSlashArgCandidates()
}

func (m *Model) syncSlashInputOverlays() {
	if m.running {
		return
	}
	raw := m.textarea.Value()
	trimmed := strings.TrimSpace(raw)
	hasResumePrefix := strings.HasPrefix(raw, "/resume ")
	hasBareResumeTrigger := strings.EqualFold(trimmed, "/resume") && len(raw) > 0 && (raw[len(raw)-1] == ' ' || raw[len(raw)-1] == '\t')
	if hasResumePrefix || hasBareResumeTrigger {
		m.activateResumePickerFromInput()
		return
	}
	if m.resumeActive {
		m.clearResume()
	}
	if command, _, ok := slashArgQueryAtEnd([]rune(raw)); ok {
		m.activateSlashArgPickerFromInput(command)
		return
	}
	if m.slashArgActive && !m.isWizardActive() {
		m.clearSlashArg()
	}
}

func (m *Model) updateSlashArgCandidates() {
	if !m.slashArgActive || m.cfg.SlashArgComplete == nil || m.running {
		m.slashArgCandidates = nil
		m.slashArgQuery = ""
		m.slashArgIndex = 0
		return
	}
	// Avoid overlapping popups.
	if len(m.mentionCandidates) > 0 || len(m.skillCandidates) > 0 || len(m.resumeCandidates) > 0 {
		m.slashArgCandidates = nil
		return
	}

	// Determine the command key and query.
	command := m.slashArgCommand
	query := ""
	ok := false

	if m.isWizardActive() {
		w := m.wizard
		step := w.currentStep()
		if step == nil {
			m.slashArgCandidates = nil
			m.slashArgQuery = ""
			m.slashArgIndex = 0
			return
		}
		// Wizard steps that suppress completion.
		if w.noCompletion() {
			query, _ = wizardQueryAtCursor(w.def.Command, m.input, m.cursor)
			m.slashArgCandidates = nil
			m.slashArgQuery = query
			m.slashArgIndex = 0
			return
		}
		command = w.completionCommand()
		query, ok = wizardQueryAtCursor(w.def.Command, m.input, m.cursor)
	} else {
		// Non-wizard slash arg (simple single-step commands).
		var parsedCmd string
		parsedCmd, query, ok = slashArgQueryAtEnd([]rune(m.textarea.Value()))
		if ok {
			if parsedCmd != command {
				if isExactModelUseReasoningCommand(command, parsedCmd, query) {
					query = ""
				} else {
					ok = false
				}
			}
		}
	}
	if !ok {
		m.slashArgCandidates = nil
		m.slashArgQuery = ""
		m.slashArgIndex = 0
		return
	}
	candidates, err := m.cfg.SlashArgComplete(command, query, 200)
	if err != nil || len(candidates) == 0 {
		m.slashArgCandidates = nil
		m.slashArgQuery = query
		m.slashArgIndex = 0
		return
	}
	filtered := filterSlashArgCandidates(query, candidates)
	if len(filtered) == 0 {
		m.slashArgCandidates = nil
		m.slashArgQuery = query
		m.slashArgIndex = 0
		return
	}
	if !m.isWizardActive() && command == "model use" {
		if nextCommand, nextCandidates := m.exactModelUseReasoningCandidates(query, filtered); nextCommand != "" && len(nextCandidates) > 0 {
			query = ""
			filtered = nextCandidates
			m.slashArgCommand = nextCommand
		}
	}
	m.slashArgIndex = normalizeFilteredSelection(m.slashArgIndex, query, m.slashArgQuery, len(filtered))
	m.slashArgQuery = query
	m.slashArgCandidates = filtered
}

func isExactModelUseReasoningCommand(command string, parsedCmd string, query string) bool {
	command = strings.TrimSpace(command)
	parsedCmd = strings.TrimSpace(parsedCmd)
	query = strings.TrimSpace(query)
	if command == "" || query == "" || parsedCmd != "model use" || !strings.HasPrefix(command, "model use ") {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(strings.TrimPrefix(command, "model use ")), query)
}

func (m *Model) exactModelUseReasoningCandidates(query string, candidates []SlashArgCandidate) (string, []SlashArgCandidate) {
	query = strings.TrimSpace(query)
	if query == "" || m == nil || m.cfg.SlashArgComplete == nil {
		return "", nil
	}
	for _, candidate := range candidates {
		value := strings.TrimSpace(candidate.Value)
		display := strings.TrimSpace(candidate.Display)
		if !strings.EqualFold(query, value) && !strings.EqualFold(query, display) {
			continue
		}
		nextCommand := "model use " + value
		next, err := m.cfg.SlashArgComplete(nextCommand, "", 200)
		if err != nil || len(next) == 0 {
			return "", nil
		}
		return nextCommand, filterSlashArgCandidates("", next)
	}
	return "", nil
}

func (m *Model) applySlashArgCompletion() tea.Cmd {
	if len(m.slashArgCandidates) == 0 || strings.TrimSpace(m.slashArgCommand) == "" {
		m.updateSlashArgCandidates()
		if len(m.slashArgCandidates) == 0 || strings.TrimSpace(m.slashArgCommand) == "" {
			return nil
		}
	}
	selected, ok := m.currentSlashArgCandidate()
	if !ok {
		return nil
	}
	choice := strings.TrimSpace(selected.Value)
	if choice == "" {
		return nil
	}
	if m.isWizardActive() {
		// During a wizard, fill only the step-local query.
		m.setInputText(choice)
		m.syncTextareaFromInput()
		m.updateSlashArgCandidates()
		return nil
	}
	// Non-wizard: fill and close.
	command := strings.TrimSpace(m.slashArgCommand)
	switch command {
	case "agent":
		m.setInputText("/agent " + choice + " ")
		m.syncTextareaFromInput()
		switch choice {
		case "add", "install", "remove", "use":
			m.activateSlashArgPickerFromInput("agent " + choice)
		default:
			m.clearSlashArg()
		}
		return nil
	case "agent add", "agent install", "agent remove", "agent use":
		m.setInputText("/" + command + " " + choice)
		m.clearSlashArg()
		return nil
	case "plugin":
		if choice == "manage" {
			line := "/plugin manage"
			m.setInputText(line)
			m.syncTextareaFromInput()
			m.clearSlashArg()
			_, cmd := m.submitLine(line)
			return cmd
		}
		m.setInputText("/plugin " + choice + " ")
		m.syncTextareaFromInput()
		switch choice {
		case "marketplace", "rm":
			m.activateSlashArgPickerFromInput("plugin " + choice)
		default:
			m.clearSlashArg()
		}
		return nil
	case "plugin marketplace":
		switch choice {
		case "list":
			line := "/plugin marketplace list"
			m.setInputText(line)
			m.syncTextareaFromInput()
			m.clearSlashArg()
			_, cmd := m.submitLine(line)
			return cmd
		case "update", "rm":
			m.setInputText("/plugin marketplace " + choice + " ")
			m.syncTextareaFromInput()
			m.activateSlashArgPickerFromInput("plugin marketplace " + choice)
		default:
			m.setInputText("/plugin marketplace " + choice + " ")
			m.syncTextareaFromInput()
			m.clearSlashArg()
		}
		return nil
	case "plugin marketplace update", "plugin marketplace rm":
		m.setInputText("/" + command + " " + choice)
		m.clearSlashArg()
		return nil
	case "plugin rm":
		m.setInputText("/" + command + " " + choice)
		m.clearSlashArg()
		return nil
	case "subagent":
		m.setInputText("/subagent " + choice + " ")
		m.syncTextareaFromInput()
		switch choice {
		case "run", "bind":
			m.activateSlashArgPickerFromInput("subagent " + choice)
		default:
			m.clearSlashArg()
		}
		return nil
	case "subagent run":
		m.setInputText("/subagent run " + choice + " ")
		m.clearSlashArg()
		return nil
	case "subagent bind":
		m.setInputText("/subagent bind " + choice + " ")
		m.syncTextareaFromInput()
		m.activateSlashArgPickerFromInput("subagent bind " + choice)
		return nil
	case "model":
		m.setInputText("/model " + choice + " ")
		m.syncTextareaFromInput()
		switch choice {
		case "use":
			m.activateSlashArgPickerFromInput("model " + choice)
		case "del":
			m.activateSlashArgPickerFromInput("model " + choice)
		default:
			m.clearSlashArg()
		}
		return nil
	case "model use":
		m.setInputText("/model use " + choice + " ")
		m.syncTextareaFromInput()
		m.activateSlashArgPickerFromInput("model use " + choice)
		return nil
	case "model del":
		m.setInputText("/model del " + choice + " ")
		m.clearSlashArg()
		return nil
	case "model use ":
		m.setInputText("/model use " + choice + " ")
		m.clearSlashArg()
		return nil
	}
	if strings.HasPrefix(command, "model use ") {
		m.setInputText("/" + command + " " + choice)
		m.clearSlashArg()
		return nil
	}
	if strings.HasPrefix(command, "model del ") {
		m.setInputText("/" + command + " " + choice)
		m.clearSlashArg()
		return nil
	}
	if strings.HasPrefix(command, "subagent bind ") {
		fields := strings.Fields(command)
		if len(fields) == 3 {
			m.setInputText("/" + command + " " + choice + " ")
			m.syncTextareaFromInput()
			switch choice {
			case "model", "acp":
				m.activateSlashArgPickerFromInput(command + " " + choice)
			default:
				m.clearSlashArg()
			}
			return nil
		}
		if len(fields) == 4 {
			m.setInputText("/" + command + " " + choice + " ")
			m.syncTextareaFromInput()
			if fields[3] == "model" {
				m.activateSlashArgPickerFromInput(command + " " + choice)
			} else {
				m.clearSlashArg()
			}
			return nil
		}
		m.setInputText("/" + command + " " + choice)
		m.clearSlashArg()
		return nil
	}
	m.setInputText("/" + command + " " + choice + " ")
	m.clearSlashArg()
	return nil
}

func (m *Model) shouldExecuteSlashArgSelection(command string, choice string) bool {
	command = strings.TrimSpace(command)
	choice = strings.TrimSpace(choice)
	if command == "" || choice == "" {
		return false
	}
	current := strings.TrimSpace(m.textarea.Value())
	if current == "" {
		return false
	}
	if requiresExactSlashArgSelection(command) && !m.slashArgSelectionMatchesInput(choice) {
		return false
	}
	switch command {
	case "agent":
		return false
	case "agent add", "agent install", "agent remove", "agent use":
		return true
	case "plugin":
		return false
	case "plugin marketplace":
		return choice == "list"
	case "plugin marketplace update", "plugin marketplace rm":
		return true
	case "plugin rm":
		return true
	case "subagent":
		return false
	case "subagent run":
		return false
	case "subagent bind":
		return false
	case "model":
		return false
	case "model use":
		return false
	case "model del":
		return true
	}
	if strings.HasPrefix(command, "model use ") || strings.HasPrefix(command, "model del ") {
		return true
	}
	if strings.HasPrefix(command, "subagent bind ") {
		fields := strings.Fields(command)
		if len(fields) == 3 {
			return choice == "default" || choice == "self" || choice == "builtin" || choice == "built-in"
		}
		if len(fields) == 4 {
			return fields[3] == "acp"
		}
		return true
	}
	return true
}

func requiresExactSlashArgSelection(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "agent remove", "agent rm", "model del", "plugin rm", "plugin marketplace update", "plugin marketplace rm":
		return true
	default:
		return false
	}
}

func (m *Model) slashArgSelectionMatchesInput(choice string) bool {
	current := strings.TrimSpace(m.textarea.Value())
	expected := strings.TrimSpace(m.suggestedSlashArgInput(choice))
	return current != "" && expected != "" && current == expected
}

func isExecutableSlashArgInput(line string) bool {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(fields[0])) {
	case "/agent":
		action := ""
		if len(fields) >= 2 {
			action = strings.ToLower(strings.TrimSpace(fields[1]))
		}
		switch action {
		case "list":
			return len(fields) == 2
		case "add", "install", "remove", "use":
			return len(fields) >= 3
		default:
			return false
		}
	case "/model":
		action := strings.ToLower(strings.TrimSpace(fields[1]))
		switch action {
		case "use":
			return len(fields) >= 3
		case "del":
			return len(fields) >= 3
		default:
			return false
		}
	case "/plugin":
		action := strings.ToLower(strings.TrimSpace(fields[1]))
		switch action {
		case "manage":
			return len(fields) == 2
		case "install", "rm":
			return len(fields) >= 3
		case "marketplace":
			if len(fields) < 3 {
				return false
			}
			marketplaceAction := strings.ToLower(strings.TrimSpace(fields[2]))
			switch marketplaceAction {
			case "list":
				return len(fields) == 3
			case "add", "update", "rm":
				return len(fields) >= 4
			default:
				return false
			}
		default:
			return false
		}
	case "/subagent":
		action := strings.ToLower(strings.TrimSpace(fields[1]))
		switch action {
		case "list":
			return len(fields) == 2
		case "run":
			return len(fields) >= 4
		case "bind":
			if len(fields) < 4 {
				return false
			}
			target := strings.ToLower(strings.TrimSpace(fields[3]))
			switch target {
			case "default", "self", "builtin", "built-in":
				return len(fields) == 4
			case "model":
				return len(fields) == 5 || len(fields) == 6
			case "acp":
				return len(fields) == 5
			default:
				return false
			}
		default:
			return false
		}
	default:
		return false
	}
}

func (m *Model) handleSlashArgKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if m.slashArgActive && strings.TrimSpace(m.slashArgCommand) == "" && !m.isWizardActive() {
		m.clearSlashArg()
		return false, nil
	}
	switch {
	case key.Matches(msg, m.keys.Back):
		if m.slashArgActive {
			m.setInputText("")
			m.syncTextareaFromInput()
		}
		m.clearSlashArg()
		return true, nil
	case key.Matches(msg, m.keys.ChoosePrev):
		if len(m.slashArgCandidates) > 0 {
			m.slashArgIndex = wrapSelectionIndex(m.slashArgIndex, len(m.slashArgCandidates), -1)
		}
		return true, nil
	case key.Matches(msg, m.keys.ChooseNext):
		if len(m.slashArgCandidates) > 0 {
			m.slashArgIndex = wrapSelectionIndex(m.slashArgIndex, len(m.slashArgCandidates), 1)
		}
		return true, nil
	case key.Matches(msg, m.keys.Complete):
		cmd := m.applySlashArgCompletion()
		m.syncTextareaFromInput()
		return true, cmd
	case key.Matches(msg, m.keys.Accept):
		if m.running || strings.TrimSpace(m.slashArgCommand) == "" {
			return true, nil
		}
		if !m.isWizardActive() {
			m.updateSlashArgCandidates()
		}
		// Delegate to wizard engine if active.
		if m.isWizardActive() {
			handled, cmd := m.handleWizardEnter()
			return handled, cmd
		}
		line := strings.TrimSpace(m.textarea.Value())
		if len(m.slashArgCandidates) == 0 && isExecutableSlashArgInput(line) {
			m.clearSlashArg()
			_, cmd := m.submitLine(line)
			return true, cmd
		}
		// Non-wizard: single-step slash arg.
		selected := ""
		if candidate, ok := m.currentSlashArgCandidate(); ok {
			selected = strings.TrimSpace(candidate.Value)
		}
		if selected == "" {
			return true, nil
		}
		command := strings.TrimSpace(m.slashArgCommand)
		if m.shouldExecuteSlashArgSelection(command, selected) {
			cmd := m.applySlashArgCompletion()
			m.syncTextareaFromInput()
			if cmd != nil {
				return true, cmd
			}
			line = strings.TrimSpace(m.textarea.Value())
			m.clearSlashArg()
			_, submitCmd := m.submitLine(line)
			return true, submitCmd
		}
		if command == "agent" || command == "plugin" || command == "plugin marketplace" || command == "plugin marketplace update" || command == "plugin marketplace rm" || command == "subagent" || command == "subagent run" || command == "subagent bind" || strings.HasPrefix(command, "subagent bind ") || command == "model" || command == "model use" || command == "model del" || strings.HasPrefix(command, "model use ") || strings.HasPrefix(command, "model del ") {
			cmd := m.applySlashArgCompletion()
			m.syncTextareaFromInput()
			return true, cmd
		}
		cmd := m.applySlashArgCompletion()
		m.syncTextareaFromInput()
		return true, cmd
	default:
		return false, nil
	}
}

func (m *Model) renderSlashArgList() string {
	candidates := m.visibleSlashArgCandidates()
	if len(candidates) == 0 {
		return ""
	}
	index := m.currentSlashArgIndex(candidates)
	maxItems := minInt(8, len(candidates))
	start := 0
	if index >= maxItems {
		start = index - maxItems + 1
	}
	maxStart := maxInt(0, len(candidates)-maxItems)
	if start > maxStart {
		start = maxStart
	}
	end := minInt(len(candidates), start+maxItems)
	var lines []string
	if start > 0 {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("  … and %d earlier", start),
		))
	}
	for i := start; i < end; i++ {
		display := strings.TrimSpace(candidates[i].Display)
		if display == "" {
			display = strings.TrimSpace(candidates[i].Value)
		}
		detail := strings.TrimSpace(candidates[i].Detail)
		prefix := "  "
		if i == index {
			prefix = m.theme.PromptStyle().Render("▸ ")
			line := prefix + m.theme.CommandActiveStyle().Render(display)
			if detail != "" {
				line += "  " + m.theme.HelpHintTextStyle().Render(detail)
			}
			lines = append(lines, line)
		} else {
			line := prefix + m.theme.CommandStyle().Render(display)
			if detail != "" {
				line += "  " + m.theme.HelpHintTextStyle().Render(detail)
			}
			lines = append(lines, line)
		}
	}
	if end < len(candidates) {
		lines = append(lines, m.theme.HelpHintTextStyle().Render(
			fmt.Sprintf("  … and %d more", len(candidates)-end),
		))
	}
	title := "Options"
	if m.isWizardActive() && m.wizard != nil {
		if step := m.wizard.currentStep(); step != nil {
			title = strings.TrimSpace(step.HintLabel)
		}
		if title == "" {
			title = "/" + strings.TrimSpace(m.wizard.def.Command)
		}
	} else {
		title = "/" + strings.TrimSpace(m.slashArgCommand)
		if title == "/" {
			title = "Options"
		}
	}
	return m.renderCompletionOverlay(title, lines)
}

func (m *Model) currentSlashArgCandidate() (SlashArgCandidate, bool) {
	candidates := m.visibleSlashArgCandidates()
	if len(candidates) == 0 {
		return SlashArgCandidate{}, false
	}
	index := m.currentSlashArgIndex(candidates)
	if index < 0 || index >= len(candidates) {
		return SlashArgCandidate{}, false
	}
	return candidates[index], true
}

func (m *Model) currentSlashArgIndex(candidates []SlashArgCandidate) int {
	if len(candidates) == 0 {
		return 0
	}
	index := m.slashArgIndex
	if index < 0 {
		index = 0
	}
	if index >= len(candidates) {
		index = len(candidates) - 1
	}
	return index
}

func (m *Model) visibleSlashArgCandidates() []SlashArgCandidate {
	if len(m.slashArgCandidates) == 0 {
		return nil
	}
	if m.isWizardActive() {
		return m.slashArgCandidates
	}
	_, query, ok := slashArgQueryAtEnd([]rune(m.textarea.Value()))
	if !ok {
		return m.slashArgCandidates
	}
	filtered := filterSlashArgCandidates(query, m.slashArgCandidates)
	if len(filtered) == 0 {
		return m.slashArgCandidates
	}
	return filtered
}
