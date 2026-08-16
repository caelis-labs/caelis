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
	"github.com/caelis-labs/caelis/internal/controlprompt/connectwizard"
)

type slashArgLoadResultMsg struct {
	seq        uint64
	command    string
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

func (m *Model) openSlashArgPicker(command string) tea.Cmd {
	cmd := strings.ToLower(strings.TrimSpace(command))
	if cmd == "" {
		return nil
	}
	// Check if this command has a registered wizard definition.
	if def := m.findWizard(cmd); def != nil {
		return m.startWizard(def)
	}
	// Fallback: simple single-step slash-arg (no wizard). Arm the picker
	// synchronously, but always load candidates through a Bubble Tea command so
	// Control or filesystem work never blocks the update loop.
	m.clearMention()
	m.clearResume()
	m.clearSlashCompletion()
	m.cancelSlashArgRequest()
	m.slashArgActive = true
	m.slashArgCommand = cmd
	m.slashArgCandidateCommand = ""
	m.slashArgCompletionSettled = false
	m.slashArgCandidates = nil
	m.slashArgIndex = 0
	m.wizard = nil
	m.setInputText("/" + cmd + " ")
	m.syncTextareaFromInput()
	return m.requestCurrentSlashArgCompletion()
}

func (m *Model) activateSlashArgPickerFromInput(command string) tea.Cmd {
	if !m.activateSlashArgPickerStateFromInput(command) {
		return nil
	}
	return m.requestCurrentSlashArgCompletion()
}

func (m *Model) activateSlashArgPickerStateFromInput(command string) bool {
	cmd := strings.ToLower(strings.TrimSpace(command))
	if cmd == "" {
		return false
	}
	if m.slashArgActive && strings.TrimSpace(m.slashArgCommand) == cmd && !m.isWizardActive() {
		return true
	}
	m.clearMention()
	m.clearResume()
	m.clearSlashCompletion()
	m.cancelSlashArgRequest()
	m.slashArgActive = true
	m.slashArgCommand = cmd
	m.slashArgCandidateCommand = ""
	m.slashArgCompletionSettled = false
	m.slashArgCandidates = nil
	m.slashArgIndex = 0
	m.wizard = nil
	return true
}

func (m *Model) syncSlashInputOverlayState() bool {
	if m.turnRunning() {
		return false
	}
	raw := m.textarea.Value()
	trimmed := strings.TrimSpace(raw)
	hasResumePrefix := strings.HasPrefix(raw, "/resume ")
	hasBareResumeTrigger := strings.EqualFold(trimmed, "/resume") && len(raw) > 0 && (raw[len(raw)-1] == ' ' || raw[len(raw)-1] == '\t')
	if hasResumePrefix || hasBareResumeTrigger {
		m.activateResumePickerFromInput()
		return true
	}
	if m.resumeActive {
		m.clearResume()
	}
	if command, _, ok := slashArgQueryAtEnd([]rune(raw)); ok {
		return m.activateSlashArgPickerStateFromInput(command)
	}
	if m.slashArgActive && !m.isWizardActive() {
		m.clearSlashArg()
	}
	return false
}

func (m *Model) currentSlashArgCompletionTarget() (command string, query string, ok bool) {
	if m == nil || !m.slashArgActive {
		return "", "", false
	}
	command = m.slashArgCommand
	if m.isWizardActive() {
		w := m.wizard
		step := w.currentStep()
		if step == nil || w.noCompletion() {
			return "", "", false
		}
		command = w.completionCommand()
		query, ok = wizardQueryAtCursor(w.def.Command, m.input, m.cursor)
		return command, query, ok
	}
	parsedCommand, query, ok := slashArgQueryAtEnd([]rune(m.textarea.Value()))
	if !ok {
		return "", "", false
	}
	if parsedCommand == command {
		return command, query, true
	}
	if isExactModelUseReasoningCommand(command, parsedCommand, query) {
		return command, "", true
	}
	return "", "", false
}

func (m *Model) hasSlashArgCompleter() bool {
	return m != nil && m.cfg.SlashArgComplete != nil
}

func (m *Model) dropStaleSlashArgCandidates() {
	command, query, ok := m.currentSlashArgCompletionTarget()
	if ok && command == m.slashArgCandidateCommand && query == m.slashArgQuery {
		return
	}
	if m.slashArgRequestPending && (!ok || command != m.slashArgRequestCommand || query != m.slashArgRequestQuery) {
		m.cancelSlashArgRequest()
	}
	if ok && isAsyncSlashArgCommand(command) && m.slashArgLoaded && sameAsyncSlashArgCatalog(m.slashArgLoadedCommand, command) {
		m.applySlashArgCandidates(command, query, m.slashArgLoadedCandidates, nil)
		return
	}
	m.slashArgCandidates = nil
	m.slashArgCandidateCommand = ""
	m.slashArgCompletionSettled = false
	m.slashArgIndex = 0
	if ok {
		m.slashArgQuery = query
	} else if m.isWizardActive() {
		m.slashArgQuery, _ = wizardQueryAtCursor(m.wizard.def.Command, m.input, m.cursor)
	} else {
		m.slashArgQuery = ""
	}
}

func (m *Model) applySlashArgCandidates(command string, query string, candidates []SlashArgCandidate, err error) {
	m.slashArgCandidateCommand = strings.TrimSpace(command)
	m.slashArgCompletionSettled = err == nil
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
		return m.requestSlashArgCompletion()
	}
	if m.slashArgLoaded && sameAsyncSlashArgCatalog(m.slashArgLoadedCommand, command) {
		completionCommand, query, ok := m.currentSlashArgCompletionTarget()
		if ok {
			m.applySlashArgCandidates(completionCommand, query, m.slashArgLoadedCandidates, nil)
		}
		return nil
	}
	if m.slashArgLoadPending && sameAsyncSlashArgCatalog(m.slashArgLoadCommand, command) {
		return nil
	}
	m.cancelSlashArgLoad()
	m.slashArgLoadSeq++
	seq := m.slashArgLoadSeq
	m.slashArgLoadPending = true
	m.slashArgLoadCommand = command
	m.slashArgLoadLabel = slashArgLoadLabel(command)
	m.slashArgLoadStartedAt = time.Now()
	m.slashArgLoadBytes = 0
	m.slashArgLoadAuthURL = ""
	m.slashArgLoadAuthCode = ""
	m.slashArgLoadAuthPrompt = nil
	m.slashArgCandidates = nil
	m.slashArgCompletionSettled = false
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
		requestCtx = modelconfig.WithAuthInput(requestCtx, func(inputCtx context.Context, request modelconfig.AuthInputRequest) (string, error) {
			responses := make(chan PromptResponse, 1)
			sender.SendMsg(modelAuthInputRequestMsg{seq: seq, request: request, response: responses})
			select {
			case response := <-responses:
				return response.Line, response.Err
			case <-inputCtx.Done():
				sender.SendMsg(modelAuthInputCancelMsg{seq: seq, response: responses})
				return "", inputCtx.Err()
			}
		})
		requestCtx = controlagents.WithAuthenticationSelection(requestCtx, func(
			inputCtx context.Context,
			request controlagents.AuthenticationSelectionRequest,
		) (string, error) {
			responses := make(chan PromptResponse, 1)
			sender.SendMsg(acpAuthSelectionRequestMsg{seq: seq, request: request, response: responses})
			select {
			case response := <-responses:
				return response.Line, response.Err
			case <-inputCtx.Done():
				sender.SendMsg(acpAuthSelectionCancelMsg{seq: seq, response: responses})
				return "", inputCtx.Err()
			}
		})
		requestCtx = controlagents.WithTerminalAuthentication(requestCtx, func(
			authCtx context.Context,
			request controlagents.TerminalAuthenticationRequest,
		) error {
			responses := make(chan error, 1)
			sender.SendMsg(acpTerminalAuthRequestMsg{
				seq: seq, ctx: authCtx, request: request, response: responses,
			})
			select {
			case err := <-responses:
				return err
			case <-authCtx.Done():
				return authCtx.Err()
			}
		})
	}
	m.slashArgLoadCancel = cancel
	complete := m.cfg.SlashArgComplete
	return tea.Batch(func() tea.Msg {
		candidates, err := complete(requestCtx, command, "", 200)
		return slashArgLoadResultMsg{
			seq: seq, command: command, candidates: candidates, err: err,
		}
	}, m.scheduleSpinnerTick())
}

