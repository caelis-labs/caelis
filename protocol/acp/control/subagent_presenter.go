package control

import (
	"fmt"
	"sort"
	"strings"
)

// AgentProfileDisplay is the canonical display model for /subagent list.
type AgentProfileDisplay struct {
	Rows     []AgentProfileDisplayRow `json:"rows,omitempty"`
	Warnings []string                 `json:"warnings,omitempty"`
}

// AgentProfileDisplayRow describes one subagent profile row without choosing a
// table, card, or list treatment.
type AgentProfileDisplayRow struct {
	ID          string `json:"id,omitempty"`
	Binding     string `json:"binding,omitempty"`
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
	Warning     string `json:"warning,omitempty"`
}

// AgentProfileDisplayFromSnapshot derives display rows from subagent profile
// status. Sorting, filtering, and binding labels are canonical here so surfaces
// do not duplicate presenter logic.
func AgentProfileDisplayFromSnapshot(status AgentProfileStatusSnapshot) AgentProfileDisplay {
	profiles := make([]AgentProfileSnapshot, 0, len(status.Profiles))
	for _, profile := range status.Profiles {
		if strings.TrimSpace(profile.ID) != "" {
			profiles = append(profiles, profile)
		}
	}
	sort.SliceStable(profiles, func(i, j int) bool {
		return strings.ToLower(profiles[i].ID) < strings.ToLower(profiles[j].ID)
	})
	display := AgentProfileDisplay{
		Rows:     make([]AgentProfileDisplayRow, 0, len(profiles)),
		Warnings: cleanSubagentWarnings(status.Warnings),
	}
	for _, profile := range profiles {
		display.Rows = append(display.Rows, AgentProfileDisplayRow{
			ID:          strings.TrimSpace(profile.ID),
			Binding:     subagentRuntimeLabel(profile),
			Status:      subagentRuntimeStatus(profile),
			Description: strings.Join(strings.Fields(strings.TrimSpace(profile.Description)), " "),
			Warning:     strings.TrimSpace(profile.Warning),
		})
	}
	return display
}

// FormatSubagentList renders subagent profile status for slash-command output.
func FormatSubagentList(status AgentProfileStatusSnapshot) string {
	display := AgentProfileDisplayFromSnapshot(status)
	if len(display.Rows) == 0 && len(display.Warnings) == 0 {
		return "no subagent profiles found"
	}
	lines := []string{"Subagents:"}
	for _, row := range display.Rows {
		line := "  " + row.ID + "  " + row.Binding
		if row.Description != "" {
			line += "  " + row.Description
		}
		if row.Warning != "" {
			line += "  warning: " + row.Warning
		}
		lines = append(lines, line)
	}
	for _, warning := range display.Warnings {
		lines = append(lines, "  warning: "+warning)
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
			model += " [" + reasoning + "]"
		}
		return model
	default:
		return strings.TrimSpace(profile.Target)
	}
}

func subagentRuntimeStatus(profile AgentProfileSnapshot) string {
	if !profile.Enabled {
		return "disabled"
	}
	if status := strings.TrimSpace(profile.Status); status != "" {
		return status
	}
	if strings.TrimSpace(profile.Warning) != "" {
		return "warning"
	}
	return "ready"
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

func cleanSubagentWarnings(warnings []string) []string {
	if len(warnings) == 0 {
		return nil
	}
	out := make([]string, 0, len(warnings))
	for _, warning := range warnings {
		if warning = strings.TrimSpace(warning); warning != "" {
			out = append(out, warning)
		}
	}
	return out
}
