package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
)

type commandPanelAction struct {
	line      string
	fillInput string
	prompt    *commandPanelPrompt
}

type commandPanelPrompt struct {
	title              string
	prompt             string
	details            []PromptDetail
	choices            []PromptChoice
	defaultChoice      string
	defaultInput       string
	filterable         bool
	allowFreeformInput bool
	buildLine          func(string) string
}

func (m *Model) tryCommandPanelClickToken(blockID string, token string) (bool, tea.Cmd) {
	input, ok := commandPanelInputFromClickToken(token)
	if !ok {
		return false, nil
	}
	block, ok := m.doc.Find(strings.TrimSpace(blockID)).(*CommandPanelBlock)
	if !ok {
		m.setInputText(input)
		return true, nil
	}
	action := commandPanelActionForInput(block.view, input)
	return m.beginCommandPanelAction(action, input)
}

func (m *Model) beginCommandPanelAction(action commandPanelAction, fallbackInput string) (bool, tea.Cmd) {
	if action.prompt != nil {
		responses := make(chan PromptResponse, 1)
		req := PromptRequestMsg{
			Title:              action.prompt.title,
			Prompt:             action.prompt.prompt,
			Details:            append([]PromptDetail(nil), action.prompt.details...),
			Choices:            append([]PromptChoice(nil), action.prompt.choices...),
			DefaultChoice:      action.prompt.defaultChoice,
			DefaultInput:       action.prompt.defaultInput,
			Filterable:         action.prompt.filterable,
			AllowFreeformInput: action.prompt.allowFreeformInput,
			Response:           responses,
		}
		m.enqueuePrompt(req)
		m.ensureViewportLayout()
		m.syncViewportContent()
		buildLine := action.prompt.buildLine
		return true, func() tea.Msg {
			response, ok := <-responses
			if !ok || response.Err != nil || buildLine == nil {
				return nil
			}
			if line := strings.TrimSpace(buildLine(response.Line)); line != "" {
				return commandPanelSubmitMsg{Line: line}
			}
			return nil
		}
	}
	if strings.TrimSpace(action.line) != "" {
		return true, func() tea.Msg {
			return commandPanelSubmitMsg{Line: action.line}
		}
	}
	m.setInputText(firstNonEmpty(action.fillInput, fallbackInput))
	return true, nil
}

func commandPanelActionForInput(view appviewmodel.CommandExecutionView, input string) commandPanelAction {
	input = strings.TrimSpace(input)
	switch {
	case view.SettingsPanel != nil:
		return settingsCommandPanelAction(*view.SettingsPanel, input)
	case view.TaskPanel != nil:
		return taskCommandPanelAction(*view.TaskPanel, input)
	case view.ResumePanel != nil:
		return commandPanelAction{line: input}
	case view.ApprovalPanel != nil:
		return commandPanelAction{line: input}
	case view.ModelSelection != nil:
		return modelSelectionCommandPanelAction(*view.ModelSelection, input)
	case view.ControllerPanel != nil:
		return controllerCommandPanelAction(*view.ControllerPanel, input)
	case view.ModelConnectPanel != nil:
		return commandPanelAction{fillInput: input + " "}
	case view.AgentManagement != nil:
		return agentCommandPanelAction(*view.AgentManagement, input)
	default:
		return commandPanelAction{fillInput: input}
	}
}

func settingsCommandPanelAction(panel appviewmodel.SettingsPanelView, input string) commandPanelAction {
	if fieldID, ok := strings.CutPrefix(input, "/settings set "); ok {
		field, ok := findSettingsPanelField(panel, strings.TrimSpace(fieldID))
		if !ok || !field.Editable {
			return commandPanelAction{fillInput: input}
		}
		return settingsFieldCommandPanelAction(field)
	}
	if actionID, ok := strings.CutPrefix(input, "/settings run "); ok {
		action, ok := findSettingsPanelAction(panel.Actions, strings.TrimSpace(actionID))
		if !ok || !action.Enabled {
			return commandPanelAction{fillInput: input}
		}
		line := "/settings run " + strings.TrimSpace(action.ID)
		if action.Destructive || action.RequiresConfirmation {
			return commandPanelAction{prompt: confirmCommandPanelPrompt(
				"Run "+strings.TrimSpace(action.ID)+"?",
				"Confirm settings action",
				[]PromptDetail{
					{Label: "Action", Value: strings.TrimSpace(action.ID), Emphasis: true},
					{Label: "Description", Value: strings.TrimSpace(action.Description)},
				},
				line+" confirm",
			)}
		}
		return commandPanelAction{line: line}
	}
	return commandPanelAction{fillInput: input}
}

