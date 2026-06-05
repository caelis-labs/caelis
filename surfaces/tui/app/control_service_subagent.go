package tuiapp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
)

type subagentControlService interface {
	AgentProfileStatus(context.Context) (control.AgentProfileStatusSnapshot, error)
	BindAgentProfile(context.Context, control.AgentProfileBindingConfig) (control.AgentProfileStatusSnapshot, error)
}

const subagentListTableBudget = 112

func slashSubagentWithContext(ctx context.Context, service control.Service, sender *ProgramSender, args string, argsStart int, fullText string, attachments []Attachment) TaskResultMsg {
	ctx = contextOrBackground(ctx)
	if sender != nil {
		ctx = sender.bindContext(ctx)
	}
	send := sender.sendFunc()
	sub, rest, restStart := splitFirstWithPromptSpan(strings.TrimSpace(args))
	switch strings.ToLower(strings.TrimSpace(sub)) {
	case "", "help":
		sendNotice(send, subagentUsageText())
		return TaskResultMsg{SuppressTurnDivider: true}
	case "list":
		profileService, ok := service.(subagentControlService)
		if !ok {
			return TaskResultMsg{Err: friendlyCommandError("subagent list", fmt.Errorf("subagent profiles are unavailable"))}
		}
		status, err := profileService.AgentProfileStatus(ctx)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("subagent list", err)}
		}
		sendNotice(send, formatSubagentList(status))
		return TaskResultMsg{SuppressTurnDivider: true}
	case "run":
		target, prompt, promptStart := splitFirstWithPromptSpan(strings.TrimSpace(rest))
		if strings.TrimSpace(target) == "" || strings.TrimSpace(prompt) == "" {
			sendNotice(send, "usage: /subagent run <id> <prompt>")
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		promptAttachments := attachmentsForPromptRange(
			attachments,
			argsStart+restStart+promptStart,
			len([]rune(strings.TrimSpace(fullText))),
		)
		return slashDynamicAgentWithContext(ctx, service, sender, target, prompt, promptAttachments)
	case "bind":
		profileService, ok := service.(subagentControlService)
		if !ok {
			return TaskResultMsg{Err: friendlyCommandError("subagent bind", fmt.Errorf("subagent profiles are unavailable"))}
		}
		cfg, ok := parseSubagentBindArgs(rest)
		if !ok {
			sendNotice(send, subagentBindUsageText())
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		status, err := profileService.BindAgentProfile(ctx, cfg)
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("subagent bind", err)}
		}
		sendNotice(send, formatSubagentBindNotice(cfg.ProfileID, status))
		refreshAgentSlashCommandsViaSendWithContext(ctx, service, send)
		return TaskResultMsg{SuppressTurnDivider: true}
	default:
		sendNotice(send, subagentUsageText())
		return TaskResultMsg{SuppressTurnDivider: true}
	}
}

func subagentUsageText() string {
	return strings.Join([]string{
		"usage:",
		"  /subagent list",
		"  /subagent run <id> <prompt>",
		"  " + subagentBindUsageLine(),
		"  /subagent bind <id> model <alias> [reasoning]",
		"  /subagent bind <id> acp <agent>",
	}, "\n")
}

func subagentBindUsageText() string {
	return strings.Join([]string{
		"usage:",
		"  " + subagentBindUsageLine(),
		"  /subagent bind <id> model <alias> [reasoning]",
		"  /subagent bind <id> acp <agent>",
	}, "\n")
}

func subagentBindUsageLine() string {
	return "/subagent bind <id> default"
}

func parseSubagentBindArgs(args string) (control.AgentProfileBindingConfig, bool) {
	fields := strings.Fields(strings.TrimSpace(args))
	if len(fields) < 2 {
		return control.AgentProfileBindingConfig{}, false
	}
	cfg := control.AgentProfileBindingConfig{ProfileID: fields[0]}
	switch strings.ToLower(strings.TrimSpace(fields[1])) {
	case "default", "self", "builtin", "built-in":
		if len(fields) != 2 {
			return control.AgentProfileBindingConfig{}, false
		}
		cfg.Target = "built_in"
		return cfg, true
	case "model":
		if len(fields) < 3 || len(fields) > 4 {
			return control.AgentProfileBindingConfig{}, false
		}
		cfg.Target = "built_in"
		cfg.Model = fields[2]
		if len(fields) == 4 {
			cfg.ReasoningEffort = fields[3]
		}
		return cfg, true
	case "acp":
		if len(fields) != 3 {
			return control.AgentProfileBindingConfig{}, false
		}
		cfg.Target = "acp"
		cfg.ACPAgent = fields[2]
		return cfg, true
	default:
		return control.AgentProfileBindingConfig{}, false
	}
}

