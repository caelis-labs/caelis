package tuiapp

import (
	"strings"
	"testing"

	"github.com/OnslaughtSnail/caelis/protocol/acp/control"
)

func TestFormatSubagentListShowsReadableBindings(t *testing.T) {
	got := formatSubagentList(control.AgentProfileStatusSnapshot{
		Profiles: []control.AgentProfileSnapshot{
			{
				ID:          "guardian",
				Description: "Reviews approval requests.",
				Enabled:     true,
				Target:      "built_in",
			},
			{
				ID:              "reviewer",
				Description:     "Reviews code changes.",
				Enabled:         true,
				Target:          "built_in",
				Model:           "deepseek@default/deepseek/deepseek-v4-pro",
				ReasoningEffort: "high",
			},
			{
				ID:              "explorer",
				Description:     "Reads and maps repository context before implementation decisions.",
				Enabled:         true,
				Target:          "built_in",
				Model:           "deepseek@default/deepseek/deepseek-v4-flash",
				ReasoningEffort: "high",
			},
		},
	})
	for _, want := range []string{
		"Agent",
		"Runtime",
		"Description",
		"explorer  deepseek/deepseek-v4-flash[high]",
		"guardian  session default",
		"reviewer  deepseek/deepseek-v4-pro[high]",
		"Reviews approval requests.",
		"Reviews code changes.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatSubagentList() missing %q:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if width := displayColumns(line); width > subagentListTableBudget {
			t.Fatalf("formatSubagentList() line width = %d, want <= %d:\n%s", width, subagentListTableBudget, got)
		}
	}
	for _, bad := range []string{"->", "guardian  disabled", "model deepseek"} {
		if strings.Contains(got, bad) {
			t.Fatalf("formatSubagentList() contains %q:\n%s", bad, got)
		}
	}
}
