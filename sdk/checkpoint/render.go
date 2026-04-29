package checkpoint

import (
	"strings"
)

// RenderBlock renders one compact model-visible checkpoint block.
func RenderBlock(state State) string {
	state = NormalizeState(state)
	if !HasContent(state) {
		return ""
	}
	lines := []string{
		"Checkpoint Summary",
	}
	if state.Objective != "" {
		lines = append(lines, "", "Objective:", "- "+state.Objective)
	}
	lines = appendSection(lines, "User Constraints", state.UserConstraints)
	lines = appendSection(lines, "Durable Decisions", state.DurableDecisions)
	lines = appendSection(lines, "Verified Facts", state.VerifiedFacts)
	lines = appendSection(lines, "Current Progress", state.CurrentProgress)
	lines = appendSection(lines, "Open Questions And Risks", state.OpenQuestionsAndRisks)
	lines = appendSection(lines, "Next Actions", state.NextActions)
	if len(state.ActiveTasks) > 0 {
		values := make([]string, 0, len(state.ActiveTasks))
		for _, item := range state.ActiveTasks {
			if summary := strings.TrimSpace(item.Summary); summary != "" {
				values = append(values, summary)
			}
		}
		lines = appendSection(lines, "Active Tasks", values)
	}
	if len(state.ActiveParticipants) > 0 {
		values := make([]string, 0, len(state.ActiveParticipants))
		for _, item := range state.ActiveParticipants {
			if summary := strings.TrimSpace(item.Summary); summary != "" {
				values = append(values, summary)
			}
		}
		lines = appendSection(lines, "Active Participants", values)
	}
	if len(state.LatestBlockers) > 0 {
		lines = appendSection(lines, "Latest Blockers", state.LatestBlockers)
	}
	if len(state.OperationalAnnex.FilesTouched) > 0 {
		lines = appendSection(lines, "Files Touched", state.OperationalAnnex.FilesTouched)
	}
	if len(state.OperationalAnnex.CommandsRun) > 0 {
		lines = appendSection(lines, "Commands Run", state.OperationalAnnex.CommandsRun)
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// RenderCompactMessage renders the durable checkpoint as a codex-style compact
// replacement message that is later injected into model-visible history as one
// synthetic user message.
func RenderCompactMessage(state State) string {
	state = NormalizeState(state)
	block := strings.TrimSpace(RenderBlock(state))
	if block == "" {
		return "CONTEXT CHECKPOINT\n(no summary available)"
	}
	lines := []string{
		"CONTEXT CHECKPOINT",
		"Use this as compressed history for continuation.",
	}
	if state.Objective != "" {
		lines = append(lines, "", "Objective: "+state.Objective)
	}
	if len(state.LatestBlockers) > 0 && strings.TrimSpace(state.LatestBlockers[0]) != "" {
		lines = append(lines, "Blocker: "+strings.TrimSpace(state.LatestBlockers[0]))
	}
	if len(state.NextActions) > 0 && strings.TrimSpace(state.NextActions[0]) != "" {
		lines = append(lines, "Next action: "+strings.TrimSpace(state.NextActions[0]))
	}
	lines = append(lines, "", block)
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// RenderRuntimeBlock renders one compact live-state block for prompt injection.
func RenderRuntimeBlock(snapshot RuntimeSnapshot) string {
	lines := []string{"Current Runtime State"}
	lines = appendSection(lines, "Plan", snapshot.PlanSummary)
	if len(snapshot.ActiveTasks) > 0 {
		values := make([]string, 0, len(snapshot.ActiveTasks))
		for _, item := range snapshot.ActiveTasks {
			if item.Summary == "" {
				continue
			}
			values = append(values, item.Summary)
		}
		lines = appendSection(lines, "Active Tasks", values)
	}
	if len(snapshot.ActiveParticipants) > 0 {
		values := make([]string, 0, len(snapshot.ActiveParticipants))
		for _, item := range snapshot.ActiveParticipants {
			if item.Summary == "" {
				continue
			}
			values = append(values, item.Summary)
		}
		lines = appendSection(lines, "Active Participants", values)
	}
	lines = appendSection(lines, "Latest Blockers", snapshot.LatestBlockers)
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func appendSection(lines []string, title string, items []string) []string {
	items = normalizeStringList(items, 16)
	if len(items) == 0 {
		return lines
	}
	lines = append(lines, "", title+":")
	for _, item := range items {
		lines = append(lines, "- "+item)
	}
	return lines
}
