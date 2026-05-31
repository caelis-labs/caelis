package tuiapp

import (
	"fmt"
	"net/url"
	"strings"

	tea "charm.land/bubbletea/v2"

	appviewmodel "github.com/OnslaughtSnail/caelis/internal/app/viewmodel"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

type CommandPanelBlock struct {
	id   string
	view appviewmodel.CommandExecutionView
}

func NewCommandPanelBlock(view appviewmodel.CommandExecutionView) *CommandPanelBlock {
	return &CommandPanelBlock{id: nextBlockID(), view: view}
}

func (b *CommandPanelBlock) BlockID() string { return b.id }
func (b *CommandPanelBlock) Kind() BlockKind { return BlockCommandPanel }

func (b *CommandPanelBlock) Render(ctx BlockRenderContext) []RenderedRow {
	lines := renderCommandPanelLines(b.view, ctx)
	clickHints := commandPanelClickHints(b.view)
	rows := make([]RenderedRow, 0, len(lines))
	for _, line := range lines {
		plain := ansi.Strip(line)
		rows = append(rows, RenderedRow{
			Styled:     line,
			Plain:      plain,
			BlockID:    b.id,
			ClickToken: commandPanelClickTokenForLine(plain, clickHints),
			PreWrapped: true,
		})
	}
	return rows
}

func (m *Model) handleCommandPanelMsg(msg CommandPanelMsg) (tea.Model, tea.Cmd) {
	if !commandExecutionHasPanel(msg.View) {
		return m.appendPlainTranscriptBlock(msg.View.Output)
	}
	m.finalizeAssistantBlock()
	m.finalizeReasoningBlock()
	block := NewCommandPanelBlock(msg.View)
	m.doc.Append(block)
	m.hasCommittedLine = true
	m.lastCommittedStyle = tuikit.LineStyleDefault
	m.syncViewportContent()
	return m, nil
}

func commandExecutionHasPanel(view appviewmodel.CommandExecutionView) bool {
	return view.SettingsPanel != nil ||
		view.TaskPanel != nil ||
		view.ResumePanel != nil ||
		view.ApprovalPanel != nil ||
		view.ControllerPanel != nil ||
		view.ModelConnectPanel != nil ||
		view.AgentManagement != nil
}

type commandPanelClickHint struct {
	Needle string
	Input  string
}

const commandPanelInputTokenPrefix = "command_panel_input:"

func commandPanelInputClickToken(input string) string {
	if strings.TrimSpace(input) == "" {
		return ""
	}
	return commandPanelInputTokenPrefix + url.QueryEscape(input)
}

func commandPanelInputFromClickToken(token string) (string, bool) {
	raw, ok := strings.CutPrefix(strings.TrimSpace(token), commandPanelInputTokenPrefix)
	if !ok {
		return "", false
	}
	input, err := url.QueryUnescape(raw)
	if err != nil {
		return "", false
	}
	return input, strings.TrimSpace(input) != ""
}

func commandPanelClickTokenForLine(plain string, hints []commandPanelClickHint) string {
	plain = strings.TrimSpace(plain)
	if plain == "" {
		return ""
	}
	for _, hint := range hints {
		needle := strings.TrimSpace(hint.Needle)
		if needle == "" || strings.TrimSpace(hint.Input) == "" {
			continue
		}
		if strings.Contains(plain, needle) {
			return commandPanelInputClickToken(hint.Input)
		}
	}
	return ""
}

func commandPanelClickHints(view appviewmodel.CommandExecutionView) []commandPanelClickHint {
	switch {
	case view.SettingsPanel != nil:
		return settingsPanelClickHints(*view.SettingsPanel)
	case view.TaskPanel != nil:
		return taskPanelClickHints(*view.TaskPanel)
	case view.ResumePanel != nil:
		return resumePanelClickHints(*view.ResumePanel)
	case view.ApprovalPanel != nil:
		return approvalPanelClickHints(*view.ApprovalPanel)
	case view.ControllerPanel != nil:
		return controllerPanelClickHints(*view.ControllerPanel)
	case view.ModelConnectPanel != nil:
		return connectPanelClickHints(*view.ModelConnectPanel)
	case view.AgentManagement != nil:
		return agentPanelClickHints(*view.AgentManagement)
	default:
		return nil
	}
}

func renderCommandPanelLines(view appviewmodel.CommandExecutionView, ctx BlockRenderContext) []string {
	width := maxInt(28, ctx.Width)
	kind, title, state, body, footer := commandPanelParts(view, width, ctx.Theme)
	if len(body) == 0 {
		body = commandPanelFallbackBody(view.Output, width, ctx.Theme)
	}
	if footer == "" {
		footer = commandPanelFooterForCommand(view.Command)
	}
	return tuikit.RenderBlockShell(ctx.Theme, tuikit.BlockShellModel{
		Variant:  tuikit.BlockShellBox,
		Width:    width,
		Expanded: true,
		Kind:     kind,
		Title:    title,
		State:    state,
		Body:     body,
		Footer:   footer,
	})
}

func commandPanelParts(view appviewmodel.CommandExecutionView, width int, theme tuikit.Theme) (kind string, title string, state string, body []string, footer string) {
	switch {
	case view.SettingsPanel != nil:
		return "settings", "Configuration", settingsPanelState(*view.SettingsPanel), settingsPanelBody(*view.SettingsPanel, view.Output, width, theme), commandPanelFooterForCommand("settings")
	case view.TaskPanel != nil:
		return "tasks", "Async Work", taskPanelState(*view.TaskPanel), taskPanelBody(*view.TaskPanel, width, theme), commandPanelFooterForCommand("task")
	case view.ResumePanel != nil:
		return "sessions", "Resume Session", resumePanelState(*view.ResumePanel), resumePanelBody(*view.ResumePanel, width, theme), commandPanelFooterForCommand("resume")
	case view.ApprovalPanel != nil:
		return "approval", "Approval", approvalPanelState(*view.ApprovalPanel), approvalPanelBody(*view.ApprovalPanel, width, theme), commandPanelFooterForCommand("approval")
	case view.ControllerPanel != nil:
		return "controller", "ACP Controller", controllerPanelState(*view.ControllerPanel), controllerPanelBody(*view.ControllerPanel, width, theme), commandPanelFooterForCommand("controller")
	case view.ModelConnectPanel != nil:
		return "connect", "Model Setup", connectPanelState(*view.ModelConnectPanel), connectPanelBody(*view.ModelConnectPanel, width, theme), commandPanelFooterForCommand("connect")
	case view.AgentManagement != nil:
		return "agents", "Agent Registry", "ready", agentPanelBody(*view.AgentManagement, width, theme), commandPanelFooterForCommand("agent")
	default:
		command := strings.ToUpper(strings.TrimSpace(view.Command))
		if command == "" {
			command = "COMMAND"
		}
		return command, "Command", "ready", nil, ""
	}
}

func settingsPanelBody(panel appviewmodel.SettingsPanelView, output string, width int, theme tuikit.Theme) []string {
	contentWidth := commandPanelContentWidth(width)
	tok := theme.Tokens()
	var body []string
	if banner := commandPanelOutputBanner(output, "settings"); banner != "" {
		body = append(body, tok.Success.Render(banner), "")
	}
	body = append(body,
		commandPanelKV(theme, contentWidth, "configured", fmt.Sprint(panel.Configured)),
		commandPanelKV(theme, contentWidth, "workspace", firstNonEmpty(panel.Runtime.WorkspaceCWD, panel.Settings.Runtime.WorkspaceCWD, "-")),
		commandPanelKV(theme, contentWidth, "store", strings.TrimSpace(firstNonEmpty(panel.Settings.Store.Backend, "-")+" "+panel.Settings.Store.URI)),
		commandPanelKV(theme, contentWidth, "model", settingsPanelModelLabel(panel.Model)),
		commandPanelKV(theme, contentWidth, "sandbox", settingsPanelSandboxLabel(panel.Sandbox.Status)),
	)
	body = appendCommandPanelDiagnostics(body, settingsDiagnosticsAsRows(panel.Diagnostics), contentWidth, theme)
	if len(panel.Sections) > 0 {
		body = append(body, "", tok.ChromeMeta.Render("Fields"))
		for _, section := range panel.Sections {
			title := firstNonEmpty(section.Title, section.ID)
			if strings.TrimSpace(title) != "" {
				body = append(body, commandPanelSubhead(theme, title))
			}
			for _, field := range section.Fields {
				body = append(body, settingsPanelFieldLine(field, contentWidth, theme))
			}
		}
	}
	if len(panel.Actions) > 0 {
		body = append(body, "", tok.ChromeMeta.Render("Actions"))
		for _, action := range panel.Actions {
			body = append(body, settingsPanelActionLine(action, contentWidth, theme))
		}
	}
	return body
}

func taskPanelBody(panel appviewmodel.TaskPanelView, width int, theme tuikit.Theme) []string {
	contentWidth := commandPanelContentWidth(width)
	tok := theme.Tokens()
	if !panel.Supported {
		return []string{tok.Warning.Render("Task service is not available")}
	}
	body := []string{
		commandPanelKV(theme, contentWidth, "summary", taskPanelSummaryLabel(panel.Summary)),
	}
	tasksByID := make(map[string]appviewmodel.TaskItem, len(panel.Tasks))
	for _, task := range panel.Tasks {
		tasksByID[strings.TrimSpace(task.ID)] = task
	}
	if len(panel.Sections) > 0 {
		for _, section := range panel.Sections {
			if len(section.TaskIDs) == 0 {
				continue
			}
			body = append(body, "", commandPanelSubhead(theme, firstNonEmpty(section.Title, section.ID)))
			for _, taskID := range section.TaskIDs {
				task, ok := tasksByID[strings.TrimSpace(taskID)]
				if !ok {
					continue
				}
				body = append(body, taskPanelTaskLine(task, contentWidth, theme))
			}
		}
	} else if len(panel.Tasks) > 0 {
		body = append(body, "", tok.ChromeMeta.Render("Tasks"))
		for _, task := range panel.Tasks {
			body = append(body, taskPanelTaskLine(task, contentWidth, theme))
		}
	}
	if len(panel.Actions) > 0 {
		body = append(body, "", tok.ChromeMeta.Render("Actions"))
		for _, action := range panel.Actions {
			body = append(body, taskPanelActionLine(action, contentWidth, theme))
		}
	}
	body = appendCommandPanelDiagnostics(body, taskDiagnosticsAsRows(panel.Diagnostics), contentWidth, theme)
	return body
}

func resumePanelBody(panel appviewmodel.ResumePanelView, width int, theme tuikit.Theme) []string {
	contentWidth := commandPanelContentWidth(width)
	tok := theme.Tokens()
	body := []string{
		commandPanelKV(theme, contentWidth, "workspace", firstNonEmpty(panel.Workspace.CWD, panel.Workspace.Key, "-")),
		commandPanelKV(theme, contentWidth, "sessions", fmt.Sprintf("%d", len(panel.Sessions))),
	}
	if search := strings.TrimSpace(panel.Search); search != "" {
		body = append(body, commandPanelKV(theme, contentWidth, "search", search))
	}
	if len(panel.Sessions) == 0 {
		body = append(body, "", tok.TextSecondary.Render("No sessions available"))
		return body
	}
	body = append(body, "", tok.ChromeMeta.Render("Recent"))
	for _, item := range panel.Sessions {
		body = append(body, resumePanelSessionLine(item, contentWidth, theme))
	}
	return body
}

func approvalPanelBody(panel appviewmodel.ApprovalPanelView, width int, theme tuikit.Theme) []string {
	contentWidth := commandPanelContentWidth(width)
	tok := theme.Tokens()
	body := []string{
		commandPanelKV(theme, contentWidth, "scope", firstNonEmpty(panel.Scope, "session")),
		commandPanelKV(theme, contentWidth, "mode", approvalPanelModeLabel(panel)),
	}
	if strings.EqualFold(strings.TrimSpace(panel.Scope), "controller") {
		body = append(body, commandPanelKV(theme, contentWidth, "controller", firstNonEmpty(panel.ControllerAgent, panel.RemoteSessionID, "-")))
	}
	if len(panel.ModeOptions) > 0 {
		body = append(body, "", tok.ChromeMeta.Render("Modes"))
		for _, mode := range panel.ModeOptions {
			body = append(body, approvalPanelModeLine(mode, contentWidth, theme))
		}
	}
	if len(panel.Pending) > 0 {
		body = append(body, "", tok.ChromeMeta.Render("Pending"))
		for _, item := range panel.Pending {
			body = append(body, approvalPanelPendingLine(item, contentWidth, theme))
		}
	}
	if len(panel.Actions) > 0 {
		body = append(body, "", tok.ChromeMeta.Render("Actions"))
		for _, action := range panel.Actions {
			body = append(body, approvalPanelActionLine(action, contentWidth, theme))
		}
	}
	return body
}

func controllerPanelBody(panel appviewmodel.ControllerPanelView, width int, theme tuikit.Theme) []string {
	contentWidth := commandPanelContentWidth(width)
	tok := theme.Tokens()
	if !panel.Active {
		return []string{tok.TextSecondary.Render("Local model controller is active")}
	}
	body := []string{
		commandPanelKV(theme, contentWidth, "agent", firstNonEmpty(panel.Summary.Agent, "-")),
		commandPanelKV(theme, contentWidth, "remote", firstNonEmpty(panel.Summary.RemoteSessionID, "-")),
		commandPanelKV(theme, contentWidth, "model", firstNonEmpty(panel.Summary.Model, "-")),
		commandPanelKV(theme, contentWidth, "mode", firstNonEmpty(panel.Summary.Mode, "-")),
		commandPanelKV(theme, contentWidth, "phase", firstNonEmpty(panel.Summary.Phase, "-")),
	}
	for _, section := range panel.Sections {
		if len(section.Fields) == 0 {
			continue
		}
		body = append(body, "", commandPanelSubhead(theme, firstNonEmpty(section.Title, section.ID)))
		for _, field := range section.Fields {
			body = append(body, controllerPanelFieldLine(field, contentWidth, theme))
		}
		for _, action := range section.Actions {
			body = append(body, controllerPanelActionLine(action, contentWidth, theme))
		}
	}
	if len(panel.Actions) > 0 {
		body = append(body, "", tok.ChromeMeta.Render("Actions"))
		for _, action := range panel.Actions {
			body = append(body, controllerPanelActionLine(action, contentWidth, theme))
		}
	}
	body = appendCommandPanelDiagnostics(body, controllerDiagnosticsAsRows(panel.Diagnostics), contentWidth, theme)
	return body
}

func connectPanelBody(panel appviewmodel.ModelConnectView, width int, theme tuikit.Theme) []string {
	contentWidth := commandPanelContentWidth(width)
	tok := theme.Tokens()
	current := "not configured"
	if panel.Current != nil {
		current = firstNonEmpty(panel.Current.Alias, panel.Current.ID, panel.Current.Model)
	} else if len(panel.Configured) > 0 {
		current = fmt.Sprintf("%d configured models", len(panel.Configured))
	}
	body := []string{commandPanelKV(theme, contentWidth, "current", current)}
	if len(panel.Providers) > 0 {
		body = append(body, "", tok.ChromeMeta.Render("Providers"))
		for _, provider := range panel.Providers {
			body = append(body, connectPanelProviderLine(provider, contentWidth, theme))
		}
	}
	if len(panel.Wizard.Steps) > 0 {
		body = append(body, "", commandPanelKV(theme, contentWidth, "wizard", fmt.Sprintf("%s (%d steps)", firstNonEmpty(panel.Wizard.DisplayLine, "/connect"), len(panel.Wizard.Steps))))
	}
	body = appendCommandPanelDiagnostics(body, connectDiagnosticsAsRows(panel.Diagnostics), contentWidth, theme)
	return body
}

func agentPanelBody(panel appviewmodel.AgentManagementView, width int, theme tuikit.Theme) []string {
	contentWidth := commandPanelContentWidth(width)
	tok := theme.Tokens()
	body := []string{
		commandPanelKV(theme, contentWidth, "registered", fmt.Sprintf("%d", len(panel.Registered))),
		commandPanelKV(theme, contentWidth, "builtins", fmt.Sprintf("%d", len(panel.Builtins))),
		commandPanelKV(theme, contentWidth, "installable", fmt.Sprintf("%d", len(panel.Installable))),
	}
	if len(panel.Registered) > 0 {
		body = append(body, "", tok.ChromeMeta.Render("Registered"))
		for _, item := range panel.Registered {
			body = append(body, agentPanelItemLine(item.Agent, contentWidth, theme))
		}
	}
	if len(panel.Builtins) > 0 {
		body = append(body, "", tok.ChromeMeta.Render("Built-ins"))
		for _, item := range panel.Builtins {
			body = append(body, agentPanelItemLine(item.Agent, contentWidth, theme))
		}
	}
	return body
}

func settingsPanelClickHints(panel appviewmodel.SettingsPanelView) []commandPanelClickHint {
	var hints []commandPanelClickHint
	for _, section := range panel.Sections {
		for _, field := range section.Fields {
			if !field.Editable {
				continue
			}
			fieldID := strings.TrimSpace(field.ID)
			if fieldID == "" {
				continue
			}
			hints = append(hints, commandPanelClickHint{
				Needle: fieldID,
				Input:  "/settings set " + fieldID + " ",
			})
		}
	}
	for _, action := range panel.Actions {
		if !action.Enabled {
			continue
		}
		actionID := strings.TrimSpace(action.ID)
		if actionID == "" {
			continue
		}
		hints = append(hints, commandPanelClickHint{
			Needle: actionID,
			Input:  "/settings run " + actionID,
		})
	}
	return hints
}

func taskPanelClickHints(panel appviewmodel.TaskPanelView) []commandPanelClickHint {
	hints := make([]commandPanelClickHint, 0, len(panel.Tasks))
	for _, action := range panel.Actions {
		hints = append(hints, taskPanelActionClickHint(action)...)
	}
	for _, task := range panel.Tasks {
		taskID := strings.TrimSpace(task.ID)
		if taskID == "" {
			continue
		}
		hints = append(hints, commandPanelClickHint{
			Needle: taskID,
			Input:  "/task tail " + taskID,
		})
	}
	return hints
}

func taskPanelActionClickHint(action appviewmodel.TaskPanelAction) []commandPanelClickHint {
	if !action.Enabled {
		return nil
	}
	actionID := strings.TrimSpace(action.ID)
	taskID := strings.TrimSpace(action.TaskID)
	kind := strings.ToLower(strings.TrimSpace(action.Kind))
	if kind == "" {
		kind, _, _ = strings.Cut(strings.TrimPrefix(actionID, "task."), ":")
	}
	var input string
	switch kind {
	case "start", "run":
		input = "/task start -- "
	case "tail", "show":
		if taskID != "" {
			input = "/task tail " + taskID
		}
	case "wait":
		if taskID != "" {
			input = "/task wait " + taskID
		}
	case "write":
		if taskID != "" {
			input = "/task write " + taskID + " -- "
		}
	case "cancel":
		if taskID != "" {
			input = "/task cancel " + taskID
		}
	case "release", "close":
		if taskID != "" {
			input = "/task release " + taskID
		}
	}
	if strings.TrimSpace(input) == "" {
		return nil
	}
	return []commandPanelClickHint{{Needle: firstNonEmpty(actionID, action.Label, taskID), Input: input}}
}

func resumePanelClickHints(panel appviewmodel.ResumePanelView) []commandPanelClickHint {
	hints := make([]commandPanelClickHint, 0, len(panel.Sessions))
	for _, item := range panel.Sessions {
		input := strings.TrimSpace(item.Command)
		if input == "" && strings.TrimSpace(item.SessionID) != "" {
			input = "/resume " + strings.TrimSpace(item.SessionID)
		}
		if input == "" {
			continue
		}
		hints = append(hints, commandPanelClickHint{
			Needle: firstNonEmpty(item.SessionID, item.Title),
			Input:  input,
		})
	}
	return hints
}

func approvalPanelClickHints(panel appviewmodel.ApprovalPanelView) []commandPanelClickHint {
	var hints []commandPanelClickHint
	for _, mode := range panel.ModeOptions {
		if strings.TrimSpace(mode.Command) == "" || mode.Current {
			continue
		}
		hints = append(hints, commandPanelClickHint{
			Needle: firstNonEmpty(mode.Name, mode.ID),
			Input:  mode.Command,
		})
	}
	for _, action := range panel.Actions {
		if !action.Enabled || strings.TrimSpace(action.Command) == "" {
			continue
		}
		hints = append(hints, commandPanelClickHint{
			Needle: firstNonEmpty(action.ID, action.Label),
			Input:  action.Command,
		})
	}
	return hints
}

func controllerPanelClickHints(panel appviewmodel.ControllerPanelView) []commandPanelClickHint {
	if !panel.Active {
		return nil
	}
	var hints []commandPanelClickHint
	for _, section := range panel.Sections {
		for _, field := range section.Fields {
			if !field.Editable {
				continue
			}
			switch strings.TrimSpace(field.ID) {
			case "controller.model":
				hints = append(hints, commandPanelClickHint{Needle: firstNonEmpty(field.Label, field.ID), Input: "/model use "})
			case "controller.reasoning":
				if model := controllerPanelCurrentModel(panel); model != "" {
					hints = append(hints, commandPanelClickHint{Needle: firstNonEmpty(field.Label, field.ID), Input: "/model use " + model + " "})
				}
			case "controller.mode":
				hints = append(hints, commandPanelClickHint{Needle: firstNonEmpty(field.Label, field.ID), Input: "/approval "})
			default:
				if optionID, ok := strings.CutPrefix(strings.TrimSpace(field.ID), "controller.config."); ok {
					optionID = strings.TrimSpace(optionID)
					if optionID != "" && field.Editable {
						hints = append(hints, commandPanelClickHint{Needle: firstNonEmpty(field.Label, field.ID), Input: "/controller set " + optionID + " "})
					}
				}
			}
		}
		for _, action := range section.Actions {
			hints = append(hints, controllerPanelActionClickHint(action)...)
		}
	}
	for _, action := range panel.Actions {
		hints = append(hints, controllerPanelActionClickHint(action)...)
	}
	return hints
}

func controllerPanelActionClickHint(action appviewmodel.ControllerPanelAction) []commandPanelClickHint {
	if !action.Enabled {
		return nil
	}
	switch strings.TrimSpace(action.ID) {
	case "controller.handoff.local":
		return []commandPanelClickHint{{Needle: firstNonEmpty(action.ID, action.Label), Input: "/agent use local"}}
	case "controller.model.set":
		return []commandPanelClickHint{{Needle: firstNonEmpty(action.ID, action.Label), Input: "/model use "}}
	case "controller.mode.set":
		return []commandPanelClickHint{{Needle: firstNonEmpty(action.ID, action.Label), Input: "/approval "}}
	case "controller.mode.cycle":
		return []commandPanelClickHint{{Needle: firstNonEmpty(action.ID, action.Label), Input: "/approval toggle"}}
	default:
		return nil
	}
}

func connectPanelClickHints(panel appviewmodel.ModelConnectView) []commandPanelClickHint {
	hints := make([]commandPanelClickHint, 0, len(panel.Providers))
	for _, provider := range panel.Providers {
		providerID := firstNonEmpty(provider.Provider, provider.ID)
		label := firstNonEmpty(provider.Label, provider.Provider, provider.ID)
		if providerID == "" || label == "" {
			continue
		}
		hints = append(hints, commandPanelClickHint{
			Needle: label,
			Input:  "/connect " + providerID + " ",
		})
	}
	return hints
}

func agentPanelClickHints(panel appviewmodel.AgentManagementView) []commandPanelClickHint {
	var hints []commandPanelClickHint
	for _, item := range panel.Registered {
		name := firstNonEmpty(item.Agent.Name, item.Agent.ID)
		if name == "" {
			continue
		}
		hints = append(hints, commandPanelClickHint{
			Needle: name,
			Input:  "/agent use " + name,
		})
	}
	for _, item := range panel.Builtins {
		name := firstNonEmpty(item.Agent.Name, item.Agent.ID)
		if name == "" {
			continue
		}
		input := "/agent add " + name
		if item.Installable {
			input = "/agent install " + name
		}
		hints = append(hints, commandPanelClickHint{
			Needle: name,
			Input:  input,
		})
	}
	for _, item := range panel.Installable {
		name := firstNonEmpty(item.Name, item.ID)
		if name == "" {
			continue
		}
		hints = append(hints, commandPanelClickHint{
			Needle: name,
			Input:  "/agent install " + name,
		})
	}
	return hints
}

func commandPanelContentWidth(width int) int {
	return maxInt(20, width-8)
}

func commandPanelKV(theme tuikit.Theme, width int, label string, value string) string {
	tok := theme.Tokens()
	label = strings.TrimSpace(label)
	value = commandPanelOneLine(firstNonEmpty(value, "-"))
	plainPrefix := label + ": "
	valueWidth := maxInt(1, width-displayColumns(plainPrefix))
	return tok.ComposerLabel.Render(label+":") + " " + tok.TextPrimary.Render(truncateTailDisplay(value, valueWidth))
}

func commandPanelSubhead(theme tuikit.Theme, title string) string {
	return "  " + theme.Tokens().TextSecondary.Bold(true).Render(strings.TrimSpace(title))
}

func commandPanelOneLine(value string) string {
	value = strings.TrimSpace(value)
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.Join(strings.Fields(value), " ")
	return value
}

func commandPanelFooterForCommand(command string) string {
	switch strings.ToLower(strings.TrimSpace(command)) {
	case "settings":
		return "edit: /settings set <field-id> <value>  run: /settings run <action-id> [confirm]"
	case "task":
		return "actions: /task tail|wait|write|cancel|release <id>"
	case "resume":
		return "open: /resume <session-id>"
	case "approval":
		return "mode: /approval auto-review|manual|toggle"
	case "controller":
		return "handoff: /agent use local  config: /model use <model>, /approval <mode>, /controller set <option-id> <value>"
	case "connect":
		return "connect: /connect provider model [base-url] [timeout] [token]"
	case "agent":
		return "manage: /agent add|install|update|remove|use"
	default:
		return ""
	}
}

func commandPanelOutputBanner(output string, command string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	first, _, _ := strings.Cut(output, "\n")
	first = strings.TrimSpace(first)
	if first == "" || strings.EqualFold(first, strings.TrimSpace(command)+":") {
		return ""
	}
	return first
}

func commandPanelFallbackBody(output string, width int, theme tuikit.Theme) []string {
	output = strings.TrimSpace(output)
	if output == "" {
		return nil
	}
	lines := strings.Split(output, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		out = append(out, theme.Tokens().TextPrimary.Render(truncateTailDisplay(commandPanelOneLine(line), commandPanelContentWidth(width))))
	}
	return out
}

type commandPanelDiagnosticRow struct {
	Severity string
	Source   string
	Kind     string
	Message  string
}

func appendCommandPanelDiagnostics(body []string, diagnostics []commandPanelDiagnosticRow, width int, theme tuikit.Theme) []string {
	if len(diagnostics) == 0 {
		return body
	}
	lines := make([]string, 0, len(diagnostics))
	for _, diagnostic := range diagnostics {
		if strings.EqualFold(strings.TrimSpace(diagnostic.Severity), "info") {
			continue
		}
		lines = append(lines, commandPanelDiagnosticLine(diagnostic, width, theme))
	}
	if len(lines) == 0 {
		return body
	}
	body = append(body, "", theme.Tokens().ChromeMeta.Render("Diagnostics"))
	body = append(body, lines...)
	return body
}

func commandPanelDiagnosticLine(diagnostic commandPanelDiagnosticRow, width int, theme tuikit.Theme) string {
	tok := theme.Tokens()
	severity := firstNonEmpty(strings.TrimSpace(diagnostic.Severity), "info")
	label := strings.Trim(strings.TrimSpace(diagnostic.Source)+"/"+strings.TrimSpace(diagnostic.Kind), "/")
	if label == "" {
		label = "diagnostic"
	}
	text := "[" + severity + "] " + label
	if message := strings.TrimSpace(diagnostic.Message); message != "" {
		text += ": " + message
	}
	style := tok.TextSecondary
	switch strings.ToLower(severity) {
	case "error":
		style = tok.Danger
	case "warning", "warn":
		style = tok.Warning
	}
	return style.Render(truncateTailDisplay(commandPanelOneLine(text), width))
}

func settingsPanelState(panel appviewmodel.SettingsPanelView) string {
	switch settingsDiagnosticState(panel.Diagnostics) {
	case "error":
		return "failed"
	case "warning":
		return "warning"
	}
	return "ready"
}

func taskPanelState(panel appviewmodel.TaskPanelView) string {
	if !panel.Supported {
		return "failed"
	}
	if panel.Summary.Failed > 0 {
		return "warning"
	}
	if panel.Summary.Running > 0 {
		return "running"
	}
	return "ready"
}

func resumePanelState(panel appviewmodel.ResumePanelView) string {
	if len(panel.Sessions) == 0 {
		return "empty"
	}
	return "ready"
}

func approvalPanelState(panel appviewmodel.ApprovalPanelView) string {
	if len(panel.Pending) > 0 {
		return "waiting"
	}
	return firstNonEmpty(panel.CurrentMode, "ready")
}

func controllerPanelState(panel appviewmodel.ControllerPanelView) string {
	if !panel.Active {
		return "inactive"
	}
	if panel.Summary.Recovering {
		return "recovering"
	}
	if panel.Summary.Running {
		return "running"
	}
	if panel.Summary.Phase != "" {
		return panel.Summary.Phase
	}
	return "ready"
}

func connectPanelState(panel appviewmodel.ModelConnectView) string {
	switch connectDiagnosticState(panel.Diagnostics) {
	case "error":
		return "failed"
	case "warning":
		return "warning"
	}
	if panel.Current != nil || len(panel.Configured) > 0 {
		return "ready"
	}
	return "setup"
}

func settingsDiagnosticState(items []appviewmodel.SettingsPanelDiagnostic) string {
	state := ""
	for _, item := range items {
		switch strings.ToLower(strings.TrimSpace(item.Severity)) {
		case "error":
			return "error"
		case "warning", "warn":
			state = "warning"
		}
	}
	return state
}

func connectDiagnosticState(items []appviewmodel.ModelConnectDiagnostic) string {
	state := ""
	for _, item := range items {
		switch strings.ToLower(strings.TrimSpace(item.Severity)) {
		case "error":
			return "error"
		case "warning", "warn":
			state = "warning"
		}
	}
	return state
}

func settingsPanelModelLabel(status appviewmodel.ModelStatus) string {
	if status.Current != nil {
		current := firstNonEmpty(status.Current.Alias, status.Current.Model, status.Current.ID)
		if provider := strings.TrimSpace(status.Current.Provider); provider != "" && !strings.Contains(current, "/") {
			return provider + "/" + current
		}
		return current
	}
	if status.MissingAPIKey {
		return "missing API key"
	}
	return "not configured"
}

func settingsPanelSandboxLabel(status appviewmodel.SandboxPanelStatus) string {
	parts := []string{
		firstNonEmpty(status.ResolvedBackend, status.RequestedBackend, "unknown"),
		status.Route,
		status.Isolation,
	}
	if status.FallbackToHost {
		parts = append(parts, "host fallback")
	}
	if status.SetupRequired {
		parts = append(parts, "setup required")
	}
	return strings.Join(compactNonEmpty(parts), "  ")
}

func settingsPanelFieldLine(field appviewmodel.SettingsPanelField, width int, theme tuikit.Theme) string {
	tok := theme.Tokens()
	id := firstNonEmpty(field.ID, field.Label)
	value := field.Value
	if field.Sensitive && strings.TrimSpace(value) != "" {
		value = "(hidden)"
	}
	if value == "" {
		value = "-"
	}
	traits := []string{field.Kind}
	if field.Editable {
		traits = append(traits, "editable")
	} else {
		traits = append(traits, "readonly")
	}
	if len(field.Options) > 0 {
		traits = append(traits, fmt.Sprintf("%d options", len(field.Options)))
	}
	plain := fmt.Sprintf("  %s = %s  [%s]", id, commandPanelOneLine(value), strings.Join(compactNonEmpty(traits), ", "))
	return tok.TextPrimary.Render(truncateTailDisplay(plain, width))
}

func settingsPanelActionLine(action appviewmodel.SettingsPanelAction, width int, theme tuikit.Theme) string {
	tok := theme.Tokens()
	state := "disabled"
	style := tok.TextMuted
	if action.Enabled {
		state = "enabled"
		style = tok.TextPrimary
	}
	plain := fmt.Sprintf("  %s - %s (%s)", firstNonEmpty(action.ID, action.Label), firstNonEmpty(action.Label, action.Kind), state)
	if action.Destructive || action.RequiresConfirmation {
		plain += " confirm"
	}
	return style.Render(truncateTailDisplay(commandPanelOneLine(plain), width))
}

func taskPanelSummaryLabel(summary appviewmodel.TaskPanelSummary) string {
	parts := []string{}
	for _, item := range []struct {
		label string
		value int
	}{
		{"total", summary.Total},
		{"running", summary.Running},
		{"waiting", summary.Waiting},
		{"failed", summary.Failed},
		{"completed", summary.Completed},
		{"subagents", summary.Subagents},
	} {
		if item.value > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", item.label, item.value))
		}
	}
	return firstNonEmpty(strings.Join(parts, "  "), "none")
}

