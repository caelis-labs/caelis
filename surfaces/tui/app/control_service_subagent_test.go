package tuiapp

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

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

func TestSlashReviewStartsReviewerProfile(t *testing.T) {
	driver := &bridgeTestDriver{}
	result := dispatchSlashCommand(driver, &ProgramSender{}, "/review")
	if result.Err != nil {
		t.Fatalf("/review error = %v", result.Err)
	}
	if driver.lastReviewInstructions != "" {
		t.Fatalf("review instructions = %q, want default empty instructions", driver.lastReviewInstructions)
	}
	if driver.lastStartedAgent != "" {
		t.Fatalf("dynamic agent runner called with %q, want profile runner only", driver.lastStartedAgent)
	}
}

func TestSlashReviewPassesCustomInstructions(t *testing.T) {
	driver := &bridgeTestDriver{}
	result := dispatchSlashCommand(driver, &ProgramSender{}, "/review focus on replay persistence")
	if result.Err != nil {
		t.Fatalf("/review custom error = %v", result.Err)
	}
	if driver.lastReviewInstructions != "focus on replay persistence" {
		t.Fatalf("review instructions = %q, want custom instructions", driver.lastReviewInstructions)
	}
}

func TestSubagentRunShowsRemovedNotice(t *testing.T) {
	driver := &bridgeTestDriver{
		agentList: []control.AgentCandidate{{Name: "reviewer"}},
	}
	var notices []string
	result := dispatchSlashCommand(driver, &ProgramSender{Send: func(msg tea.Msg) {
		if chunk, ok := msg.(LogChunkMsg); ok {
			notices = append(notices, chunk.Chunk)
		}
	}}, "/subagent run reviewer inspect this")
	if result.Err != nil {
		t.Fatalf("/subagent run error = %v", result.Err)
	}
	if driver.lastStartedAgent != "" || driver.lastReviewInstructions != "" {
		t.Fatalf("runner was called: dynamic=%q review=%q", driver.lastStartedAgent, driver.lastReviewInstructions)
	}
	combined := strings.Join(notices, "\n")
	for _, want := range []string{"/subagent run has been removed", "/review [instructions]", "/<agent> <prompt>"} {
		if !strings.Contains(combined, want) {
			t.Fatalf("/subagent run notice = %q, want %q", combined, want)
		}
	}
}