func settingsFieldCommandPanelAction(field appviewmodel.SettingsPanelField) commandPanelAction {
	fieldID := strings.TrimSpace(field.ID)
	details := []PromptDetail{
		{Label: "Field", Value: fieldID, Emphasis: true},
		{Label: "Current", Value: strings.TrimSpace(field.Value)},
		{Label: "Description", Value: strings.TrimSpace(field.Description)},
	}
	if len(field.Options) > 0 {
		return commandPanelAction{prompt: &commandPanelPrompt{
			title:         "Set " + fieldID,
			prompt:        firstNonEmpty(field.Label, fieldID),
			details:       details,
			choices:       settingsPromptChoices(field.Options),
			defaultChoice: firstNonEmpty(field.Value, firstSettingsOptionValue(field.Options)),
			filterable:    true,
			buildLine: func(value string) string {
				value = strings.TrimSpace(value)
				if value == "" {
					return ""
				}
				return "/settings set " + fieldID + " " + value
			},
		}}
	}
	return commandPanelAction{prompt: &commandPanelPrompt{
		title:        "Set " + fieldID,
		prompt:       firstNonEmpty(field.Label, fieldID),
		details:      details,
		defaultInput: strings.TrimSpace(field.Value),
		buildLine: func(value string) string {
			value = strings.TrimSpace(value)
			if value == "" && strings.TrimSpace(field.Value) == "" {
				return ""
			}
			return "/settings set " + fieldID + " " + value
		},
	}}
}

func modelSelectionCommandPanelAction(panel appviewmodel.ModelSelectionView, input string) commandPanelAction {
	for _, action := range panel.Actions {
		command := strings.TrimSpace(action.Command)
		if !action.Enabled || command == "" || command != input {
			continue
		}
		if action.Destructive {
			modelID := firstNonEmpty(action.ModelID, strings.TrimPrefix(command, "/model del "))
			return commandPanelAction{prompt: confirmCommandPanelPrompt(
				"Delete model?",
				"Confirm model delete",
				[]PromptDetail{
					{Label: "Model", Value: strings.TrimSpace(modelID), Emphasis: true},
					{Label: "Action", Value: firstNonEmpty(action.Label, action.ID)},
				},
				command,
			)}
		}
		return commandPanelAction{line: command}
	}
	if modelRef, ok := strings.CutPrefix(input, "/model use "); ok {
		modelRef = strings.TrimSpace(modelRef)
		if modelRef != "" {
			return commandPanelAction{line: "/model use " + modelRef}
		}
	}
	if modelRef, ok := strings.CutPrefix(input, "/model del "); ok {
		modelRef = strings.TrimSpace(modelRef)
		if modelRef != "" {
			choice, _ := findModelSelectionChoice(panel, modelRef)
			return commandPanelAction{prompt: confirmCommandPanelPrompt(
				"Delete model?",
				"Confirm model delete",
				[]PromptDetail{
					{Label: "Model", Value: firstNonEmpty(modelRef, modelChoiceLabel(choice)), Emphasis: true},
					{Label: "Provider", Value: strings.TrimSpace(choice.Provider)},
				},
				"/model del "+modelRef,
			)}
		}
	}
	if strings.EqualFold(input, "/connect") {
		return commandPanelAction{line: "/connect"}
	}
	return commandPanelAction{fillInput: input}
}

func agentCommandPanelAction(panel appviewmodel.AgentManagementView, input string) commandPanelAction {
	for _, action := range agentPanelAllActions(panel) {
		command := strings.TrimSpace(action.Command)
		if !action.Enabled || command == "" || command != input {
			continue
		}
		if action.Destructive {
			return commandPanelAction{prompt: confirmCommandPanelPrompt(
				"Remove agent?",
				"Confirm agent removal",
				[]PromptDetail{
					{Label: "Agent", Value: firstNonEmpty(action.AgentID, strings.TrimPrefix(command, "/agent remove ")), Emphasis: true},
					{Label: "Action", Value: firstNonEmpty(action.Name, action.ID)},
				},
				command,
			)}
		}
		if action.RequiresInput {
			return commandPanelAction{fillInput: command + " "}
		}
		return commandPanelAction{line: command}
	}
	if target, ok := strings.CutPrefix(input, "/agent remove "); ok {
		target = strings.TrimSpace(target)
		if target != "" {
			return commandPanelAction{prompt: confirmCommandPanelPrompt(
				"Remove agent?",
				"Confirm agent removal",
				[]PromptDetail{{Label: "Agent", Value: target, Emphasis: true}},
				"/agent remove "+target,
			)}
		}
	}
	return commandPanelAction{fillInput: input}
}