func taskPanelTaskLine(task appviewmodel.TaskItem, width int, theme tuikit.Theme) string {
	tok := theme.Tokens()
	state := firstNonEmpty(task.State, "unknown")
	if task.Running {
		state = firstNonEmpty(task.State, "running")
	}
	title := firstNonEmpty(task.Title, task.Command, task.Agent, task.ID)
	plain := fmt.Sprintf("  %s  %s  %s", task.ID, state, title)
	if task.Kind != "" || task.Source != "" {
		plain += "  [" + strings.Join(compactNonEmpty([]string{task.Kind, task.Source}), ", ") + "]"
	}
	style := tok.TextPrimary
	if task.Error != "" || strings.EqualFold(state, "failed") {
		style = tok.Danger
	}
	return style.Render(truncateTailDisplay(commandPanelOneLine(plain), width))
}

func taskPanelActionLine(action appviewmodel.TaskPanelAction, width int, theme tuikit.Theme) string {
	tok := theme.Tokens()
	state := "disabled"
	style := tok.TextMuted
	if action.Enabled {
		state = "enabled"
		style = tok.TextPrimary
	}
	parts := []string{firstNonEmpty(action.ID, action.Label, action.Kind)}
	if taskID := strings.TrimSpace(action.TaskID); taskID != "" {
		parts = append(parts, "task="+taskID)
	}
	parts = append(parts, state)
	if action.RequiresInput {
		parts = append(parts, "input")
	}
	if action.Destructive {
		parts = append(parts, "destructive")
	}
	plain := "  " + strings.Join(compactNonEmpty(parts), "  ")
	return style.Render(truncateTailDisplay(commandPanelOneLine(plain), width))
}

