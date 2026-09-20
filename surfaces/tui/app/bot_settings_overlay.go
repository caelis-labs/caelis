package tuiapp

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/bot"
)

// Bot edits use the shared overlay, but are saved only through BotClient.
// The original revision remains fixed throughout an edit, including model search.
type botSettingsDraft struct {
	current                          bot.Bot
	config                           bot.Config
	create, modelOnly, choosingModel bool
	fields                           []wizardField
	loading                          bool
	cancel                           context.CancelFunc
}

type botSettingsLoadedMsg struct {
	owner   *wizardOverlayState
	current bot.Bot
	err     error
}
type botSettingsSavedMsg struct {
	owner    *wizardOverlayState
	messages []tea.Msg
}

func (m *Model) startBotCreateFlow() tea.Cmd {
	if m.bot == nil || m.bot.client == nil {
		return nil
	}
	m.openBotSettingsOverlay(&botSettingsDraft{create: true})
	m.showBotSettingsForm()
	return nil
}

func (m *Model) startBotSettingsFlow(modelOnly bool) tea.Cmd {
	active, ok := m.activeBot()
	if !ok {
		return m.showHint("Select or create a Bot first", botHintOptions())
	}
	if m.turnRunning() {
		return m.showHint(botSaveAfterReplyNotice, botHintOptions())
	}
	m.openBotSettingsOverlay(&botSettingsDraft{modelOnly: modelOnly})
	s := m.wizardOverlay
	s.pending, s.bot.loading = true, true
	client := m.bot.client
	ctx, cancel := context.WithCancel(m.botFlowContext())
	s.bot.cancel = cancel
	return func() tea.Msg {
		current, err := client.GetBot(ctx, active.ID)
		return botSettingsLoadedMsg{s, current, err}
	}
}

func (m *Model) openBotSettingsOverlay(draft *botSettingsDraft) {
	m.clearWizard()
	m.clearSlashCompletion()
	m.clearMention()
	command := "settings"
	if draft.create {
		command = "new"
	}
	if draft.modelOnly {
		command = "model"
	}
	m.wizard = &wizardRuntime{def: &WizardDef{Command: command, Steps: []WizardStepDef{{Key: "bot_settings", NoCompletion: true}}}, state: map[string]string{}}
	m.wizardOverlay = &wizardOverlayState{bot: draft, drafts: map[string]wizardStepDraft{}}
}

func (m *Model) handleBotSettingsLoaded(msg botSettingsLoadedMsg) tea.Cmd {
	if m.wizardOverlay != msg.owner {
		return nil
	}
	s := m.wizardOverlay
	s.pending, s.bot.loading = false, false
	if s.bot.cancel != nil {
		s.bot.cancel()
		s.bot.cancel = nil
	}
	if msg.err != nil {
		s.err, s.blocked = "Could not load settings: "+msg.err.Error(), true
		return nil
	}
	s.bot.current, s.bot.config = msg.current, msg.current.Config
	if s.bot.modelOnly {
		return m.openBotSettingsModels()
	}
	m.showBotSettingsForm()
	return nil
}

func (m *Model) showBotSettingsForm() {
	s := m.wizardOverlay
	d := s.bot
	d.choosingModel = false
	m.cancelWizardCatalog()
	m.slashArgActive = false
	m.wizard.def.Steps = []WizardStepDef{{Key: "bot_settings", NoCompletion: true}}
	s.fields = []wizardField{
		{key: "name", label: "Name", value: d.config.Name},
		{key: "description", label: "Description", value: d.config.Description, placeholder: "Optional"},
		{key: "bot_model", label: "Model", value: firstNonEmpty(d.current.ModelSelector, d.config.Model, "Host default")},
	}
	if d.fields != nil {
		s.fields = d.fields
	}
	s.field, s.cursor, s.window = 0, len([]rune(s.fields[0].value)), 0
}

func (m *Model) openBotSettingsModels() tea.Cmd {
	s := m.wizardOverlay
	if len(s.fields) > 0 {
		s.bot.fields = s.fields
	}
	s.fields, s.field, s.window = nil, 0, 0
	s.bot.choosingModel = true
	m.wizard.def.Steps = []WizardStepDef{{Key: "bot_model", RequireCandidate: true, CompletionCommand: func(map[string]string) string { return "model" }}}
	m.slashArgActive, m.slashArgCommand, m.slashArgQuery, m.slashArgIndex = true, "model", "", 0
	m.modelPicker = &modelPickerState{drafts: map[string]modelPickerDraft{}}
	return m.requestCurrentSlashArgCompletion()
}