func taskCommandPanelAction(panel appviewmodel.TaskPanelView, input string) commandPanelAction {
	for _, action := range panel.Actions {
		if !action.Enabled {
			continue
		}
		for _, hint := range taskPanelActionClickHint(action) {
			if strings.TrimSpace(hint.Input) == input {
				return taskPanelActionCommand(action, taskByID(panel.Tasks, action.TaskID))
			}
		}
	}
	if taskID, ok := strings.CutPrefix(input, "/task tail "); ok {
		taskID = strings.TrimSpace(taskID)
		if taskID != "" {
			return commandPanelAction{line: "/task tail " + taskID}
		}
	}
	return commandPanelAction{fillInput: input}
}

func taskPanelActionCommand(action appviewmodel.TaskPanelAction, task appviewmodel.TaskItem) commandPanelAction {
	taskID := strings.TrimSpace(action.TaskID)
	kind := strings.ToLower(strings.TrimSpace(action.Kind))
	if kind == "" {
		kind, _, _ = strings.Cut(strings.TrimPrefix(strings.TrimSpace(action.ID), "task."), ":")
	}
	details := []PromptDetail{
		{Label: "Task", Value: firstNonEmpty(taskID, task.ID), Emphasis: true},
		{Label: "Title", Value: strings.TrimSpace(firstNonEmpty(task.Title, task.Command))},
	}
	switch kind {
	case "start", "run":
		return commandPanelAction{prompt: &commandPanelPrompt{
			title:  "Start task",
			prompt: "Command",
			buildLine: func(value string) string {
				value = strings.TrimSpace(value)
				if value == "" {
					return ""
				}
				return "/task start -- " + value
			},
		}}
	case "write":
		if taskID == "" {
			return commandPanelAction{}
		}
		return commandPanelAction{prompt: &commandPanelPrompt{
			title:   "Write to task",
			prompt:  "Input",
			details: details,
			buildLine: func(value string) string {
				value = strings.TrimSpace(value)
				if value == "" {
					return ""
				}
				return "/task write " + taskID + " -- " + value
			},
		}}
	case "cancel":
		if taskID == "" {
			return commandPanelAction{}
		}
		return commandPanelAction{prompt: confirmCommandPanelPrompt(
			"Cancel task?",
			"Confirm task cancel",
			details,
			"/task cancel "+taskID,
		)}
	case "tail", "show":
		if taskID != "" {
			return commandPanelAction{line: "/task tail " + taskID}
		}
	case "wait":
		if taskID != "" {
			return commandPanelAction{line: "/task wait " + taskID}
		}
	case "release", "close":
		if taskID != "" {
			return commandPanelAction{line: "/task release " + taskID}
		}
	}
	return commandPanelAction{}
}

func controllerCommandPanelAction(panel appviewmodel.ControllerPanelView, input string) commandPanelAction {
	if !panel.Active {
		return commandPanelAction{fillInput: input}
	}
	switch input {
	case "/model use":
		field, ok := findControllerPanelField(panel, "controller.model")
		if !ok || !field.Editable || len(field.Options) == 0 {
			return commandPanelAction{fillInput: input + " "}
		}
		return controllerFieldCommandPanelAction(field, "/model use ")
	case "/approval":
		field, ok := findControllerPanelField(panel, "controller.mode")
		if !ok || !field.Editable || len(field.Options) == 0 {
			return commandPanelAction{fillInput: input + " "}
		}
		return controllerFieldCommandPanelAction(field, "/approval ")
	case "/approval toggle", "/agent use local":
		return commandPanelAction{line: input}
	}
	if modelRef, ok := strings.CutPrefix(input, "/model use "); ok {
		modelRef = strings.TrimSpace(modelRef)
		field, hasReasoning := findControllerPanelField(panel, "controller.reasoning")
		if modelRef != "" && hasReasoning && field.Editable && len(field.Options) > 0 && strings.EqualFold(modelRef, controllerPanelCurrentModel(panel)) {
			return controllerReasoningCommandPanelAction(field, modelRef)
		}
		return commandPanelAction{fillInput: input + " "}
	}
	if rest, ok := strings.CutPrefix(input, "/controller set "); ok {
		optionID := strings.TrimSpace(rest)
		field, ok := findControllerPanelField(panel, "controller.config."+optionID)
		if !ok || !field.Editable {
			return commandPanelAction{fillInput: input + " "}
		}
		return controllerConfigFieldCommandPanelAction(field, optionID)
	}
	return commandPanelAction{fillInput: input}
}