func resumePanelSessionLine(item appviewmodel.ResumeSessionItem, width int, theme tuikit.Theme) string {
	tok := theme.Tokens()
	sessionID := firstNonEmpty(item.SessionID, item.Ref.SessionID)
	title := firstNonEmpty(item.Title, "untitled")
	details := compactNonEmpty([]string{resumePanelTimestamp(item), item.Workspace})
	plain := "  " + strings.Join(compactNonEmpty([]string{sessionID, title}), "  ")
	if item.EventCount > 0 {
		details = append(details, fmt.Sprintf("%d events", item.EventCount))
	}
	if len(details) > 0 {
		plain += "  [" + strings.Join(details, ", ") + "]"
	}
	return tok.TextPrimary.Render(truncateTailDisplay(commandPanelOneLine(plain), width))
}

func resumePanelTimestamp(item appviewmodel.ResumeSessionItem) string {
	updatedAt := item.UpdatedAt
	if updatedAt.IsZero() {
		updatedAt = item.LastEventAt
	}
	if updatedAt.IsZero() {
		return ""
	}
	return updatedAt.UTC().Format("2006-01-02 15:04")
}

func approvalPanelModeLabel(panel appviewmodel.ApprovalPanelView) string {
	mode := firstNonEmpty(panel.CurrentModeName, panel.CurrentMode, "auto-review")
	if panel.CurrentModeName != "" && panel.CurrentMode != "" && !strings.EqualFold(panel.CurrentModeName, panel.CurrentMode) {
		return panel.CurrentMode + " (" + panel.CurrentModeName + ")"
	}
	return mode
}

