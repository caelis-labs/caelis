package tuiapp

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

type subagentConfigurationService interface {
	agentbinding.Service
}

func slashSubagentWithContext(ctx context.Context, service subagentConfigurationService, send func(tea.Msg), args string) TaskResultMsg {
	action, rest, _ := controlprompt.ParseFirst(strings.TrimSpace(args))
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list":
		status, err := service.AgentBindingStatus(contextOrBackground(ctx))
		if err != nil {
			return TaskResultMsg{Err: controlprompt.FriendlyCommandError("list subagent bindings", err)}
		}
		if send != nil {
			send(SlashCommandResultMsg{Result: controlprompt.NewTableSlashResult("subagent", subagentStatusTable(status))})
		}
		return TaskResultMsg{SuppressTurnDivider: true}
	case "bind":
		subject, targetAndEffort, _ := controlprompt.ParseFirst(rest)
		target, effort, _ := controlprompt.ParseFirst(targetAndEffort)
		if strings.TrimSpace(subject) == "" || strings.TrimSpace(target) == "" {
			sendNotice(send, subagentUsageText(), SlashNoticeHint)
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		handle := agentbinding.NormalizeHandle(agentbinding.Handle(subject))
		system := agentbinding.IsSystem(handle)
		var (
			status agentbinding.Status
			err    error
		)
		resetTarget := strings.EqualFold(strings.TrimSpace(target), "self") ||
			strings.EqualFold(strings.TrimSpace(target), "default")
		if resetTarget {
			if strings.TrimSpace(effort) != "" {
				label := "self"
				if system {
					label = "default"
				}
				return TaskResultMsg{Err: controlprompt.FriendlyCommandError("reset subagent binding", fmt.Errorf("%s does not accept a reasoning effort override", label))}
			}
			status, err = service.ResetAgentBinding(contextOrBackground(ctx), handle)
		} else {
			if strings.TrimSpace(effort) == "" {
				return TaskResultMsg{Err: controlprompt.FriendlyCommandError("bind subagent handle", fmt.Errorf("an explicit effort is required"))}
			}
			status, err = service.BindAgentBinding(contextOrBackground(ctx), agentbinding.Binding{
				Handle: handle, ProfileID: target, Effort: effort,
			})
		}
		if err != nil {
			return TaskResultMsg{Err: controlprompt.FriendlyCommandError("update subagent binding", err)}
		}
		sendNotice(send, formatAgentBindingNotice(status, handle), SlashNoticeFeedback)
		if controlService, ok := any(service).(controlprompt.Service); ok && !system {
			refreshAgentSlashCommandsViaSendWithContext(ctx, controlService, send)
		}
		return TaskResultMsg{SuppressTurnDivider: true}
	default:
		sendNotice(send, subagentUsageText(), SlashNoticeHint)
		return TaskResultMsg{SuppressTurnDivider: true}
	}
}

func subagentUsageText() string {
	return "usage: /subagent list | /subagent bind <handle> <self|default|profile-id> [effort]\nrun /subagent to open the Agent configuration overlay"
}

func subagentStatusTable(status agentbinding.Status) controlprompt.SlashTableSnapshot {
	delegationRows := make([][]string, 0, 4)
	systemRows := make([][]string, 0, 2)
	for _, handle := range status.Handles {
		row := agentBindingStatusRow(handle)
		if handle.Definition.Class == agentbinding.HandleClassSystem {
			systemRows = append(systemRows, row)
		} else {
			delegationRows = append(delegationRows, row)
		}
	}
	return controlprompt.SlashTableSnapshot{
		Title: "Subagents",
		Sections: []controlprompt.SlashTableSection{
			{Title: "Delegation Profiles", Columns: []string{"Profile", "Name", "Binding"}, Rows: delegationRows},
			{Title: "System Agents", Columns: []string{"Agent", "Name", "Binding"}, Rows: systemRows},
		},
	}
}

func formatAgentBinding(status agentbinding.Status, handle agentbinding.Handle) string {
	handle = agentbinding.NormalizeHandle(handle)
	for _, item := range status.Handles {
		if item.Definition.Handle == handle || item.Binding.Handle == handle {
			return strings.Join(agentBindingStatusRow(item), "  ")
		}
	}
	return string(handle)
}

func formatAgentBindingNotice(status agentbinding.Status, handle agentbinding.Handle) string {
	label := "Updated "
	if agentbinding.IsSystem(handle) {
		label = "Updated system agent "
	}
	return label + formatAgentBinding(status, handle)
}

func agentBindingStatusRow(status agentbinding.HandleStatus) []string {
	handle := status.Definition.Handle
	if handle == "" {
		handle = status.Binding.Handle
	}
	name := firstNonEmpty(strings.TrimSpace(status.Definition.Name), string(handle))
	target := "Unbound"
	switch {
	case handle == agentbinding.HandleSelf:
		target = "Current Session controller and effort"
	case agentbinding.IsSystem(handle):
		target = "Main Agent default"
	}
	if strings.TrimSpace(status.Binding.ProfileID) != "" {
		target = firstNonEmpty(strings.TrimSpace(status.Profile.DisplayName), strings.TrimSpace(status.Binding.ProfileID))
		target += " [" + strings.TrimSpace(status.Binding.Effort) + "]"
	}
	return []string{string(handle), name, target}
}

func subagentProfileCommandDetail(status agentbinding.HandleStatus) string {
	description := strings.TrimSpace(status.Definition.Description)
	if strings.TrimSpace(status.Binding.ProfileID) == "" {
		return strings.Join(compactNonEmpty([]string{description, "unbound · configure with /subagent bind"}), " · ")
	}
	target := firstNonEmpty(strings.TrimSpace(status.Profile.DisplayName), strings.TrimSpace(status.Binding.ProfileID))
	if effort := strings.TrimSpace(status.Binding.Effort); effort != "" {
		target += " [" + effort + "]"
	}
	return strings.Join(compactNonEmpty([]string{description, target}), " · ")
}

func isSystemAgentID(value string) bool {
	return agentbinding.IsSystem(agentbinding.Handle(value))
}
