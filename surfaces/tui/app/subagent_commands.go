package tuiapp

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	controldelegation "github.com/caelis-labs/caelis/control/delegation"
	controlsystemagent "github.com/caelis-labs/caelis/control/systemagent"
	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
	"github.com/caelis-labs/caelis/protocol/acp/control"
)

type subagentConfigurationService interface {
	controldelegation.Service
	controlsystemagent.Service
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
		for _, definition := range controldelegation.Definitions() {
			if !definition.Configurable {
				continue
			}
			candidates = append(candidates, SlashArgCandidate{
				Value: string(definition.Profile), Display: definition.Name, Detail: definition.Description,
			})
		}
		for _, definition := range controlsystemagent.Definitions() {
			candidates = append(candidates, SlashArgCandidate{
				Value: string(definition.ID), Display: definition.Name, Detail: definition.Description,
			})
		}
		return filterSubagentSlashCandidates(candidates, query, limit), true, nil
	case strings.HasPrefix(command, "subagent-target:"):
		subject := strings.TrimSpace(strings.TrimPrefix(command, "subagent-target:"))
		if isSystemAgentID(subject) {
			status, err := service.SystemAgentStatus(contextOrBackground(ctx))
			if err != nil {
				return nil, true, err
			}
			candidates := []SlashArgCandidate{{
				Value: "default", Display: "Main Agent default", Detail: "Follow the product's default model behavior",
			}}
			for _, target := range status.Targets {
				alias := firstNonEmpty(strings.TrimSpace(target.Model.Alias), strings.TrimSpace(target.Model.ID))
				detail := strings.Join(compactNonEmpty([]string{target.Model.Provider, target.Model.Model}), " · ")
				candidates = append(candidates, SlashArgCandidate{
					Value: target.Agent.ID, Display: alias, Detail: detail,
				})
			}
			return filterSubagentSlashCandidates(candidates, query, limit), true, nil
		}
		status, err := service.DelegationStatus(contextOrBackground(ctx))
		if err != nil {
			return nil, true, err
		}
		candidates := []SlashArgCandidate{{
			Value: "self", Display: "Unbound · self", Detail: "Remove the explicit binding and hide this profile from Spawn and slash commands",
		}}
		for _, target := range status.Targets {
			agentID := strings.TrimSpace(target.Agent.ID)
			if agentID == "" || strings.EqualFold(agentID, "self") {
				continue
			}
			detail := "External ACP Agent · uses the Agent's model and session defaults"
			if alias := strings.TrimSpace(target.Agent.Backing.ModelAlias); alias != "" {
				detail = "Model-backed Agent · " + alias + " · Agent default effort"
				if len(target.ReasoningLevels) > 0 {
					detail = "Model-backed Agent · " + alias + " · efforts: " + strings.Join(target.ReasoningLevels, ", ")
				}
			} else if modelID := strings.TrimSpace(target.Agent.Defaults.ModelID); modelID != "" {
				detail = "External ACP Agent · " + modelID + " · uses Agent defaults"
			}
			candidates = append(candidates, SlashArgCandidate{
				Value: agentID, Display: "/" + agentID, Detail: detail,
			})
		}
		return filterSubagentSlashCandidates(candidates, query, limit), true, nil
	case strings.HasPrefix(command, "subagent-effort:"):
		subjectAndTarget := strings.SplitN(strings.TrimPrefix(command, "subagent-effort:"), ":", 2)
		subject := ""
		agentID := strings.TrimSpace(subjectAndTarget[0])
		if len(subjectAndTarget) == 2 {
			subject = strings.TrimSpace(subjectAndTarget[0])
			agentID = strings.TrimSpace(subjectAndTarget[1])
		}
		if strings.EqualFold(agentID, "self") {
			return filterSubagentSlashCandidates([]SlashArgCandidate{{
				Value: "default", Display: "Unbound", Detail: "Remove the explicit profile binding",
			}}, query, limit), true, nil
		}
		if isSystemAgentID(subject) {
			status, err := service.SystemAgentStatus(contextOrBackground(ctx))
			if err != nil {
				return nil, true, err
			}
			for _, target := range status.Targets {
				if !strings.EqualFold(strings.TrimSpace(target.Agent.ID), agentID) {
					continue
				}
				candidates := []SlashArgCandidate{{
					Value: "default", Display: "Agent default", Detail: "Use the model-backed Agent's configured effort",
				}}
				for _, raw := range target.Model.ReasoningLevels {
					effort := strings.TrimSpace(raw)
					if effort == "" || strings.EqualFold(effort, "default") {
						continue
					}
					candidates = append(candidates, SlashArgCandidate{
						Value: effort, Display: "Reasoning effort: " + effort, Detail: "Override this system Agent only",
					})
				}
				return filterSubagentSlashCandidates(candidates, query, limit), true, nil
			}
			return nil, true, nil
		}
		status, err := service.DelegationStatus(contextOrBackground(ctx))
		if err != nil {
			return nil, true, err
		}
		for _, target := range status.Targets {
			if !strings.EqualFold(strings.TrimSpace(target.Agent.ID), agentID) {
				continue
			}
			candidates := []SlashArgCandidate{{
				Value: "default", Display: "Agent default", Detail: "Use the Agent's configured session defaults",
			}}
			if strings.TrimSpace(target.Agent.Backing.ModelAlias) == "" {
				return filterSubagentSlashCandidates(candidates, query, limit), true, nil
			}
			for _, raw := range target.ReasoningLevels {
				effort := strings.TrimSpace(raw)
				if effort == "" || strings.EqualFold(effort, "default") {
					continue
				}
				candidates = append(candidates, SlashArgCandidate{
					Value: effort, Display: "Reasoning effort: " + effort, Detail: "Override this delegation profile only",
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
		delegationStatus, err := service.DelegationStatus(contextOrBackground(ctx))
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("list subagent profiles", err)}
		}
		systemStatus, err := service.SystemAgentStatus(contextOrBackground(ctx))
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("list system Agents", err)}
		}
		if send != nil {
			send(SlashCommandResultMsg{Result: control.NewTableSlashResult(
				"subagent",
				subagentStatusTable(delegationStatus, systemStatus),
			)})
		}
		return TaskResultMsg{SuppressTurnDivider: true}
	case "bind":
		subject, targetAndEffort, _ := controlprompt.ParseFirst(rest)
		target, effort, _ := controlprompt.ParseFirst(targetAndEffort)
		if strings.TrimSpace(subject) == "" || strings.TrimSpace(target) == "" {
			sendNotice(send, subagentUsageText())
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		if isSystemAgentID(subject) {
			id := controlsystemagent.NormalizeID(controlsystemagent.ID(subject))
			var (
				status controlsystemagent.Status
				err    error
			)
			if strings.EqualFold(strings.TrimSpace(target), "default") || strings.EqualFold(strings.TrimSpace(target), "self") {
				if strings.TrimSpace(effort) != "" {
					return TaskResultMsg{Err: friendlyCommandError("bind system Agent", fmt.Errorf("default does not accept a reasoning effort override"))}
				}
				status, err = service.ResetSystemAgent(contextOrBackground(ctx), id)
			} else {
				status, err = service.BindSystemAgent(contextOrBackground(ctx), controlsystemagent.BindRequest{
					ID: id, AgentID: target, ReasoningEffort: effort,
				})
			}
			if err != nil {
				return TaskResultMsg{Err: friendlyCommandError("bind system Agent", err)}
			}
			sendNotice(send, formatSystemAgentBindingNotice(status, id))
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		profile := controldelegation.Profile(subject)
		var (
			status controldelegation.Status
			err    error
		)
		if strings.EqualFold(strings.TrimSpace(target), "self") {
			if strings.TrimSpace(effort) != "" {
				return TaskResultMsg{Err: friendlyCommandError("bind subagent profile", fmt.Errorf("self does not accept a reasoning effort override"))}
			}
			status, err = service.ResetDelegation(contextOrBackground(ctx), profile)
		} else {
			status, err = service.BindDelegation(contextOrBackground(ctx), controldelegation.BindRequest{
				Profile: profile, AgentID: target, ReasoningEffort: effort,
			})
		}
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("bind subagent profile", err)}
		}
		sendNotice(send, formatSubagentBindingNotice(status, profile))
		if controlService, ok := any(service).(control.Service); ok {
			refreshAgentSlashCommandsViaSendWithContext(ctx, controlService, send)
		}
		return TaskResultMsg{SuppressTurnDivider: true}
	default:
		sendNotice(send, subagentUsageText())
		return TaskResultMsg{SuppressTurnDivider: true}
	}
}