func approvalPanelModeLine(mode appviewmodel.ApprovalModeChoice, width int, theme tuikit.Theme) string {
	tok := theme.Tokens()
	state := "available"
	style := tok.TextPrimary
	if mode.Current {
		state = "current"
		style = tok.Success
	}
	plain := fmt.Sprintf("  %s  %s", mode.ID, state)
	if name := strings.TrimSpace(mode.Name); name != "" && !strings.EqualFold(name, mode.ID) {
		plain += "  " + name
	}
	return style.Render(truncateTailDisplay(commandPanelOneLine(plain), width))
}

func approvalPanelPendingLine(item appviewmodel.ApprovalItem, width int, theme tuikit.Theme) string {
	tok := theme.Tokens()
	parts := []string{firstNonEmpty(item.ID, item.EventID), firstNonEmpty(item.Tool, "approval")}
	if item.Command != "" {
		parts = append(parts, item.Command)
	} else if item.Reason != "" {
		parts = append(parts, item.Reason)
	}
	if len(item.Actions) > 0 {
		parts = append(parts, fmt.Sprintf("%d actions", len(item.Actions)))
	}
	plain := "  " + strings.Join(compactNonEmpty(parts), "  ")
	return tok.Warning.Render(truncateTailDisplay(commandPanelOneLine(plain), width))
}