func controllerFieldCommandPanelAction(field appviewmodel.ControllerPanelField, prefix string) commandPanelAction {
	fieldID := strings.TrimSpace(field.ID)
	return commandPanelAction{prompt: &commandPanelPrompt{
		title:         "Set " + fieldID,
		prompt:        firstNonEmpty(field.Label, fieldID),
		details:       []PromptDetail{{Label: "Field", Value: fieldID, Emphasis: true}, {Label: "Current", Value: strings.TrimSpace(field.Value)}},
		choices:       controllerPromptChoices(field.Options),
		defaultChoice: firstNonEmpty(field.Value, firstControllerOptionValue(field.Options)),
		filterable:    true,
		buildLine: func(value string) string {
			value = strings.TrimSpace(value)
			if value == "" {
				return ""
			}
			return prefix + value
		},
	}}
}

func controllerReasoningCommandPanelAction(field appviewmodel.ControllerPanelField, modelRef string) commandPanelAction {
	fieldID := strings.TrimSpace(field.ID)
	return commandPanelAction{prompt: &commandPanelPrompt{
		title:         "Set " + fieldID,
		prompt:        firstNonEmpty(field.Label, fieldID),
		details:       []PromptDetail{{Label: "Model", Value: modelRef, Emphasis: true}, {Label: "Current", Value: strings.TrimSpace(field.Value)}},
		choices:       controllerPromptChoices(field.Options),
		defaultChoice: firstNonEmpty(field.Value, firstControllerOptionValue(field.Options)),
		filterable:    true,
		buildLine: func(value string) string {
			value = strings.TrimSpace(value)
			if value == "" {
				return ""
			}
			return "/model use " + modelRef + " " + value
		},
	}}
}

func controllerConfigFieldCommandPanelAction(field appviewmodel.ControllerPanelField, optionID string) commandPanelAction {
	optionID = strings.TrimSpace(optionID)
	fieldID := strings.TrimSpace(field.ID)
	details := []PromptDetail{
		{Label: "Option", Value: optionID, Emphasis: true},
		{Label: "Current", Value: strings.TrimSpace(field.Value)},
	}
	if len(field.Options) > 0 {
		return commandPanelAction{prompt: &commandPanelPrompt{
			title:         "Set " + firstNonEmpty(fieldID, optionID),
			prompt:        firstNonEmpty(field.Label, optionID),
			details:       details,
			choices:       controllerPromptChoices(field.Options),
			defaultChoice: firstNonEmpty(field.Value, firstControllerOptionValue(field.Options)),
			filterable:    true,
			buildLine: func(value string) string {
				value = strings.TrimSpace(value)
				if value == "" {
					return ""
				}
				return "/controller set " + optionID + " " + value
			},
		}}
	}
	return commandPanelAction{prompt: &commandPanelPrompt{
		title:        "Set " + firstNonEmpty(fieldID, optionID),
		prompt:       firstNonEmpty(field.Label, optionID),
		details:      details,
		defaultInput: strings.TrimSpace(field.Value),
		buildLine: func(value string) string {
			value = strings.TrimSpace(value)
			if value == "" && strings.TrimSpace(field.Value) == "" {
				return ""
			}
			return "/controller set " + optionID + " " + value
		},
	}}
}

func confirmCommandPanelPrompt(title string, prompt string, details []PromptDetail, line string) *commandPanelPrompt {
	return &commandPanelPrompt{
		title:         title,
		prompt:        prompt,
		details:       details,
		defaultChoice: "cancel",
		choices: []PromptChoice{
			{Label: "Run", Value: "run"},
			{Label: "Cancel", Value: "cancel"},
		},
		buildLine: func(value string) string {
			if strings.EqualFold(strings.TrimSpace(value), "run") {
				return line
			}
			return ""
		},
	}
}

