package tuiapp

import (
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

type slashCompletionCandidates struct {
	commands []string
	displays map[string]string
	details  map[string]string
}

func (m *Model) refreshSlashCommands() {
	selected := ""
	if m.slashIndex >= 0 && m.slashIndex < len(m.slashCandidates) {
		selected = strings.TrimSpace(m.slashCandidates[m.slashIndex])
	}
	m.clearSlashCompletion()
	if m.turnRunning() || m.slashArgActive || m.isWizardActive() {
		return
	}
	if len(m.mentionCandidates) > 0 || len(m.resumeCandidates) > 0 || len(m.slashArgCandidates) > 0 {
		return
	}
	query, ok := slashCommandQueryAtCursor(m.input, m.cursor)
	if !ok {
		return
	}

	assembled := assembleSlashCompletionCandidates(m.cfg.Commands, m.slashSkillCatalog, query)
	if len(assembled.commands) == 0 {
		return
	}
	m.slashCandidates = assembled.commands
	m.slashDisplays = assembled.displays
	m.slashDetails = assembled.details
	m.slashIndex = 0
	if selected != "" {
		for i, candidate := range assembled.commands {
			if candidate == selected {
				m.slashIndex = i
				break
			}
		}
	}
	m.slashPrefix = "/" + query
}

// requestSlashSkillCatalog loads the immutable Runtime skill snapshot outside
// the Bubble Tea update loop. Built-in slash commands are always refreshed
// immediately; the skill results are merged when this command completes.
func (m *Model) requestSlashSkillCatalog() tea.Cmd {
	if m == nil || m.cfg.SkillComplete == nil || m.slashSkillLoaded || m.slashSkillLoadPending || m.turnRunning() {
		return nil
	}
	if _, ok := slashCommandQueryAtCursor(m.input, m.cursor); !ok {
		return nil
	}
	m.slashSkillLoadSeq++
	seq := m.slashSkillLoadSeq
	m.slashSkillLoadPending = true
	complete := m.cfg.SkillComplete
	return func() tea.Msg {
		started := time.Now()
		candidates, err := complete("", completionCandidateMaxLimit)
		return slashSkillCompletionResultMsg{
			seq:        seq,
			candidates: append([]CompletionCandidate(nil), candidates...),
			err:        err,
			latency:    time.Since(started),
		}
	}
}

func (m *Model) handleSlashSkillCompletionResultMsg(msg slashSkillCompletionResultMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.seq != m.slashSkillLoadSeq || !m.slashSkillLoadPending {
		return m, nil
	}
	m.slashSkillLoadPending = false
	m.diag.LastSlashSkillLatency = msg.latency
	if msg.err != nil {
		return m, nil
	}
	m.slashSkillLoaded = true
	m.slashSkillCatalog = append(m.slashSkillCatalog[:0], msg.candidates...)
	m.refreshSlashCommands()
	return m, nil
}

func (m *Model) resetSlashSkillCatalog() {
	if m == nil {
		return
	}
	m.slashSkillLoadSeq++
	m.slashSkillCatalog = nil
	m.slashSkillLoaded = false
	m.slashSkillLoadPending = false
}

func assembleSlashCompletionCandidates(commands []string, skills []CompletionCandidate, query string) slashCompletionCandidates {
	candidates := make([]string, 0, len(commands)+len(skills))
	seen := make(map[string]struct{}, len(commands)+len(skills))
	builtinNames := make(map[string]struct{}, len(commands))
	queryKey := strings.ToLower(strings.TrimSpace(query))
	for _, command := range commands {
		full := "/" + strings.TrimSpace(command)
		if full == "/" {
			continue
		}
		key := strings.ToLower(strings.TrimPrefix(full, "/"))
		builtinNames[key] = struct{}{}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		if queryKey == "" || strings.HasPrefix(strings.ToLower(full), "/"+queryKey) {
			candidates = append(candidates, full)
		}
	}

	var displays map[string]string
	var details map[string]string
	for _, candidate := range skills {
		command := strings.TrimSpace(candidate.Value)
		key := strings.ToLower(command)
		if command == "" || strings.ContainsAny(command, " \t\r\n") {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		display := strings.TrimSpace(candidate.Display)
		if display == "" {
			display = command
		}
		displayKey := strings.ToLower(display)
		if _, builtin := builtinNames[displayKey]; builtin {
			display = command
		}
		if queryKey != "" && !strings.HasPrefix(key, queryKey) && !strings.HasPrefix(displayKey, queryKey) {
			continue
		}
		seen[key] = struct{}{}
		candidates = append(candidates, "/"+command)
		if !strings.EqualFold(display, command) {
			if displays == nil {
				displays = map[string]string{}
			}
			displays[key] = "/" + display
		}
		if detail := formatSlashSkillDetail(candidate.Kind, candidate.Detail); detail != "" {
			if details == nil {
				details = map[string]string{}
			}
			details[key] = detail
		}
	}
	sort.Strings(candidates)
	return slashCompletionCandidates{commands: candidates, displays: displays, details: details}
}

func formatSlashSkillDetail(kind string, detail string) string {
	kind = strings.TrimSpace(kind)
	detail = strings.TrimSpace(detail)
	switch {
	case kind != "" && detail != "":
		return kind + " · " + detail
	case kind != "":
		return kind
	default:
		return detail
	}
}

func (m *Model) applySlashCommandCompletion() tea.Cmd {
	if len(m.slashCandidates) == 0 {
		m.refreshSlashCommands()
		if len(m.slashCandidates) == 0 {
			return nil
		}
	}
	selected := strings.TrimSpace(m.slashCandidates[m.slashIndex])
	if selected == "" {
		return nil
	}
	if strings.EqualFold(strings.TrimPrefix(selected, "/"), "subagent") {
		m.clearSlashCompletion()
		m.resetComposerAfterOverlayOpen()
		return m.openSubagentOverlay()
	}
	line := selected + " "
	m.setInputText(line)
	m.clearSlashCompletion()
	m.tryOpenSlashArgPicker(line)
	return nil
}

func (m *Model) handleSlashCommandKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back):
		if _, ok := slashCommandQueryAtCursor(m.input, m.cursor); ok {
			m.setInputText("")
			m.syncTextareaFromInput()
		}
		m.clearSlashCompletion()
		return true, nil
	case key.Matches(msg, m.keys.ChoosePrev):
		if len(m.slashCandidates) > 0 {
			m.moveActiveCompletionSelection(-1, true)
		}
		return true, nil
	case key.Matches(msg, m.keys.ChooseNext):
		if len(m.slashCandidates) > 0 {
			m.moveActiveCompletionSelection(1, true)
		}
		return true, nil
	case key.Matches(msg, m.keys.Complete):
		cmd := m.applySlashCommandCompletion()
		m.syncTextareaFromInput()
		if cmd != nil {
			return true, cmd
		}
		if m.resumeActive {
			return true, m.requestCompletionRefresh()
		}
		return true, nil
	case key.Matches(msg, m.keys.Accept):
		if m.turnRunning() || len(m.slashCandidates) == 0 {
			return true, nil
		}
		cmd := m.applySlashCommandCompletion()
		m.syncTextareaFromInput()
		if cmd != nil {
			return true, cmd
		}
		if m.resumeActive {
			return true, m.requestCompletionRefresh()
		}
		return true, nil
	default:
		return false, nil
	}
}

func (m *Model) renderSlashCommandList() string {
	return m.renderCompletionKind(completionSlashCommand)
}

func (m *Model) renderSlashCommandListGeometry(geometry completionOverlayGeometry, candidates []string) string {
	lines := make([]string, 0, geometry.candidateCount)
	for i := geometry.windowStart; i < geometry.windowEnd; i++ {
		command := candidates[i]
		lines = append(lines, m.renderCompletionTextLine(
			m.slashCommandDisplay(command),
			m.commandCompletionDetail(command),
			i == geometry.selected,
		))
	}
	return m.renderCompletionOverlay(geometry, lines)
}

func (m *Model) slashCommandDisplay(command string) string {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(command), "/"))
	if display := strings.TrimSpace(m.slashDisplays[name]); display != "" {
		return display
	}
	return command
}

func (m *Model) clearSlashCompletion() {
	m.slashCandidates = nil
	m.slashDisplays = nil
	m.slashDetails = nil
	m.slashIndex = 0
	m.slashPrefix = ""
}