func approvalPanelActionLine(action appviewmodel.ApprovalPanelAction, width int, theme tuikit.Theme) string {
	tok := theme.Tokens()
	state := "disabled"
	style := tok.TextMuted
	if action.Enabled {
		state = "enabled"
		style = tok.TextPrimary
	}
	plain := fmt.Sprintf("  %s - %s (%s)", firstNonEmpty(action.ID, action.Label, action.Kind), firstNonEmpty(action.Label, action.Kind), state)
	return style.Render(truncateTailDisplay(commandPanelOneLine(plain), width))
}

func controllerPanelFieldLine(field appviewmodel.ControllerPanelField, width int, theme tuikit.Theme) string {
	value := firstNonEmpty(field.Value, "-")
	traits := []string{field.Kind}
	if field.Editable {
		traits = append(traits, "editable")
	}
	if len(field.Options) > 0 {
		traits = append(traits, fmt.Sprintf("%d options", len(field.Options)))
	}
	plain := fmt.Sprintf("  %s = %s", firstNonEmpty(field.Label, field.ID), value)
	if len(compactNonEmpty(traits)) > 0 {
		plain += "  [" + strings.Join(compactNonEmpty(traits), ", ") + "]"
	}
	return theme.Tokens().TextPrimary.Render(truncateTailDisplay(commandPanelOneLine(plain), width))
}

