package checkpoint

import (
	"fmt"
	"strings"
	"time"

	sdksession "github.com/OnslaughtSnail/caelis/sdk/session"
)

// RuntimeSnapshot provides current non-transcript runtime state for compaction.
type RuntimeSnapshot struct {
	PlanSummary        []string
	ActiveTasks        []TaskState
	ActiveParticipants []ParticipantState
	LatestBlockers     []string
}

// HeuristicBuild merges a new heuristic checkpoint candidate from prior state,
// compacted prefix events, and current runtime snapshot.
func HeuristicBuild(
	base State,
	session sdksession.Session,
	events []*sdksession.Event,
	runtime RuntimeSnapshot,
	trigger string,
) State {
	now := time.Now()
	candidate := State{
		Revision:              base.Revision + 1,
		SummarizedThroughID:   lastEventID(events),
		UpdatedAt:             now,
		Trigger:               strings.TrimSpace(trigger),
		Generator:             "heuristic",
		Objective:             heuristicObjective(base, events),
		UserConstraints:       heuristicConstraints(events),
		DurableDecisions:      heuristicDecisions(events),
		VerifiedFacts:         heuristicFacts(events),
		CurrentProgress:       heuristicProgress(events, runtime),
		OpenQuestionsAndRisks: heuristicRisks(events),
		NextActions:           heuristicNextActions(runtime, events),
		ActiveTasks:           runtime.ActiveTasks,
		ActiveParticipants:    runtime.ActiveParticipants,
		LatestBlockers:        runtime.LatestBlockers,
		OperationalAnnex: OperationalAnnex{
			FilesTouched: heuristicFilesTouched(events),
			CommandsRun:  heuristicCommandsRun(events),
		},
	}
	if candidate.Objective == "" {
		candidate.Objective = strings.TrimSpace(session.Title)
	}
	return Merge(base, candidate)
}

func heuristicObjective(base State, events []*sdksession.Event) string {
	if strings.TrimSpace(base.Objective) != "" {
		return strings.TrimSpace(base.Objective)
	}
	for _, event := range events {
		if event == nil || event.Type != sdksession.EventTypeUser {
			continue
		}
		if text := strings.TrimSpace(event.Text); text != "" {
			return compactSentence(text, 200)
		}
	}
	return ""
}

func heuristicConstraints(events []*sdksession.Event) []string {
	out := []string{}
	for _, event := range events {
		if event == nil || event.Type != sdksession.EventTypeUser {
			continue
		}
		text := strings.ToLower(strings.TrimSpace(event.Text))
		switch {
		case strings.Contains(text, "exactly"):
			out = append(out, compactSentence(event.Text, 180))
		case strings.Contains(text, "must "):
			out = append(out, compactSentence(event.Text, 180))
		case strings.Contains(text, "do not "):
			out = append(out, compactSentence(event.Text, 180))
		}
	}
	return normalizeStringList(out, 8)
}

func heuristicDecisions(events []*sdksession.Event) []string {
	out := []string{}
	for _, event := range events {
		if event == nil {
			continue
		}
		if event.Type == sdksession.EventTypePlan && event.Text != "" {
			out = append(out, "plan updated: "+compactSentence(event.Text, 160))
		}
		if event.Type == sdksession.EventTypeToolResult && event.Meta != nil {
			if name, _ := event.Meta["tool_name"].(string); strings.TrimSpace(name) == "PLAN" {
				out = append(out, "used PLAN tool to refresh execution plan")
			}
		}
	}
	return normalizeStringList(out, 8)
}

func heuristicFacts(events []*sdksession.Event) []string {
	out := []string{}
	for _, event := range events {
		if event == nil {
			continue
		}
		switch event.Type {
		case sdksession.EventTypeUser, sdksession.EventTypeAssistant:
			if text := strings.TrimSpace(event.Text); text != "" {
				out = append(out, compactSentence(text, 180))
			}
		case sdksession.EventTypeToolResult:
			if event.Meta != nil {
				if value, _ := event.Meta["result"].(string); strings.TrimSpace(value) != "" {
					out = append(out, compactSentence(value, 180))
				}
			}
		}
	}
	return normalizeStringList(out, 12)
}

func heuristicProgress(events []*sdksession.Event, runtime RuntimeSnapshot) []string {
	out := append([]string{}, runtime.PlanSummary...)
	for _, event := range events {
		if event == nil {
			continue
		}
		switch event.Type {
		case sdksession.EventTypeAssistant, sdksession.EventTypeToolResult:
			if text := strings.TrimSpace(event.Text); text != "" {
				out = append(out, compactSentence(text, 160))
			}
		}
	}
	return normalizeStringList(out, 8)
}

func heuristicRisks(events []*sdksession.Event) []string {
	out := []string{}
	for _, event := range events {
		if event == nil {
			continue
		}
		text := strings.TrimSpace(event.Text)
		lower := strings.ToLower(text)
		switch {
		case strings.Contains(lower, "blocked"):
			out = append(out, compactSentence(text, 180))
		case strings.Contains(lower, "failed"):
			out = append(out, compactSentence(text, 180))
		case strings.Contains(lower, "error"):
			out = append(out, compactSentence(text, 180))
		}
	}
	return normalizeStringList(out, 6)
}

func heuristicNextActions(runtime RuntimeSnapshot, events []*sdksession.Event) []string {
	if len(runtime.PlanSummary) > 0 {
		return normalizeStringList(runtime.PlanSummary, 6)
	}
	out := []string{}
	for i := len(events) - 1; i >= 0 && len(out) < 4; i-- {
		event := events[i]
		if event == nil || event.Type != sdksession.EventTypeUser {
			continue
		}
		if text := strings.TrimSpace(event.Text); text != "" {
			out = append(out, "continue from: "+compactSentence(text, 160))
		}
	}
	return normalizeStringList(out, 6)
}

func heuristicFilesTouched(events []*sdksession.Event) []string {
	out := []string{}
	for _, event := range events {
		if event == nil || event.Protocol == nil || event.Protocol.ToolCall == nil {
			continue
		}
		for _, key := range []string{"path", "workdir"} {
			if value, ok := event.Protocol.ToolCall.RawInput[key].(string); ok && strings.TrimSpace(value) != "" {
				out = append(out, strings.TrimSpace(value))
			}
		}
	}
	return normalizeStringList(out, 12)
}

func heuristicCommandsRun(events []*sdksession.Event) []string {
	out := []string{}
	for _, event := range events {
		if event == nil || event.Protocol == nil || event.Protocol.ToolCall == nil {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(event.Protocol.ToolCall.Name), "BASH") {
			continue
		}
		if value, ok := event.Protocol.ToolCall.RawInput["command"].(string); ok && strings.TrimSpace(value) != "" {
			out = append(out, compactSentence(value, 180))
		}
	}
	return normalizeStringList(out, 12)
}

func lastEventID(events []*sdksession.Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		if id := strings.TrimSpace(events[i].ID); id != "" {
			return id
		}
	}
	return ""
}

func compactSentence(text string, limit int) string {
	text = strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
	if limit <= 0 || len([]rune(text)) <= limit {
		return text
	}
	runes := []rune(text)
	head := limit / 2
	tail := limit - head - 3
	if tail < 8 {
		tail = 8
	}
	if head < 8 {
		head = 8
	}
	if head+tail+3 > len(runes) {
		return string(runes)
	}
	return fmt.Sprintf("%s...%s", string(runes[:head]), string(runes[len(runes)-tail:]))
}
