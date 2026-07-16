package tuiapp

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	controldelegation "github.com/caelis-labs/caelis/control/delegation"
	controlprompt "github.com/caelis-labs/caelis/ports/controlprompt"
)

func subagentWizard() WizardDef {
	return WizardDef{
		Command:     "subagent",
		DisplayLine: "/subagent",
		Steps: []WizardStepDef{
			{
				Key:              "profile",
				HintLabel:        "/subagent profile",
				FreeformHint:     "/subagent profile: choose Caelis Breeze, Orbit, or Zenith",
				RequireCandidate: true,
				CompletionCommand: func(map[string]string) string {
					return "subagent-profile"
				},
			},
			{
				Key:              "target",
				HintLabel:        "/subagent target",
				FreeformHint:     "/subagent target: choose Session self or one configured Agent",
				RequireCandidate: true,
				CompletionCommand: func(state map[string]string) string {
					return "subagent-target:" + strings.TrimSpace(state["profile"])
				},
			},
			{
				Key:              "effort",
				HintLabel:        "/subagent reasoning effort",
				FreeformHint:     "/subagent reasoning effort: use the Agent default or choose one supported level",
				RequireCandidate: true,
				CompletionCommand: func(state map[string]string) string {
					return "subagent-effort:" + strings.TrimSpace(state["target"])
				},
			},
		},
		BuildExecLine: func(state map[string]string) string {
			parts := []string{"/subagent", "bind", strings.TrimSpace(state["profile"]), strings.TrimSpace(state["target"])}
			if effort := strings.TrimSpace(state["effort"]); effort != "" && !strings.EqualFold(effort, "default") {
				parts = append(parts, effort)
			}
			return strings.Join(parts, " ")
		},
	}
}

func completeSubagentSlashArgs(
	ctx context.Context,
	service controldelegation.Service,
	command string,
	query string,
	limit int,
) ([]SlashArgCandidate, bool, error) {
	command = strings.ToLower(strings.TrimSpace(command))
	switch {
	case command == "subagent-profile":
		candidates := make([]SlashArgCandidate, 0, 3)
		for _, definition := range controldelegation.Definitions() {
			if !definition.Configurable {
				continue
			}
			candidates = append(candidates, SlashArgCandidate{
				Value: string(definition.Profile), Display: definition.Name, Detail: definition.Description,
			})
		}
		return filterSubagentSlashCandidates(candidates, query, limit), true, nil
	case strings.HasPrefix(command, "subagent-target:"):
		status, err := service.DelegationStatus(contextOrBackground(ctx))
		if err != nil {
			return nil, true, err
		}
		candidates := []SlashArgCandidate{{
			Value: "self", Display: "Session Default · self", Detail: "Inherit the current Session controller model and reasoning effort",
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
		agentID := strings.TrimSpace(strings.TrimPrefix(command, "subagent-effort:"))
		if strings.EqualFold(agentID, "self") {
			return filterSubagentSlashCandidates([]SlashArgCandidate{{
				Value: "default", Display: "Session default", Detail: "Inherit the current Session reasoning effort",
			}}, query, limit), true, nil
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

func slashSubagentWithContext(ctx context.Context, service controldelegation.Service, send func(tea.Msg), args string) TaskResultMsg {
	action, rest, _ := controlprompt.ParseFirst(strings.TrimSpace(args))
	switch strings.ToLower(strings.TrimSpace(action)) {
	case "", "list":
		status, err := service.DelegationStatus(contextOrBackground(ctx))
		if err != nil {
			return TaskResultMsg{Err: friendlyCommandError("list subagent profiles", err)}
		}
		sendNotice(send, formatSubagentStatus(status))
		return TaskResultMsg{SuppressTurnDivider: true}
	case "bind":
		profileText, targetAndEffort, _ := controlprompt.ParseFirst(rest)
		target, effort, _ := controlprompt.ParseFirst(targetAndEffort)
		if strings.TrimSpace(profileText) == "" || strings.TrimSpace(target) == "" {
			sendNotice(send, subagentUsageText())
			return TaskResultMsg{SuppressTurnDivider: true}
		}
		profile := controldelegation.Profile(profileText)
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
		return TaskResultMsg{SuppressTurnDivider: true}
	default:
		sendNotice(send, subagentUsageText())
		return TaskResultMsg{SuppressTurnDivider: true}
	}
}

func subagentUsageText() string {
	return "usage: /subagent list | /subagent bind <breeze|orbit|zenith> <self|agent-id> [effort]\nrun /subagent to open the guided binding wizard"
}

func formatSubagentStatus(status controldelegation.Status) string {
	lines := []string{"Delegation profiles"}
	for _, profile := range status.Profiles {
		lines = append(lines, "  "+formatSubagentProfileStatus(profile))
	}
	return strings.Join(lines, "\n")
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
	profileID := profile.Definition.Profile
	if profileID == "" {
		profileID = profile.Binding.Profile
	}
	name := strings.TrimSpace(profile.Definition.Name)
	if name == "" {
		name = string(profileID)
	}
	target := "self · current Session controller and effort"
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
	return fmt.Sprintf("%-7s  %s -> %s", profileID, name, target)
}
