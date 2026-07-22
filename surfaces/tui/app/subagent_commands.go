package tuiapp

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/agentbinding"
	"github.com/caelis-labs/caelis/internal/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
)

type subagentConfigurationService interface {
	agentbinding.Service
}

func subagentWizard() WizardDef {
	return WizardDef{
		Command:     "subagent",
		DisplayLine: "/subagent",
		Steps: []WizardStepDef{{
			Key:              "action",
			HintLabel:        "/subagent action",
			FreeformHint:     "/subagent action: list current bindings or bind one Agent",
			RequireCandidate: true,
			CompletionCommand: func(map[string]string) string {
				return "subagent-action"
			},
		}},
		Branch: func(_ string, value string, _ *SlashArgCandidate, _ map[string]string) *WizardDef {
			switch strings.ToLower(strings.TrimSpace(value)) {
			case "list":
				return &WizardDef{
					Command: "subagent", DisplayLine: "/subagent list",
					BuildExecLine: func(map[string]string) string { return "/subagent list" },
				}
			case "bind":
				next := subagentBindWizard()
				return &next
			default:
				return nil
			}
		},
	}
}

func subagentBindWizard() WizardDef {
	return WizardDef{
		Command: "subagent", DisplayLine: "/subagent bind",
		Steps: []WizardStepDef{
			{
				Key: "subject", HintLabel: "/subagent Agent",
				FreeformHint: "/subagent bind: choose Breeze, Orbit, Zenith, Guardian, or Reviewer", RequireCandidate: true,
				CompletionCommand: func(map[string]string) string { return "subagent-bindable" },
			},
			{
				Key: "target", HintLabel: "/subagent model or Agent",
				FreeformHint: "/subagent target: choose a connected model or Agent", RequireCandidate: true,
				CompletionCommand: func(state map[string]string) string {
					return "subagent-target:" + strings.TrimSpace(state["subject"])
				},
			},
			{
				Key: "effort", HintLabel: "/subagent reasoning effort",
				FreeformHint: "/subagent reasoning effort: use the Agent default or choose one supported level", RequireCandidate: true,
				CompletionCommand: func(state map[string]string) string {
					return "subagent-effort:" + strings.TrimSpace(state["subject"]) + ":" + strings.TrimSpace(state["target"])
				},
				ShouldSkip: func(state map[string]string) bool {
					if !isSystemAgentID(strings.TrimSpace(state["subject"])) {
						return false
					}
					target := strings.TrimSpace(state["target"])
					return strings.EqualFold(target, "default") || strings.EqualFold(target, "self")
				},
			},
		},
		BuildExecLine: func(state map[string]string) string {
			parts := []string{"/subagent", "bind", strings.TrimSpace(state["subject"]), strings.TrimSpace(state["target"])}
			if effort := strings.TrimSpace(state["effort"]); effort != "" && !strings.EqualFold(effort, "default") {
				parts = append(parts, effort)
			}
			return strings.Join(parts, " ")
		},
	}
}