func (m *Model) botSettingsCandidates(query string, candidates []SlashArgCandidate) []SlashArgCandidate {
	d := m.wizardOverlay.bot
	filtered := make([]SlashArgCandidate, 0, len(candidates)+1)
	found := false
	for _, c := range candidates {
		if c.ModelConfigID == "" {
			continue
		}
		current := c.ModelConfigID == d.config.Model
		found = found || current
		if c.ModelSelection != nil {
			selection := *c.ModelSelection
			selection.Current = current
			if current {
				selection.Effort, selection.Fast = d.config.Effort, d.config.Fast
			}
			c.ModelSelection = &selection
		}
		if current && m.modelPicker.selected == "" {
			m.modelPicker.selected = c.Value
		}
		filtered = append(filtered, c)
	}
	// A filtered catalog cannot establish whether the current model is unavailable.
	if !found && d.config.Model != "" && strings.TrimSpace(query) == "" {
		c := SlashArgCandidate{Value: d.config.Model, ModelConfigID: d.config.Model, Display: firstNonEmpty(d.current.ModelSelector, d.config.Model), Detail: "Current · unavailable"}
		filtered = append([]SlashArgCandidate{c}, filtered...)
		if m.modelPicker.selected == "" {
			m.modelPicker.selected = c.Value
		}
	}
	return filtered
}

func (m *Model) acceptBotSettings() tea.Cmd {
	s, d := m.wizardOverlay, m.wizardOverlay.bot
	if d.choosingModel {
		c, ok := m.currentSlashArgCandidate()
		if !ok || !m.slashArgCompletionSettledForCurrentTarget() {
			return nil
		}
		if d.config.Model != c.ModelConfigID {
			d.config.Model, d.config.Effort, d.config.Fast = c.ModelConfigID, "", false
		}
		if c.ModelSelection != nil {
			draft := m.modelPickerDraft(c)
			d.config.Effort, d.config.Fast = draft.effort, draft.fast
		}
		if d.modelOnly {
			return m.saveBotSettings()
		}
		d.fields[2].value = firstNonEmpty(c.Display, c.Value)
		m.showBotSettingsForm()
		s.field = 2
		return nil
	}
	return m.saveBotSettings()
}

func (m *Model) saveBotSettings() tea.Cmd {
	s, d := m.wizardOverlay, m.wizardOverlay.bot
	if m.turnRunning() {
		s.err = "Wait for the reply to finish"
		return nil
	}
	if !d.modelOnly {
		d.config.Name, d.config.Description = strings.TrimSpace(s.fields[0].value), s.fields[1].value
	}
	if d.config.Name == "" {
		s.err, s.field = "Enter a name", 0
		return nil
	}
	if !d.create && d.current.Config == d.config {
		m.clearWizard()
		return nil
	}
	s.pending, s.err = true, ""
	current, config, create := d.current, d.config, d.create
	client, ctx := m.bot.client, m.botFlowContext()
	return func() tea.Msg {
		var messages []tea.Msg
		send := func(msg tea.Msg) { messages = append(messages, msg) }
		if create {
			runBotCreateFlow(ctx, client, config, send)
		} else {
			runBotSettingsFlow(ctx, client, current, config, send)
		}
		return botSettingsSavedMsg{s, messages}
	}
}

func (m *Model) handleBotSettingsSaved(msg botSettingsSavedMsg) tea.Cmd {
	if m.wizardOverlay != msg.owner {
		return nil
	}
	s := m.wizardOverlay
	s.pending = false
	for _, message := range msg.messages {
		if result, ok := message.(botFlowResultMsg); ok {
			m.clearWizard()
			return m.handleBotFlowResult(result)
		}
		if notice, ok := message.(SlashNoticeMsg); ok {
			s.err = notice.Text
		}
	}
	// A failed or unknown mutation is review-only. Never repeat it from the
	// stale revision or replay a creation that may already have committed.
	s.blocked = true
	return nil
}

func (m *Model) botSettingsTitle() string {
	d := m.wizardOverlay.bot
	if m.wizardOverlay.pending {
		if d.current.ID == "" && !d.create {
			return "Loading"
		}
		return "Saving"
	}
	if d.choosingModel {
		return "Models"
	}
	if d.create {
		return "New Bot"
	}
	return d.current.Config.Name
}

func (m *Model) isBotSettingsModel() bool {
	return m.wizardOverlay != nil && m.wizardOverlay.bot != nil && m.wizardOverlay.bot.choosingModel
}