func controllerPanelActionLine(action appviewmodel.ControllerPanelAction, width int, theme tuikit.Theme) string {
	tok := theme.Tokens()
	state := "disabled"
	style := tok.TextMuted
	if action.Enabled {
		state = "enabled"
		style = tok.TextPrimary
	}
	parts := []string{firstNonEmpty(action.ID, action.Label, action.Kind), state}
	if action.RequiresInput {
		parts = append(parts, "input")
	}
	if action.Destructive {
		parts = append(parts, "destructive")
	}
	plain := "  " + strings.Join(compactNonEmpty(parts), "  ")
	return style.Render(truncateTailDisplay(commandPanelOneLine(plain), width))
}

func connectPanelProviderLine(provider appviewmodel.ModelConnectProvider, width int, theme tuikit.Theme) string {
	label := firstNonEmpty(provider.Label, provider.Provider, provider.ID)
	details := []string{}
	if provider.ConfiguredModelCount > 0 {
		details = append(details, fmt.Sprintf("%d configured", provider.ConfiguredModelCount))
	}
	if provider.CatalogModelCount > 0 {
		details = append(details, fmt.Sprintf("%d suggested", provider.CatalogModelCount))
	}
	if provider.TokenEnv != "" {
		details = append(details, "env:"+provider.TokenEnv)
	} else if provider.NoAuthRequired {
		details = append(details, "no auth")
	}
	plain := "  " + label
	if len(details) > 0 {
		plain += "  [" + strings.Join(details, ", ") + "]"
	}
	return theme.Tokens().TextPrimary.Render(truncateTailDisplay(commandPanelOneLine(plain), width))
}