func settingsPromptChoices(options []appviewmodel.SettingsPanelFieldOption) []PromptChoice {
	out := make([]PromptChoice, 0, len(options))
	for _, option := range options {
		value := strings.TrimSpace(option.Value)
		if value == "" {
			continue
		}
		out = append(out, PromptChoice{
			Label:  firstNonEmpty(option.Label, value),
			Value:  value,
			Detail: strings.TrimSpace(option.Description),
		})
	}
	return out
}

func controllerPromptChoices(options []appviewmodel.ControllerConfigChoice) []PromptChoice {
	out := make([]PromptChoice, 0, len(options))
	for _, option := range options {
		value := strings.TrimSpace(option.Value)
		if value == "" {
			continue
		}
		out = append(out, PromptChoice{
			Label:  firstNonEmpty(option.Name, value),
			Value:  value,
			Detail: strings.TrimSpace(option.Description),
		})
	}
	return out
}

func firstSettingsOptionValue(options []appviewmodel.SettingsPanelFieldOption) string {
	for _, option := range options {
		if value := strings.TrimSpace(option.Value); value != "" {
			return value
		}
	}
	return ""
}

func firstControllerOptionValue(options []appviewmodel.ControllerConfigChoice) string {
	for _, option := range options {
		if value := strings.TrimSpace(option.Value); value != "" {
			return value
		}
	}
	return ""
}

func findSettingsPanelField(panel appviewmodel.SettingsPanelView, id string) (appviewmodel.SettingsPanelField, bool) {
	id = strings.TrimSpace(id)
	for _, section := range panel.Sections {
		for _, field := range section.Fields {
			if strings.TrimSpace(field.ID) == id {
				return field, true
			}
		}
	}
	return appviewmodel.SettingsPanelField{}, false
}

func findSettingsPanelAction(actions []appviewmodel.SettingsPanelAction, id string) (appviewmodel.SettingsPanelAction, bool) {
	id = strings.TrimSpace(id)
	for _, action := range actions {
		if strings.TrimSpace(action.ID) == id {
			return action, true
		}
	}
	return appviewmodel.SettingsPanelAction{}, false
}

func findModelSelectionChoice(panel appviewmodel.ModelSelectionView, id string) (appviewmodel.ModelChoice, bool) {
	id = strings.TrimSpace(id)
	for _, choice := range panel.Configured {
		for _, candidate := range []string{choice.ID, choice.Alias, choice.Model} {
			if modelSelectionIDsMatch(candidate, id) {
				return choice, true
			}
		}
	}
	return appviewmodel.ModelChoice{}, false
}

func agentPanelAllActions(panel appviewmodel.AgentManagementView) []appviewmodel.AgentManagementAction {
	var actions []appviewmodel.AgentManagementAction
	actions = append(actions, panel.Actions...)
	for _, item := range panel.Registered {
		actions = append(actions, item.Actions...)
	}
	for _, item := range panel.Builtins {
		actions = append(actions, item.Actions...)
	}
	for _, item := range panel.Installable {
		actions = append(actions, item.Actions...)
	}
	return actions
}

func findControllerPanelField(panel appviewmodel.ControllerPanelView, id string) (appviewmodel.ControllerPanelField, bool) {
	id = strings.TrimSpace(id)
	for _, section := range panel.Sections {
		for _, field := range section.Fields {
			if strings.TrimSpace(field.ID) == id {
				return field, true
			}
		}
	}
	return appviewmodel.ControllerPanelField{}, false
}

func controllerPanelCurrentModel(panel appviewmodel.ControllerPanelView) string {
	if model := strings.TrimSpace(panel.Summary.Model); model != "" {
		return model
	}
	if field, ok := findControllerPanelField(panel, "controller.model"); ok {
		return strings.TrimSpace(field.Value)
	}
	if panel.Status != nil {
		return strings.TrimSpace(panel.Status.Model)
	}
	return ""
}

func taskByID(tasks []appviewmodel.TaskItem, id string) appviewmodel.TaskItem {
	id = strings.TrimSpace(id)
	for _, task := range tasks {
		if strings.TrimSpace(task.ID) == id {
			return task
		}
	}
	return appviewmodel.TaskItem{}
}