func completeSubagentSlashArgs(
	ctx context.Context,
	service subagentConfigurationService,
	command string,
	query string,
	limit int,
) ([]SlashArgCandidate, bool, error) {
	command = strings.ToLower(strings.TrimSpace(command))
	switch {
	case command == "subagent-action":
		return filterSubagentSlashCandidates([]SlashArgCandidate{
			{Value: "list", Display: "List bindings", Detail: "Show delegation profiles and system Agent models"},
			{Value: "bind", Display: "Bind Agent", Detail: "Choose a profile or system Agent, then select its target"},
		}, query, limit), true, nil
	case command == "subagent-bindable":
		candidates := make([]SlashArgCandidate, 0, 5)
		for _, definition := range agentbinding.Definitions() {
			if !definition.Configurable {
				continue
			}
			candidates = append(candidates, SlashArgCandidate{
				Value: string(definition.Handle), Display: definition.Name, Detail: definition.Description,
			})
		}
		return filterSubagentSlashCandidates(candidates, query, limit), true, nil
	case strings.HasPrefix(command, "subagent-target:"):
		handle := agentbinding.NormalizeHandle(agentbinding.Handle(strings.TrimPrefix(command, "subagent-target:")))
		status, err := service.AgentBindingStatus(contextOrBackground(ctx))
		if err != nil {
			return nil, true, err
		}
		candidates := make([]SlashArgCandidate, 0, len(status.Targets)+1)
		if agentbinding.IsSystem(handle) {
			candidates = append(candidates, SlashArgCandidate{
				Value: "default", Display: "Main Agent default", Detail: "Follow the product's default model behavior",
			})
		} else {
			candidates = append(candidates, SlashArgCandidate{
				Value: "self", Display: "Unbound · self", Detail: "Remove the explicit binding and hide this profile from Spawn and slash commands",
			})
		}
		for _, profile := range status.Targets {
			if !agentbinding.SupportsProfile(handle, profile) {
				continue
			}
			efforts := make([]string, 0, len(profile.Effort.Choices))
			for _, choice := range profile.Effort.Choices {
				efforts = append(efforts, choice.Canonical)
			}
			detail := string(profile.Kind()) + " ModelProfile · efforts: " + strings.Join(efforts, ", ")
			candidates = append(candidates, SlashArgCandidate{
				Value: profile.ID, Display: profile.DisplayName, Detail: detail,
			})
		}
		return filterSubagentSlashCandidates(candidates, query, limit), true, nil
	case strings.HasPrefix(command, "subagent-effort:"):
		subjectAndTarget := strings.SplitN(strings.TrimPrefix(command, "subagent-effort:"), ":", 2)
		handle := agentbinding.Handle("")
		profileID := strings.TrimSpace(subjectAndTarget[0])
		if len(subjectAndTarget) == 2 {
			handle = agentbinding.NormalizeHandle(agentbinding.Handle(subjectAndTarget[0]))
			profileID = strings.TrimSpace(subjectAndTarget[1])
		}
		if strings.EqualFold(profileID, "self") || strings.EqualFold(profileID, "default") {
			return filterSubagentSlashCandidates([]SlashArgCandidate{{
				Value: "default", Display: "Unbound", Detail: "Remove the explicit profile binding",
			}}, query, limit), true, nil
		}
		status, err := service.AgentBindingStatus(contextOrBackground(ctx))
		if err != nil {
			return nil, true, err
		}
		for _, profile := range status.Targets {
			if !strings.EqualFold(strings.TrimSpace(profile.ID), profileID) || !agentbinding.SupportsProfile(handle, profile) {
				continue
			}
			candidates := make([]SlashArgCandidate, 0, len(profile.Effort.Choices))
			for _, choice := range profile.Effort.Choices {
				effort := strings.TrimSpace(choice.Canonical)
				detail := "Bind this delegation profile explicitly"
				if agentbinding.IsSystem(handle) {
					detail = "Bind this system Agent explicitly"
				}
				candidates = append(candidates, SlashArgCandidate{
					Value: effort, Display: "Reasoning effort: " + effort, Detail: detail,
				})
			}
			return filterSubagentSlashCandidates(candidates, query, limit), true, nil
		}
		return nil, true, nil
	default:
		return nil, false, nil
	}
}

func filterSubagentSlashCandidates(candidates []SlashArgCandidate, query string, limit int) []SlashArgCandidate {
	filtered := filterSlashArgCandidates(query, candidates)
	if limit > 0 && len(filtered) > limit {
		filtered = filtered[:limit]
	}
	return filtered
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
			send(SlashCommandResultMsg{Result: control.NewTableSlashResult("subagent", subagentStatusTable(status))})
		}
		return TaskResultMsg{SuppressTurnDivider: true}
	case "bind":
		subject, targetAndEffort, _ := controlprompt.ParseFirst(rest)
		target, effort, _ := controlprompt.ParseFirst(targetAndEffort)
		if strings.TrimSpace(subject) == "" || strings.TrimSpace(target) == "" {
			sendNotice(send, subagentUsageText())
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
		sendNotice(send, formatAgentBindingNotice(status, handle))
		if controlService, ok := any(service).(control.Service); ok && !system {
			refreshAgentSlashCommandsViaSendWithContext(ctx, controlService, send)
		}
		return TaskResultMsg{SuppressTurnDivider: true}
	default:
		sendNotice(send, subagentUsageText())
		return TaskResultMsg{SuppressTurnDivider: true}
	}
}

func subagentUsageText() string {
	return "usage: /subagent list | /subagent bind <breeze|orbit|zenith> <self|profile-id> <effort> | /subagent bind <guardian|reviewer> <default|provider-profile-id> <effort>\nrun /subagent to choose list or bind"
}

func subagentStatusTable(status agentbinding.Status) control.SlashTableSnapshot {
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
	return control.SlashTableSnapshot{
		Title: "Subagents",
		Sections: []control.SlashTableSection{
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
	prefix := "subagent updated "
	if agentbinding.IsSystem(handle) {
		prefix = "system Agent updated "
	}
	return prefix + formatAgentBinding(status, handle)
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