func (m *Model) handleSlashArgLoadResult(msg slashArgLoadResultMsg) tea.Cmd {
	if m == nil || msg.seq != m.slashArgLoadSeq || !m.slashArgLoadPending || msg.command != m.slashArgLoadCommand {
		return nil
	}
	m.cancelSlashArgLoad()
	m.slashArgLoadPending = false
	m.slashArgLoadCommand = ""
	m.slashArgLoadLabel = ""
	m.slashArgLoadStartedAt = time.Time{}
	m.slashArgLoadBytes = 0
	m.slashArgLoadAuthURL = ""
	m.slashArgLoadAuthCode = ""
	m.slashArgLoadAuthPrompt = nil
	if !m.turnRunning() {
		m.stopRunningAnimation()
	}
	if m.turnRunning() {
		return nil
	}
	command, query, ok := m.currentSlashArgCompletionTarget()
	if ok {
		if command != msg.command {
			return nil
		}
	} else {
		// Async setup commands are normally wizard-owned, but the picker also
		// supports direct activation with an internal command identity.
		command = strings.TrimSpace(m.slashArgCommand)
		query = strings.TrimSpace(m.slashArgQuery)
		if command != msg.command {
			return nil
		}
	}
	if msg.err == nil {
		m.slashArgLoaded = true
		m.slashArgLoadedCommand = msg.command
		m.slashArgLoadedCandidates = append([]SlashArgCandidate(nil), msg.candidates...)
	}
	// Catalog requests deliberately load with an empty backend query. Filter
	// against the current composer query so typing while the load is in flight
	// cannot restore stale, unfiltered candidates when the result arrives.
	m.applySlashArgCandidates(command, query, msg.candidates, msg.err)
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

func exactModelUseReasoningCommandForQuery(query string, candidates []SlashArgCandidate) string {
	query = strings.TrimSpace(query)
	if query == "" {
		return ""
	}
	for _, candidate := range candidates {
		value := strings.TrimSpace(candidate.Value)
		display := strings.TrimSpace(candidate.Display)
		if strings.EqualFold(query, value) || strings.EqualFold(query, display) {
			return "model use " + value
		}
	}
	return ""
}

func (m *Model) slashArgCompletionSettledForCurrentTarget() bool {
	if m == nil || !m.slashArgCompletionSettled || m.slashArgRequestPending || m.slashArgLoadPending {
		return false
	}
	command, query, ok := m.currentSlashArgCompletionTarget()
	return ok && command == m.slashArgCandidateCommand && query == m.slashArgQuery
}

func (m *Model) applySlashArgCompletion() tea.Cmd {
	if len(m.slashArgCandidates) == 0 || strings.TrimSpace(m.slashArgCommand) == "" {
		return m.requestCurrentSlashArgCompletion()
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
		if handled, cmd := m.toggleWizardMultiSelectCandidate(selected); handled {
			return cmd
		}
		// During a wizard, fill only the step-local query.
		m.setInputText(choice)
		m.syncTextareaFromInput()
		return m.requestCurrentSlashArgCompletion()
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
			return m.activateSlashArgPickerFromInput("plugin " + choice)
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
			return m.activateSlashArgPickerFromInput("plugin marketplace " + choice)
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
		case "use", "del":
			return m.activateSlashArgPickerFromInput("model " + choice)
		default:
			m.clearSlashArg()
		}
		return nil
	case "model use":
		m.setInputText("/model use " + choice + " ")
		m.syncTextareaFromInput()
		return m.activateSlashArgPickerFromInput("model use " + choice)
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
		switch {
		case key.Matches(msg, m.keys.Back):
			command := m.slashArgCommand
			m.setInputText("")
			m.syncTextareaFromInput()
			m.clearSlashArg()
			return true, m.showHint(slashArgLoadCancelLabel(command), hintOptions{
				priority: HintPriorityNormal, clearAfter: systemHintDuration,
			})
		case key.Matches(msg, m.keys.Accept),
			key.Matches(msg, m.keys.Complete),
			key.Matches(msg, m.keys.ChoosePrev),
			key.Matches(msg, m.keys.ChooseNext):
			// Keep submission and picker gestures fenced until the current
			// catalog is available, but let ordinary composer edits proceed.
			return true, nil
		default:
			return false, nil
		}
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
			m.moveActiveCompletionSelection(-1, true)
		}
		return true, nil
	case key.Matches(msg, m.keys.ChooseNext):
		if len(m.slashArgCandidates) > 0 {
			m.moveActiveCompletionSelection(1, true)
		}
		return true, nil
	case m.isWizardMultiSelectToggleKey(msg):
		candidate, ok := m.currentSlashArgCandidate()
		if !ok {
			return true, nil
		}
		_, cmd := m.toggleWizardMultiSelectCandidate(candidate)
		return true, cmd
	case key.Matches(msg, m.keys.Complete):
		if len(m.slashArgCandidates) == 0 {
			return true, m.requestCurrentSlashArgCompletion()
		}
		cmd := m.applySlashArgCompletion()
		m.syncTextareaFromInput()
		return true, cmd
	case key.Matches(msg, m.keys.Accept):
		if m.turnRunning() || strings.TrimSpace(m.slashArgCommand) == "" {
			return true, nil
		}
		line := strings.TrimSpace(m.textarea.Value())
		if !m.isWizardActive() && len(m.slashArgCandidates) == 0 {
			if !m.slashArgCompletionSettledForCurrentTarget() {
				return true, m.requestCurrentSlashArgCompletion()
			}
			if isExecutableSlashArgInput(line) && !requiresExactSlashArgSelection(m.slashArgCommand) {
				m.clearSlashArg()
				_, cmd := m.submitLine(line)
				return true, cmd
			}
		}
		// Delegate to wizard input before requesting candidates so free-form steps
		// remain one-Enter interactions while candidate-only steps wait safely for
		// their asynchronous catalog.
		if m.isWizardActive() {
			step := m.wizard.currentStep()
			if len(m.slashArgCandidates) == 0 && step != nil && step.RequireCandidate {
				return true, m.requestCurrentSlashArgCompletion()
			}
			handled, cmd := m.handleWizardEnter()
			return handled, cmd
		}
		if len(m.slashArgCandidates) == 0 {
			return true, m.requestCurrentSlashArgCompletion()
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
	return m.renderCompletionKind(completionSlashArg)
}

func (m *Model) renderSlashArgListGeometry(geometry completionOverlayGeometry, candidates []SlashArgCandidate) string {
	rows := make([]completionTableRow, 0, geometry.candidateCount)
	for i := geometry.windowStart; i < geometry.windowEnd; i++ {
		identity := slashArgCandidateIdentity(candidates[i])
		if marker, ok := m.wizardMultiSelectMarker(candidates[i]); ok {
			identity = marker + identity
		}
		rows = append(rows, completionTableRow{
			identity: identity,
			hint:     slashArgPickerHint(m.slashArgCommand, candidates, i),
		})
	}
	return m.renderCompletionOverlay(geometry, m.renderCompletionTableLines(geometry, rows))
}

func (m *Model) isWizardMultiSelectToggleKey(msg tea.KeyMsg) bool {
	if m == nil || m.wizard == nil {
		return false
	}
	step := m.wizard.currentStep()
	if step == nil || !step.MultiSelect || len(m.slashArgCandidates) == 0 {
		return false
	}
	switch msg.String() {
	case " ", "space":
		return true
	default:
		return false
	}
}

func (m *Model) wizardMultiSelectMarker(candidate SlashArgCandidate) (string, bool) {
	if m == nil || m.wizard == nil {
		return "", false
	}
	step := m.wizard.currentStep()
	if !wizardCandidateSupportsMultiSelect(step, candidate) {
		return "", false
	}
	if wizardMultiSelectContains(wizardMultiSelectValues(m.wizard, step), candidate.Value) {
		return "[x] ", true
	}
	return "[ ] ", true
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