func formatSubagentList(status control.AgentProfileStatusSnapshot) string {
	if len(status.Profiles) == 0 && len(status.Warnings) == 0 {
		return "no subagent profiles found"
	}
	lines := []string{"Subagents:"}
	rows := make([]subagentListRow, 0, len(status.Profiles))
	for _, profile := range status.Profiles {
		id := strings.TrimSpace(profile.ID)
		if id == "" {
			continue
		}
		rows = append(rows, subagentListRow{
			agent:       id,
			runtime:     subagentRuntimeLabel(profile),
			description: subagentListDescription(profile),
			warning:     strings.TrimSpace(profile.Warning),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].agent) < strings.ToLower(rows[j].agent)
	})
	for _, line := range formatSubagentTable(rows, subagentListTableBudget) {
		lines = append(lines, "  "+line)
	}
	for _, warning := range status.Warnings {
		if warning = strings.TrimSpace(warning); warning != "" {
			lines = append(lines, "  warning: "+warning)
		}
	}
	return strings.Join(lines, "\n")
}

type subagentListRow struct {
	agent       string
	runtime     string
	description string
	warning     string
}

func formatSubagentTable(rows []subagentListRow, budget int) []string {
	if len(rows) == 0 {
		return nil
	}
	agentWidth := displayColumns("Agent")
	runtimeWidth := displayColumns("Runtime")
	for _, row := range rows {
		agentWidth = maxInt(agentWidth, displayColumns(row.agent))
		runtimeWidth = maxInt(runtimeWidth, displayColumns(row.runtime))
	}
	agentWidth = minInt(maxInt(agentWidth, 5), 14)
	runtimeWidth = minInt(maxInt(runtimeWidth, 12), 38)
	contentBudget := maxInt(72, budget-2)
	descWidth := contentBudget - agentWidth - runtimeWidth - 4
	if descWidth < 28 {
		runtimeWidth = maxInt(12, minInt(runtimeWidth, contentBudget-agentWidth-4-28))
		descWidth = contentBudget - agentWidth - runtimeWidth - 4
	}
	descWidth = maxInt(24, descWidth)

	out := []string{
		padRightDisplay("Agent", agentWidth) + "  " + padRightDisplay("Runtime", runtimeWidth) + "  " + "Description",
		strings.Repeat("-", agentWidth) + "  " + strings.Repeat("-", runtimeWidth) + "  " + strings.Repeat("-", descWidth),
	}
	for _, row := range rows {
		desc := row.description
		if row.warning != "" {
			if desc != "" {
				desc += " "
			}
			desc += "warning: " + row.warning
		}
		out = append(out,
			padRightDisplay(truncateTailDisplay(row.agent, agentWidth), agentWidth)+"  "+
				padRightDisplay(truncateTailDisplay(row.runtime, runtimeWidth), runtimeWidth)+"  "+
				truncateTailDisplay(desc, descWidth),
		)
	}
	return out
}

func padRightDisplay(value string, width int) string {
	if width <= 0 {
		return value
	}
	count := displayColumns(value)
	if count >= width {
		return value
	}
	return value + strings.Repeat(" ", width-count)
}

func subagentListDescription(profile control.AgentProfileSnapshot) string {
	desc := strings.Join(strings.Fields(strings.TrimSpace(profile.Description)), " ")
	status := strings.TrimSpace(profile.Status)
	if status != "" && !strings.EqualFold(status, "ok") {
		if desc != "" {
			desc += " "
		}
		desc += "[" + status + "]"
	}
	return desc
}

func subagentRuntimeLabel(profile control.AgentProfileSnapshot) string {
	if !profile.Enabled {
		return "disabled"
	}
	switch strings.ToLower(strings.TrimSpace(profile.Target)) {
	case "acp":
		if agent := strings.TrimSpace(profile.ACPAgent); agent != "" {
			return "acp:" + agent
		}
		return "acp"
	case "built_in", "builtin", "self":
		model := compactSubagentModelLabel(profile.Model)
		if model == "" {
			model = "session default"
		}
		if reasoning := strings.TrimSpace(profile.ReasoningEffort); reasoning != "" {
			model += "[" + reasoning + "]"
		}
		return model
	default:
		return strings.TrimSpace(profile.Target)
	}
}

func compactSubagentModelLabel(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	if before, after, ok := strings.Cut(model, "/"); ok && strings.Contains(before, "@") && strings.TrimSpace(after) != "" {
		return strings.TrimSpace(after)
	}
	return model
}

func subagentBindingLabel(profile control.AgentProfileSnapshot) string {
	if !profile.Enabled {
		return "disabled"
	}
	switch strings.ToLower(strings.TrimSpace(profile.Target)) {
	case "acp":
		return "acp " + strings.TrimSpace(profile.ACPAgent)
	case "built_in", "builtin", "self":
		model := strings.TrimSpace(profile.Model)
		if model == "" {
			return "session default"
		}
		if reasoning := strings.TrimSpace(profile.ReasoningEffort); reasoning != "" {
			return "model " + model + " (" + reasoning + ")"
		}
		return "model " + model
	default:
		return strings.TrimSpace(profile.Target)
	}
}

func formatSubagentBindNotice(profileID string, status control.AgentProfileStatusSnapshot) string {
	profileID = strings.ToLower(strings.TrimSpace(profileID))
	for _, profile := range status.Profiles {
		if !strings.EqualFold(strings.TrimSpace(profile.ID), profileID) {
			continue
		}
		return fmt.Sprintf("subagent %s bound to %s", profile.ID, subagentBindingLabel(profile))
	}
	return fmt.Sprintf("subagent %s binding updated", profileID)
}