func subagentUsageText() string {
	return "usage: /subagent list | /subagent bind <breeze|orbit|zenith> <self|agent> [effort] | /subagent bind <guardian|reviewer> <default|model-agent> [effort]\nrun /subagent to choose list or bind"
}

func subagentStatusTable(status controldelegation.Status, systemStatus controlsystemagent.Status) control.SlashTableSnapshot {
	profiles := make([][]string, 0, len(status.Profiles))
	for _, profile := range status.Profiles {
		profiles = append(profiles, subagentProfileStatusRow(profile))
	}
	systemAgents := make([][]string, 0, len(systemStatus.Agents))
	for _, systemAgent := range systemStatus.Agents {
		systemAgents = append(systemAgents, systemAgentStatusRow(systemAgent))
	}
	return control.SlashTableSnapshot{
		Title: "Subagents",
		Sections: []control.SlashTableSection{
			{
				Title:   "Delegation Profiles",
				Columns: []string{"Profile", "Name", "Binding"},
				Rows:    profiles,
			},
			{
				Title:   "System Agents",
				Columns: []string{"Agent", "Name", "Binding"},
				Rows:    systemAgents,
			},
		},
	}
}

func formatSubagentProfileBinding(status controldelegation.Status, profile controldelegation.Profile) string {
	profile = controldelegation.NormalizeProfile(profile)
	for _, item := range status.Profiles {
		if item.Definition.Profile == profile || item.Binding.Profile == profile {
			return formatSubagentProfileStatus(item)
		}
	}
	return string(profile)
}

