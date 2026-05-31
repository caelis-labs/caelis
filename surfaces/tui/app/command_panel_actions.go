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
		return resumeCommandPanelAction(*view.ResumePanel, input)
	case view.ApprovalPanel != nil:
		return approvalCommandPanelAction(*view.ApprovalPanel, input)
	case view.ModelSelection != nil:
		return modelSelectionCommandPanelAction(*view.ModelSelection, input)
	case view.ControllerPanel != nil:
		return controllerCommandPanelAction(*view.ControllerPanel, input)
	case view.ModelConnectPanel != nil:
		return connectCommandPanelAction(*view.ModelConnectPanel, input)
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
	for _, action := range panel.Actions {
		command := settingsPanelActionCommand(action)
		if !action.Enabled || command == "" || command != input {
			continue
		}
		if action.Destructive || action.RequiresConfirmation {
			return commandPanelAction{prompt: confirmCommandPanelPrompt(
				"Run "+strings.TrimSpace(action.ID)+"?",
				"Confirm settings action",
				[]PromptDetail{
					{Label: "Action", Value: strings.TrimSpace(action.ID), Emphasis: true},
					{Label: "Description", Value: strings.TrimSpace(action.Description)},
				},
				settingsPanelConfirmedActionCommand(action),
			)}
		}
		return commandPanelAction{line: command}
	}
	if actionID, ok := strings.CutPrefix(input, "/settings run "); ok {
		action, ok := findSettingsPanelAction(panel.Actions, strings.TrimSpace(actionID))
		if !ok || !action.Enabled {
			return commandPanelAction{fillInput: input}
		}
		line := settingsPanelActionCommand(action)
		if line == "" || !strings.HasPrefix(strings.ToLower(line), "/settings run ") {
			return commandPanelAction{fillInput: input}
		}
		if action.Destructive || action.RequiresConfirmation {
			return commandPanelAction{prompt: confirmCommandPanelPrompt(
				"Run "+strings.TrimSpace(action.ID)+"?",
				"Confirm settings action",
				[]PromptDetail{
					{Label: "Action", Value: strings.TrimSpace(action.ID), Emphasis: true},
					{Label: "Description", Value: strings.TrimSpace(action.Description)},
				},
				settingsPanelConfirmedActionCommand(action),
			)}
		}
		return commandPanelAction{line: line}
	}
	return commandPanelAction{fillInput: input}
}

func settingsFieldCommandPanelAction(field appviewmodel.SettingsPanelField) commandPanelAction {
	fieldID := strings.TrimSpace(field.ID)
	command := strings.TrimSpace(field.Command)
	if command == "" {
		command = "/settings set " + fieldID + " "
	}
	command = commandPanelEnsureTrailingSpace(command)
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
				return command + value
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
			return command + value
		},
	}}
}

func settingsPanelActionCommand(action appviewmodel.SettingsPanelAction) string {
	command := strings.TrimSpace(action.Command)
	if command == "" && strings.TrimSpace(action.ID) != "" {
		command = "/settings run " + strings.TrimSpace(action.ID)
	}
	return command
}

func settingsPanelConfirmedActionCommand(action appviewmodel.SettingsPanelAction) string {
	command := settingsPanelActionCommand(action)
	if strings.HasPrefix(strings.ToLower(command), "/settings run ") {
		return command + " confirm"
	}
	return command
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
	return commandPanelAction{fillInput: input}
}