func agentPanelItemLine(agent appviewmodel.AgentItem, width int, theme tuikit.Theme) string {
	plain := "  " + firstNonEmpty(agent.Name, agent.ID, agent.Command)
	details := compactNonEmpty([]string{agent.Kind, agent.Command})
	if len(details) > 0 {
		plain += "  [" + strings.Join(details, ", ") + "]"
	}
	return theme.Tokens().TextPrimary.Render(truncateTailDisplay(commandPanelOneLine(plain), width))
}

func settingsDiagnosticsAsRows(items []appviewmodel.SettingsPanelDiagnostic) []commandPanelDiagnosticRow {
	out := make([]commandPanelDiagnosticRow, 0, len(items))
	for _, item := range items {
		out = append(out, commandPanelDiagnosticRow{Severity: item.Severity, Source: item.Source, Kind: item.Kind, Message: item.Message})
	}
	return out
}

func taskDiagnosticsAsRows(items []appviewmodel.TaskPanelDiagnostic) []commandPanelDiagnosticRow {
	out := make([]commandPanelDiagnosticRow, 0, len(items))
	for _, item := range items {
		out = append(out, commandPanelDiagnosticRow{Severity: item.Severity, Kind: item.Kind, Message: item.Message})
	}
	return out
}

func controllerDiagnosticsAsRows(items []appviewmodel.ControllerPanelDiagnostic) []commandPanelDiagnosticRow {
	out := make([]commandPanelDiagnosticRow, 0, len(items))
	for _, item := range items {
		out = append(out, commandPanelDiagnosticRow{Severity: item.Severity, Kind: item.Kind, Message: item.Message})
	}
	return out
}

func connectDiagnosticsAsRows(items []appviewmodel.ModelConnectDiagnostic) []commandPanelDiagnosticRow {
	out := make([]commandPanelDiagnosticRow, 0, len(items))
	for _, item := range items {
		out = append(out, commandPanelDiagnosticRow{Severity: item.Severity, Source: item.Provider, Kind: item.Kind, Message: item.Message})
	}
	return out
}