func formatSubagentBindingNotice(status controldelegation.Status, profile controldelegation.Profile) string {
	return "subagent updated " + formatSubagentProfileBinding(status, profile)
}

func formatSubagentProfileStatus(profile controldelegation.ProfileStatus) string {
	row := subagentProfileStatusRow(profile)
	return strings.Join(row, "  ")
}

func subagentProfileStatusRow(profile controldelegation.ProfileStatus) []string {
	profileID := profile.Definition.Profile
	if profileID == "" {
		profileID = profile.Binding.Profile
	}
	name := strings.TrimSpace(profile.Definition.Name)
	if name == "" {
		name = string(profileID)
	}
	target := "Unbound"
	if profileID == controldelegation.ProfileSelf {
		target = "Current Session controller and effort"
	}
	if profile.Binding.Target == controldelegation.TargetAgent {
		agentID := strings.TrimSpace(profile.Agent.ID)
		if agentID == "" {
			agentID = strings.TrimSpace(profile.Binding.AgentID)
		}
		target = "/" + agentID
		if alias := strings.TrimSpace(profile.Agent.Backing.ModelAlias); alias != "" {
			target += " · " + alias
			if effort := strings.TrimSpace(profile.Binding.ReasoningEffort); effort != "" {
				target += " [" + effort + "]"
			} else {
				target += " [Agent default]"
			}
		} else {
			if modelID := strings.TrimSpace(profile.Agent.Defaults.ModelID); modelID != "" {
				target += " · " + modelID
			}
			target += " · ACP Agent defaults"
		}
	}
	return []string{string(profileID), name, target}
}

func subagentProfileCommandDetail(profile controldelegation.ProfileStatus) string {
	description := strings.TrimSpace(profile.Definition.Description)
	if profile.Binding.Target != controldelegation.TargetAgent {
		return strings.Join(compactNonEmpty([]string{description, "unbound · configure with /subagent bind"}), " · ")
	}
	target := strings.TrimSpace(profile.Agent.Backing.ModelAlias)
	if target == "" {
		target = strings.Join(compactNonEmpty([]string{profile.Agent.ID, profile.Agent.Defaults.ModelID}), "/")
	}
	if effort := strings.TrimSpace(profile.Binding.ReasoningEffort); effort != "" {
		target += " [" + effort + "]"
	}
	return strings.Join(compactNonEmpty([]string{description, target}), " · ")
}

func formatSystemAgentStatus(status controlsystemagent.AgentStatus) string {
	return strings.Join(systemAgentStatusRow(status), "  ")
}

func systemAgentStatusRow(status controlsystemagent.AgentStatus) []string {
	id := status.Definition.ID
	name := firstNonEmpty(strings.TrimSpace(status.Definition.Name), string(id))
	target := "Main Agent default"
	if status.Binding.AgentID != "" {
		target = firstNonEmpty(strings.TrimSpace(status.Agent.Backing.ModelAlias), strings.TrimSpace(status.Agent.ID))
		if effort := strings.TrimSpace(status.Binding.ReasoningEffort); effort != "" {
			target += " [" + effort + "]"
		} else {
			target += " [Agent default]"
		}
	}
	return []string{string(id), name, target}
}

func formatSystemAgentBindingNotice(status controlsystemagent.Status, id controlsystemagent.ID) string {
	id = controlsystemagent.NormalizeID(id)
	for _, item := range status.Agents {
		if item.Definition.ID == id || item.Binding.ID == id {
			return "system Agent updated " + formatSystemAgentStatus(item)
		}
	}
	return "system Agent updated " + string(id)
}

func isSystemAgentID(value string) bool {
	id := controlsystemagent.NormalizeID(controlsystemagent.ID(value))
	for _, definition := range controlsystemagent.Definitions() {
		if definition.ID == id {
			return true
		}
	}
	return false
}