func connectCommandPanelAction(panel appviewmodel.ModelConnectView, input string) commandPanelAction {
	for _, provider := range panel.Providers {
		command := strings.TrimSpace(provider.Command)
		if command == "" || command != input {
			continue
		}
		return commandPanelAction{fillInput: commandPanelEnsureTrailingSpace(command)}
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

func resumeCommandPanelAction(panel appviewmodel.ResumePanelView, input string) commandPanelAction {
	for _, item := range panel.Sessions {
		for _, action := range item.Actions {
			command := strings.TrimSpace(action.Command)
			if !action.Enabled || command == "" || command != input {
				continue
			}
			return commandPanelAction{line: command}
		}
	}
	return commandPanelAction{fillInput: input}
}

func approvalCommandPanelAction(panel appviewmodel.ApprovalPanelView, input string) commandPanelAction {
	for _, mode := range panel.ModeOptions {
		command := strings.TrimSpace(mode.Command)
		if mode.Current || command == "" || command != input {
			continue
		}
		return commandPanelAction{line: command}
	}
	for _, action := range panel.Actions {
		command := strings.TrimSpace(action.Command)
		if !action.Enabled || command == "" || command != input {
			continue
		}
		return commandPanelAction{line: command}
	}
	return commandPanelAction{fillInput: input}
}

func taskCommandPanelAction(panel appviewmodel.TaskPanelView, input string) commandPanelAction {
	for _, action := range panel.Actions {
		if !action.Enabled {
			continue
		}
		command := taskPanelActionInput(action)
		if command != "" && strings.TrimSpace(command) == input {
			return taskPanelActionCommand(action, taskByID(panel.Tasks, action.TaskID))
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
	kind := taskPanelActionKind(action)
	command := taskPanelActionInput(action)
	if command == "" {
		return commandPanelAction{}
	}
	details := []PromptDetail{
		{Label: "Task", Value: firstNonEmpty(taskID, task.ID), Emphasis: true},
		{Label: "Title", Value: strings.TrimSpace(firstNonEmpty(task.Title, task.Command))},
	}
	switch kind {
	case "start", "run":
		prefix := commandPanelEnsureTrailingSpace(command)
		return commandPanelAction{prompt: &commandPanelPrompt{
			title:  "Start task",
			prompt: "Command",
			buildLine: func(value string) string {
				value = strings.TrimSpace(value)
				if value == "" {
					return ""
				}
				return prefix + value
			},
		}}
	case "write":
		if taskID == "" {
			return commandPanelAction{}
		}
		prefix := commandPanelEnsureTrailingSpace(command)
		return commandPanelAction{prompt: &commandPanelPrompt{
			title:   "Write to task",
			prompt:  "Input",
			details: details,
			buildLine: func(value string) string {
				value = strings.TrimSpace(value)
				if value == "" {
					return ""
				}
				return prefix + value
			},
		}}
	case "cancel":
		if taskID == "" {
			return commandPanelAction{}
		}
		line := strings.TrimSpace(command)
		return commandPanelAction{prompt: confirmCommandPanelPrompt(
			"Cancel task?",
			"Confirm task cancel",
			details,
			line,
		)}
	case "tail", "show":
		if line := strings.TrimSpace(command); line != "" {
			return commandPanelAction{line: line}
		}
	case "wait":
		if line := strings.TrimSpace(command); line != "" {
			return commandPanelAction{line: line}
		}
	case "release", "close":
		if line := strings.TrimSpace(command); line != "" {
			return commandPanelAction{line: line}
		}
	}
	return commandPanelAction{}
}

func taskPanelActionInput(action appviewmodel.TaskPanelAction) string {
	command := strings.TrimSpace(action.Command)
	if command == "" {
		return ""
	}
	if action.RequiresInput {
		return commandPanelEnsureTrailingSpace(command)
	}
	return command
}

func taskPanelActionKind(action appviewmodel.TaskPanelAction) string {
	kind := strings.ToLower(strings.TrimSpace(action.Kind))
	if kind != "" {
		return kind
	}
	kind, _, _ = strings.Cut(strings.TrimPrefix(strings.TrimSpace(action.ID), "task."), ":")
	return strings.ToLower(strings.TrimSpace(kind))
}

func controllerCommandPanelAction(panel appviewmodel.ControllerPanelView, input string) commandPanelAction {
	if !panel.Active {
		return commandPanelAction{fillInput: input}
	}
	for _, section := range panel.Sections {
		for _, field := range section.Fields {
			command := controllerPanelFieldInput(field)
			if !field.Editable || command == "" || strings.TrimSpace(command) != input {
				continue
			}
			return controllerFieldCommandPanelAction(field)
		}
		for _, action := range section.Actions {
			if result, ok := controllerPanelActionCommand(panel, action, input); ok {
				return result
			}
		}
	}
	for _, action := range panel.Actions {
		if result, ok := controllerPanelActionCommand(panel, action, input); ok {
			return result
		}
	}
	return commandPanelAction{fillInput: input}
}

func controllerPanelActionCommand(panel appviewmodel.ControllerPanelView, action appviewmodel.ControllerPanelAction, input string) (commandPanelAction, bool) {
	command := controllerPanelActionInput(action)
	if !action.Enabled || command == "" || strings.TrimSpace(command) != input {
		return commandPanelAction{}, false
	}
	if action.RequiresInput {
		switch strings.TrimSpace(action.ID) {
		case "controller.model.set":
			if field, ok := findControllerPanelField(panel, "controller.model"); ok && field.Editable {
				return controllerFieldCommandPanelAction(field), true
			}
		case "controller.mode.set":
			if field, ok := findControllerPanelField(panel, "controller.mode"); ok && field.Editable {
				return controllerFieldCommandPanelAction(field), true
			}
		}
		return commandPanelAction{fillInput: command}, true
	}
	return commandPanelAction{line: strings.TrimSpace(command)}, true
}

func controllerFieldCommandPanelAction(field appviewmodel.ControllerPanelField) commandPanelAction {
	fieldID := strings.TrimSpace(field.ID)
	prefix := commandPanelEnsureTrailingSpace(controllerPanelFieldInput(field))
	details := []PromptDetail{
		{Label: "Field", Value: fieldID, Emphasis: true},
		{Label: "Current", Value: strings.TrimSpace(field.Value)},
	}
	if len(field.Options) > 0 {
		return commandPanelAction{prompt: &commandPanelPrompt{
			title:         "Set " + fieldID,
			prompt:        firstNonEmpty(field.Label, fieldID),
			details:       details,
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
			return prefix + value
		},
	}}
}

func controllerPanelFieldInput(field appviewmodel.ControllerPanelField) string {
	command := strings.TrimSpace(field.Command)
	if command == "" {
		return ""
	}
	return commandPanelEnsureTrailingSpace(command)
}

func controllerPanelActionInput(action appviewmodel.ControllerPanelAction) string {
	command := strings.TrimSpace(action.Command)
	if command == "" {
		return ""
	}
	if action.RequiresInput {
		return commandPanelEnsureTrailingSpace(command)
	}
	return command
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

func taskByID(tasks []appviewmodel.TaskItem, id string) appviewmodel.TaskItem {
	id = strings.TrimSpace(id)
	for _, task := range tasks {
		if strings.TrimSpace(task.ID) == id {
			return task
		}
	}
	return appviewmodel.TaskItem{}
}
