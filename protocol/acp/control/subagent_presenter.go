package control

import (
	"fmt"
	"sort"
	"strings"
)

// FormatSubagentList renders subagent profile status for slash-command output.
func FormatSubagentList(status AgentProfileStatusSnapshot) string {
	if len(status.Profiles) == 0 && len(status.Warnings) == 0 {
		return "no subagent profiles found"
	}
	lines := []string{"Subagents:"}
	rows := make([]AgentProfileSnapshot, 0, len(status.Profiles))
	for _, profile := range status.Profiles {
		if strings.TrimSpace(profile.ID) != "" {
			rows = append(rows, profile)
		}
	}
	sort.SliceStable(rows, func(i, j int) bool {
		return strings.ToLower(rows[i].ID) < strings.ToLower(rows[j].ID)
	})
	for _, profile := range rows {
		line := "  " + strings.TrimSpace(profile.ID) + "  " + subagentRuntimeLabel(profile)
		if desc := strings.Join(strings.Fields(strings.TrimSpace(profile.Description)), " "); desc != "" {
			line += "  " + desc
		}
		if warning := strings.TrimSpace(profile.Warning); warning != "" {
			line += "  warning: " + warning
		}
		lines = append(lines, line)
	}
	for _, warning := range status.Warnings {
		if warning = strings.TrimSpace(warning); warning != "" {
			lines = append(lines, "  warning: "+warning)
		}
	}
	return strings.Join(lines, "\n")
}

// FormatSubagentBindNotice renders a concise notice after updating a subagent
// profile binding.
func FormatSubagentBindNotice(profileID string, status AgentProfileStatusSnapshot) string {
	profileID = strings.ToLower(strings.TrimSpace(profileID))
	for _, profile := range status.Profiles {
		if !strings.EqualFold(strings.TrimSpace(profile.ID), profileID) {
			continue
		}
		return fmt.Sprintf("subagent %s bound to %s", profile.ID, subagentBindingLabel(profile))
	}
	return fmt.Sprintf("subagent %s binding updated", profileID)
}

func subagentRuntimeLabel(profile AgentProfileSnapshot) string {
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
		model := strings.TrimSpace(profile.Model)
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

func subagentBindingLabel(profile AgentProfileSnapshot) string {
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
