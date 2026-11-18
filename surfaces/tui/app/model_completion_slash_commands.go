package tuiapp

import (
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// slashCommandOrder keeps related actions adjacent within the Commands group.
var slashCommandOrder = []string{
	"/help",
	"/resume", "/new", "/status", "/compact", "/btw",
	"/model", "/connect", "/disconnect",
	"/team", "/review",
	"/plugin", "/theme", "/experimental",
	"/doctor", "/quit",
}

type slashCompletionCandidates struct {
	commands []string
	displays map[string]string
	details  map[string]string
	groups   []slashCompletionGroup
}

type slashCompletionGroup struct {
	start int
	title string
}

func (m *Model) refreshSlashCommands() {
	selected := ""
	if m.slashIndex >= 0 && m.slashIndex < len(m.slashCandidates) {
		selected = strings.TrimSpace(m.slashCandidates[m.slashIndex])
	}
	m.clearSlashCompletion()
	if m.slashArgActive || m.isWizardActive() {
		return
	}
	if len(m.mentionCandidates) > 0 || len(m.slashArgCandidates) > 0 {
		return
	}
	query, start, end, skillOnly, ok := slashCompletionTargetAtCursor(m.input, m.cursor)
	if !ok {
		return
	}

	commands := m.slashCommandsForCompletion(skillOnly)
	reserved := commands
	if !skillOnly {
		reserved = append([]string(nil), m.cfg.Commands...)
	}
	if _, lineStart := slashCommandQueryAtCursor(m.input, m.cursor); lineStart {
		commands = append(commands, "theme")
		reserved = append(reserved, "theme")
	}
	assembled := assembleSlashCompletionCandidates(commands, reserved, m.slashSkillCatalog, query, skillOnly)
	if len(assembled.commands) == 0 {
		return
	}
	m.slashCandidates = assembled.commands
	m.slashDisplays = assembled.displays
	m.slashDetails = assembled.details
	m.slashGroups = assembled.groups
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
	m.slashSkillOnly = skillOnly
	m.slashStart = start
	m.slashEnd = end
}

func (m *Model) slashCommandsForCompletion(skillOnly bool) []string {
	if skillOnly {
		return nil
	}
	commands := append([]string(nil), m.cfg.Commands...)
	if !m.turnRunning() {
		return commands
	}
	filtered := make([]string, 0, len(commands))
	for _, command := range commands {
		spec, ok := controlprompt.Lookup(command)
		if ok && spec.AvailableWhileRunning {
			filtered = append(filtered, command)
		}
	}
	return filtered
}

// requestSlashSkillCatalog loads the immutable Runtime skill snapshot outside
// the Bubble Tea update loop. Built-in slash commands are always refreshed
// immediately; the skill results are merged when this command completes.
func (m *Model) requestSlashSkillCatalog() tea.Cmd {
	if m == nil || m.cfg.SkillComplete == nil || m.slashSkillLoaded || m.slashSkillLoadPending {
		return nil
	}
	if _, _, _, _, ok := slashCompletionTargetAtCursor(m.input, m.cursor); !ok {
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

func assembleSlashCompletionCandidates(commands []string, reserved []string, skills []CompletionCandidate, query string, skillOnly bool) slashCompletionCandidates {
	var commandNames, agentNames, skillNames []string
	seen := make(map[string]struct{}, len(commands)+len(skills)+len(reserved))
	builtinNames := make(map[string]struct{}, len(commands)+len(reserved))
	var displays map[string]string
	var details map[string]string
	queryKey := strings.ToLower(strings.TrimSpace(query))
	for _, command := range commands {
		name := strings.TrimSpace(command)
		var aliases []string
		spec, known := controlprompt.Lookup(name)
		if known {
			name, aliases = spec.Name, spec.Aliases
		}
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		builtinNames[key] = struct{}{}
		for _, alias := range aliases {
			// Canonical built-ins outrank same-name skills, and an alias never
			// becomes a separate candidate, so reserve alias keys up front.
			builtinNames[alias] = struct{}{}
			seen[alias] = struct{}{}
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		full := "/" + name
		if !commandMatchesQuery(full, aliases, queryKey) {
			continue
		}
		switch {
		case name == "theme" || name == "btw", known && !agentbinding.IsDirectRun(agentbinding.Handle(name)):
			commandNames = append(commandNames, full)
		default:
			agentNames = append(agentNames, full)
		}
		if len(aliases) > 0 {
			if displays == nil {
				displays = map[string]string{}
			}
			displays[key] = full + " (" + strings.Join(aliases, ", ") + ")"
		}
	}
	for _, command := range reserved {
		name := strings.TrimSpace(command)
		var aliases []string
		if spec, ok := controlprompt.Lookup(name); ok {
			name, aliases = spec.Name, spec.Aliases
		}
		if name == "" {
			continue
		}
		key := strings.ToLower(name)
		builtinNames[key] = struct{}{}
		seen[key] = struct{}{}
		for _, alias := range aliases {
			// Reserved commands may be filtered out of the visible list while a
			// Turn runs, but their aliases must still suppress same-name skills.
			builtinNames[alias] = struct{}{}
			seen[alias] = struct{}{}
		}
	}

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
		skillNames = append(skillNames, "/"+command)
		if !strings.EqualFold(display, command) {
			if displays == nil {
				displays = map[string]string{}
			}
			displays[key] = "/" + display
		}
		detail := strings.TrimSpace(candidate.Detail)
		if skillOnly {
			detail = formatSlashSkillDetail(candidate.Kind, detail)
		}
		if detail != "" || !skillOnly {
			if details == nil {
				details = map[string]string{}
			}
			details[key] = detail
		}
	}
	slices.Sort(agentNames)
	slices.Sort(skillNames)
	result := slashCompletionCandidates{displays: displays, details: details}
	if skillOnly {
		result.commands = skillNames
		return result
	}
	slices.SortFunc(commandNames, func(a, b string) int {
		left, right := slices.Index(slashCommandOrder, a), slices.Index(slashCommandOrder, b)
		switch {
		case left == right:
			return strings.Compare(a, b)
		case left < 0:
			return 1
		case right < 0:
			return -1
		default:
			return left - right
		}
	})
	for _, group := range []struct {
		title string
		names []string
	}{{"Commands", commandNames}, {"Agents", agentNames}, {"Skills", skillNames}} {
		if len(group.names) == 0 {
			continue
		}
		result.groups = append(result.groups, slashCompletionGroup{start: len(result.commands), title: group.title})
		result.commands = append(result.commands, group.names...)
	}
	return result
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
	if selected == "/experimental" {
		m.clearSlashCompletion()
		return m.openExperimental()
	}
	if selected == "/theme" {
		m.clearSlashCompletion()
		return m.submitThemeCommand(selected)
	}
	if m.slashSkillOnly || m.slashCandidateIsSkill(selected) {
		m.applySlashSkillReferenceCompletion(selected)
		return nil
	}
	command := strings.TrimPrefix(selected, "/")
	if slashCommandSubmitsOnCompletion(command) {
		m.setInputText(selected)
		m.syncTextareaFromInput()
		m.clearSlashCompletion()
		_, cmd := m.submitInteractiveLine(selected, selected, nil)
		return cmd
	}
	line := selected + " "
	m.setInputText(line)
	// An overlay may consume the completed command when it opens.
	m.syncTextareaFromInput()
	m.clearSlashCompletion()
	_, cmd := m.tryOpenSlashArgPicker(line)
	return cmd
}

func (m *Model) applySlashSkillReferenceCompletion(selected string) {
	selected = strings.TrimSpace(selected)
	if selected == "" {
		return
	}
	replaced, nextCursor := replaceRuneSpan(m.input, m.slashStart, m.slashEnd, selected)
	m.input = replaced
	m.cursor = nextCursor
	if m.cursor == len(m.input) {
		m.input = append(m.input, ' ')
		m.cursor++
	}
	m.clearSlashCompletion()
}

// Selecting an argument-free command uses the same submit path as typed input.
func slashCommandSubmitsOnCompletion(command string) bool {
	name := strings.ToLower(strings.TrimSpace(command))
	if spec, ok := controlprompt.Lookup(name); ok {
		name = spec.Name
	}
	switch name {
	case "help", "status", "quit", "team":
		return true
	default:
		return false
	}
}

// commandMatchesQuery reports whether a built-in command or one of its aliases
// prefix-matches the slash query. Discovery widens through the alias while the
// committed candidate stays canonical.
func commandMatchesQuery(full string, aliases []string, queryKey string) bool {
	if queryKey == "" {
		return true
	}
	if strings.HasPrefix(strings.ToLower(full), "/"+queryKey) {
		return true
	}
	for _, alias := range aliases {
		if strings.HasPrefix(alias, queryKey) {
			return true
		}
	}
	return false
}

func (m *Model) slashCandidateIsSkill(selected string) bool {
	name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(selected), "/"))
	if name == "" || name == "theme" {
		return false
	}
	return !m.isCommandAvailable(name)
}

func (m *Model) handleSlashCommandKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Back):
		if !m.turnRunning() && !m.slashSkillOnly {
			if _, ok := slashCommandQueryAtCursor(m.input, m.cursor); ok {
				m.setInputText("")
				m.syncTextareaFromInput()
			}
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
		return true, nil
	case key.Matches(msg, m.keys.Accept):
		if len(m.slashCandidates) == 0 {
			return true, nil
		}
		selected := m.slashCandidates[m.slashIndex]
		if !m.slashSkillOnly && !m.slashCandidateIsSkill(selected) && (selected == "/new" || selected == "/compact") {
			m.clearSlashCompletion()
			m.setInputText(selected)
			m.syncTextareaFromInput()
			_, cmd := m.submitInteractiveLine(selected, selected, nil)
			return true, cmd
		}
		cmd := m.applySlashCommandCompletion()
		m.syncTextareaFromInput()
		if cmd != nil {
			return true, cmd
		}
		return true, nil
	default:
		return false, nil
	}
}

func (m *Model) renderSlashCommandListGeometry(geometry completionOverlayGeometry, candidates []string) string {
	rows := make([]completionTableRow, 0, geometry.candidateCount)
	for i := geometry.windowStart; i < geometry.windowEnd; i++ {
		command := candidates[i]
		rows = append(rows, completionTableRow{
			identity: m.slashCommandDisplay(command),
			hint:     m.commandCompletionDetail(command),
		})
	}
	candidateLines := m.renderCompletionTableLines(geometry, rows)
	if len(geometry.groupedRows) == 0 {
		return m.renderCompletionOverlay(geometry, candidateLines)
	}
	lines := make([]string, 0, len(geometry.groupedRows))
	for _, row := range geometry.groupedRows {
		switch {
		case row.heading != "":
			lines = append(lines, " "+m.theme.Tokens().TextSecondary.Bold(true).Render(row.heading))
		case row.candidate < 0:
			lines = append(lines, "")
		default:
			lines = append(lines, candidateLines[row.candidate-geometry.windowStart])
		}
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
	m.slashGroups = nil
	m.slashIndex = 0
	m.slashPrefix = ""
	m.slashSkillOnly = false
	m.slashStart = 0
	m.slashEnd = 0
}
