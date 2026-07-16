package tuiapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	controlagents "github.com/caelis-labs/caelis/control/agents"
	"github.com/caelis-labs/caelis/control/modelconfig"
	"github.com/caelis-labs/caelis/ports/controlprompt/connectwizard"
)

type slashArgLoadResultMsg struct {
	seq        uint64
	command    string
	query      string
	candidates []SlashArgCandidate
	err        error
}

type acpSetupProgressMsg struct {
	seq      uint64
	progress controlagents.SetupProgress
}

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
	if m.turnRunning() {
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
	if m.slashArgLoadPending {
		return
	}
	if !m.slashArgActive || !m.hasSlashArgCompleter() || m.turnRunning() {
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
	if isAsyncSlashArgCommand(command) {
		if m.slashArgLoaded && sameAsyncSlashArgCatalog(m.slashArgLoadedCommand, command) {
			m.applySlashArgCandidates(command, query, m.slashArgLoadedCandidates, nil)
			return
		}
		m.slashArgCandidates = nil
		m.slashArgQuery = query
		m.slashArgIndex = 0
		return
	}
	candidates, err := m.completeSlashArg(contextOrBackground(m.cfg.Context), command, query, 200)
	m.applySlashArgCandidates(command, query, candidates, err)
}

func (m *Model) hasSlashArgCompleter() bool {
	return m != nil && m.cfg.SlashArgComplete != nil
}

func (m *Model) completeSlashArg(ctx context.Context, command string, query string, limit int) ([]SlashArgCandidate, error) {
	if m == nil {
		return nil, nil
	}
	if m.cfg.SlashArgComplete != nil {
		return m.cfg.SlashArgComplete(contextOrBackground(ctx), command, query, limit)
	}
	return nil, nil
}

func (m *Model) applySlashArgCandidates(command string, query string, candidates []SlashArgCandidate, err error) {
	if err != nil || len(candidates) == 0 {
		m.slashArgCandidates = nil
		m.slashArgQuery = query
		m.slashArgIndex = 0
		return
	}
	filtered := filterSlashArgCandidates(query, candidates)
	filtered = m.filterWizardMultiSelectCandidates(filtered)
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

func (m *Model) beginSlashArgLoad() tea.Cmd {
	if m == nil || !m.slashArgActive || !m.hasSlashArgCompleter() {
		return nil
	}
	command := m.currentSlashArgCompletionCommand()
	if !isAsyncSlashArgCommand(command) {
		m.updateSlashArgCandidates()
		return nil
	}
	if m.slashArgLoaded && sameAsyncSlashArgCatalog(m.slashArgLoadedCommand, command) {
		m.updateSlashArgCandidates()
		return nil
	}
	query := strings.TrimSpace(m.slashArgQuery)
	m.cancelSlashArgLoad()
	m.slashArgLoadSeq++
	seq := m.slashArgLoadSeq
	m.slashArgLoadPending = true
	m.slashArgLoadLabel = slashArgLoadLabel(command)
	m.slashArgLoadStartedAt = time.Now()
	m.slashArgLoadBytes = 0
	m.slashArgLoadAuthURL = ""
	m.slashArgLoadAuthCode = ""
	m.slashArgCandidates = nil
	m.slashArgLoaded = false
	m.slashArgLoadedCommand = ""
	m.slashArgLoadedCandidates = nil
	if !m.turnRunning() {
		m.startRunningAnimation()
	}
	requestCtx, cancel := context.WithCancel(contextOrBackground(m.cfg.Context))
	if sender := m.cfg.ProgramSender; sender != nil {
		requestCtx = controlagents.WithSetupProgress(requestCtx, func(progress controlagents.SetupProgress) {
			sender.SendMsg(acpSetupProgressMsg{seq: seq, progress: progress})
		})
		requestCtx = modelconfig.WithAuthProgress(requestCtx, func(progress modelconfig.AuthProgress) {
			sender.SendMsg(modelAuthProgressMsg{seq: seq, progress: progress})
		})
	}
	m.slashArgLoadCancel = cancel
	complete := m.cfg.SlashArgComplete
	return tea.Batch(func() tea.Msg {
		candidates, err := complete(requestCtx, command, "", 200)
		return slashArgLoadResultMsg{
			seq: seq, command: command, query: query, candidates: candidates, err: err,
		}
	}, m.scheduleSpinnerTick())
}

func (m *Model) handleSlashArgLoadResult(msg slashArgLoadResultMsg) tea.Cmd {
	if m == nil || msg.seq != m.slashArgLoadSeq || !m.slashArgLoadPending || strings.TrimSpace(m.slashArgCommand) != msg.command {
		return nil
	}
	m.cancelSlashArgLoad()
	m.slashArgLoadPending = false
	m.slashArgLoadLabel = ""
	m.slashArgLoadStartedAt = time.Time{}
	m.slashArgLoadBytes = 0
	m.slashArgLoadAuthURL = ""
	m.slashArgLoadAuthCode = ""
	if !m.turnRunning() {
		m.stopRunningAnimation()
	}
	if msg.err == nil {
		m.slashArgLoaded = true
		m.slashArgLoadedCommand = msg.command
		m.slashArgLoadedCandidates = append([]SlashArgCandidate(nil), msg.candidates...)
	}
	m.applySlashArgCandidates(msg.command, msg.query, msg.candidates, msg.err)
	if msg.err != nil {
		return m.showHint(fmt.Sprintf("%s: %v", slashArgLoadFailureLabel(msg.command), msg.err), hintOptions{
			priority: HintPriorityHigh, clearOnMessage: true, clearAfter: systemHintDuration,
		})
	}
	return nil
}

func (m *Model) handleACPSetupProgress(msg acpSetupProgressMsg) {
	if m == nil || msg.seq != m.slashArgLoadSeq || !m.slashArgLoadPending {
		return
	}
	progress := msg.progress
	name := acpSetupAdapterDisplayName(progress.AdapterID)
	switch progress.Phase {
	case controlagents.SetupPhaseChecking:
		m.slashArgLoadLabel = "Checking the " + name + " ACP Agent installation"
	case controlagents.SetupPhaseWaiting:
		m.slashArgLoadLabel = "Another Caelis session is installing " + name + "; waiting safely"
	case controlagents.SetupPhaseInstalling:
		m.slashArgLoadLabel = "Installing " + name + " ACP Agent; the runtime download may take several minutes"
	case controlagents.SetupPhaseDownloading:
		m.slashArgLoadLabel = "Downloading and unpacking " + name + " ACP Agent"
	case controlagents.SetupPhaseVerifying:
		m.slashArgLoadLabel = "Verifying the " + name + " adapter and platform runtime"
	case controlagents.SetupPhaseReady:
		m.slashArgLoadLabel = name + " ACP Agent is ready"
	case controlagents.SetupPhaseDiscovering:
		m.slashArgLoadLabel = "Starting " + name + " ACP Agent and discovering models"
	default:
		if detail := strings.TrimSpace(progress.Detail); detail != "" {
			m.slashArgLoadLabel = detail
		}
	}
	if progress.Bytes > m.slashArgLoadBytes {
		m.slashArgLoadBytes = progress.Bytes
	}
}

func (m *Model) cancelSlashArgLoad() {
	if m == nil || m.slashArgLoadCancel == nil {
		return
	}
	m.slashArgLoadCancel()
	m.slashArgLoadCancel = nil
}

func (m *Model) currentSlashArgCompletionCommand() string {
	if m == nil {
		return ""
	}
	if m.isWizardActive() && m.wizard != nil {
		return strings.TrimSpace(m.wizard.completionCommand())
	}
	return strings.TrimSpace(m.slashArgCommand)
}

func sameAsyncSlashArgCatalog(left string, right string) bool {
	return asyncSlashArgCatalogKey(left) == asyncSlashArgCatalogKey(right)
}

func asyncSlashArgCatalogKey(command string) string {
	command = strings.TrimSpace(command)
	prefix := ""
	raw := ""
	switch {
	case strings.HasPrefix(command, "connect-acp-model:"):
		prefix = "model"
		raw = strings.TrimPrefix(command, "connect-acp-model:")
	case strings.HasPrefix(command, "connect-acp-config:"):
		prefix = "config"
		raw = strings.TrimPrefix(command, "connect-acp-config:")
	default:
		return command
	}
	payload, err := parseACPConnectWizardPayload(raw)
	if err != nil {
		return command
	}
	return strings.Join([]string{
		prefix,
		payload.Agent,
		string(payload.Launcher),
		payload.CommandLine,
		payload.Model,
	}, "\x00")
}

func isAsyncSlashArgCommand(command string) bool {
	command = strings.TrimSpace(command)
	if strings.HasPrefix(command, "connect-acp-model:") || strings.HasPrefix(command, "connect-acp-config:") {
		return true
	}
	if !strings.HasPrefix(command, "connect-model:") {
		return false
	}
	payload := connectwizard.ParseConnectWizardStatePayload(strings.TrimPrefix(command, "connect-model:"))
	template, ok := modelconfig.LookupProvider(payload.Provider)
	return ok && template.AuthFlow != ""
}

func slashArgLoadLabel(command string) string {
	if strings.HasPrefix(strings.TrimSpace(command), "connect-model:") {
		payload := connectwizard.ParseConnectWizardStatePayload(strings.TrimPrefix(strings.TrimSpace(command), "connect-model:"))
		provider := strings.TrimSpace(payload.Provider)
		if provider == "" {
			provider = "model provider"
		}
		return "Starting " + provider + " sign-in"
	}
	prefix := "Preparing local ACP Agent"
	raw := ""
	switch {
	case strings.HasPrefix(command, "connect-acp-model:"):
		raw = strings.TrimPrefix(command, "connect-acp-model:")
	case strings.HasPrefix(command, "connect-acp-config:"):
		raw = strings.TrimPrefix(command, "connect-acp-config:")
		prefix = "Loading ACP Agent options"
	}
	if payload, err := parseACPConnectWizardPayload(raw); err == nil && payload.Agent != "" {
		name := acpSetupAdapterDisplayName(payload.Agent)
		if strings.HasPrefix(command, "connect-acp-model:") {
			return "Preparing " + name + " ACP Agent"
		}
		return "Loading " + name + " model options"
	}
	return prefix
}

func slashArgLoadFailureLabel(command string) string {
	if strings.HasPrefix(strings.TrimSpace(command), "connect-model:") {
		return "Model provider sign-in failed"
	}
	return "ACP Agent setup failed"
}

func slashArgLoadCancelLabel(command string) string {
	if strings.HasPrefix(strings.TrimSpace(command), "connect-model:") {
		return "Model provider sign-in canceled."
	}
	return "ACP Agent setup canceled; no incomplete installation was activated."
}

func acpSetupAdapterDisplayName(adapterID string) string {
	switch strings.ToLower(strings.TrimSpace(adapterID)) {
	case "claude":
		return "Claude Code"
	case "codex":
		return "Codex"
	default:
		adapterID = strings.TrimSpace(adapterID)
		if adapterID == "" {
			return "local"
		}
		runes := []rune(adapterID)
		runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
		return string(runes)
	}
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
	if query == "" || m == nil || !m.hasSlashArgCompleter() {
		return "", nil
	}
	for _, candidate := range candidates {
		value := strings.TrimSpace(candidate.Value)
		display := strings.TrimSpace(candidate.Display)
		if !strings.EqualFold(query, value) && !strings.EqualFold(query, display) {
			continue
		}
		nextCommand := "model use " + value
		next, err := m.completeSlashArg(contextOrBackground(m.cfg.Context), nextCommand, "", 200)
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
		if m.addWizardMultiSelectCandidate(selected) {
			return nil
		}
		// During a wizard, fill only the step-local query.
		m.setInputText(choice)
		m.syncTextareaFromInput()
		m.updateSlashArgCandidates()
		return nil
	}
	// Non-wizard: fill and close.
	command := strings.TrimSpace(m.slashArgCommand)
	switch command {
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
	case "plugin":
		return false
	case "plugin marketplace":
		return choice == "list"
	case "plugin marketplace update", "plugin marketplace rm":
		return true
	case "plugin rm":
		return true
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
	return true
}

func requiresExactSlashArgSelection(command string) bool {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "model del", "plugin rm", "plugin marketplace update", "plugin marketplace rm":
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
	default:
		return false
	}
}

func (m *Model) handleSlashArgKey(msg tea.KeyMsg) (bool, tea.Cmd) {
	if m.slashArgActive && strings.TrimSpace(m.slashArgCommand) == "" && !m.isWizardActive() {
		m.clearSlashArg()
		return false, nil
	}
	if m.slashArgLoadPending {
		if key.Matches(msg, m.keys.Back) {
			command := m.slashArgCommand
			m.setInputText("")
			m.syncTextareaFromInput()
			m.clearSlashArg()
			return true, m.showHint(slashArgLoadCancelLabel(command), hintOptions{
				priority: HintPriorityNormal, clearAfter: systemHintDuration,
			})
		}
		return true, nil
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
		if len(m.slashArgCandidates) == 0 {
			if cmd := m.beginSlashArgLoad(); cmd != nil {
				return true, cmd
			}
		}
		cmd := m.applySlashArgCompletion()
		m.syncTextareaFromInput()
		return true, cmd
	case key.Matches(msg, m.keys.Accept):
		if m.turnRunning() || strings.TrimSpace(m.slashArgCommand) == "" {
			return true, nil
		}
		if !m.isWizardActive() {
			m.updateSlashArgCandidates()
		}
		if len(m.slashArgCandidates) == 0 {
			if cmd := m.beginSlashArgLoad(); cmd != nil {
				return true, cmd
			}
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
		if command == "plugin" || command == "plugin marketplace" || command == "plugin marketplace update" || command == "plugin marketplace rm" || command == "model" || command == "model use" || command == "model del" || strings.HasPrefix(command, "model use ") || strings.HasPrefix(command, "model del ") {
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
	lines := make([]string, 0, end-start)
	for i := start; i < end; i++ {
		display := strings.TrimSpace(candidates[i].Display)
		if display == "" {
			display = strings.TrimSpace(candidates[i].Value)
		}
		detail := strings.TrimSpace(candidates[i].Detail)
		lines = append(lines, m.renderCompletionValueLine(display, detail, i == index))
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
