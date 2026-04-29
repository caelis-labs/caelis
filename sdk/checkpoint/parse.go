package checkpoint

import (
	"regexp"
	"strings"
)

var numberedItemPattern = regexp.MustCompile(`^\d+\.\s+`)

type sectionKind int

const (
	sectionNone sectionKind = iota
	sectionObjective
	sectionConstraints
	sectionDecisions
	sectionFacts
	sectionProgress
	sectionRisks
	sectionNextActions
	sectionActiveTasks
	sectionActiveParticipants
	sectionLatestBlockers
	sectionOperationalNotes
)

func ParseCompactMessage(text string) State {
	text = strings.TrimSpace(text)
	if text == "" {
		return State{}
	}
	state := State{}
	current := sectionNone
	for _, rawLine := range strings.Split(text, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		switch {
		case strings.EqualFold(line, "CONTEXT CHECKPOINT"),
			strings.EqualFold(line, "Checkpoint Summary"),
			strings.EqualFold(line, "Use this as compressed history for continuation."):
			continue
		}
		if value, ok := labeledValue(line, "Objective:"); ok {
			state.Objective = firstNonEmptyString(state.Objective, value)
			current = sectionNone
			continue
		}
		if value, ok := labeledValue(line, "Blocker:"); ok {
			state.LatestBlockers = append(state.LatestBlockers, value)
			current = sectionNone
			continue
		}
		if value, ok := labeledValue(line, "Next action:"); ok {
			state.NextActions = append(state.NextActions, value)
			current = sectionNone
			continue
		}
		if kind, ok := parseCompactHeading(line); ok {
			current = kind
			continue
		}
		item := compactListItem(line)
		if item == "" {
			continue
		}
		switch current {
		case sectionObjective:
			state.Objective = firstNonEmptyString(state.Objective, item)
		case sectionConstraints:
			state.UserConstraints = append(state.UserConstraints, item)
		case sectionDecisions:
			state.DurableDecisions = append(state.DurableDecisions, item)
		case sectionFacts:
			state.VerifiedFacts = append(state.VerifiedFacts, item)
		case sectionProgress:
			state.CurrentProgress = append(state.CurrentProgress, item)
		case sectionRisks:
			state.OpenQuestionsAndRisks = append(state.OpenQuestionsAndRisks, item)
		case sectionNextActions:
			state.NextActions = append(state.NextActions, item)
		case sectionActiveTasks:
			state.ActiveTasks = append(state.ActiveTasks, TaskState{TaskID: taskIDForSummary(item), Summary: item})
		case sectionActiveParticipants:
			state.ActiveParticipants = append(state.ActiveParticipants, ParticipantState{Agent: item, Summary: item})
		case sectionLatestBlockers:
			state.LatestBlockers = append(state.LatestBlockers, item)
		case sectionOperationalNotes:
			appendOperationalNote(&state, item)
		}
	}
	return NormalizeState(state)
}

func parseCompactHeading(line string) (sectionKind, bool) {
	line = strings.TrimSpace(strings.TrimPrefix(line, "## "))
	line = strings.TrimSpace(strings.TrimSuffix(line, ":"))
	switch strings.ToLower(line) {
	case "objective", "active objective":
		return sectionObjective, true
	case "user constraints", "durable constraints":
		return sectionConstraints, true
	case "durable decisions":
		return sectionDecisions, true
	case "verified facts", "verified facts and references":
		return sectionFacts, true
	case "current progress":
		return sectionProgress, true
	case "open questions / risks", "open questions and risks":
		return sectionRisks, true
	case "next actions", "immediate next actions":
		return sectionNextActions, true
	case "active tasks":
		return sectionActiveTasks, true
	case "active participants":
		return sectionActiveParticipants, true
	case "latest blockers":
		return sectionLatestBlockers, true
	case "operational notes", "files touched", "commands run":
		return sectionOperationalNotes, true
	default:
		return sectionNone, false
	}
}

func labeledValue(line string, label string) (string, bool) {
	if !strings.HasPrefix(strings.ToLower(line), strings.ToLower(label)) {
		return "", false
	}
	value := strings.TrimSpace(line[len(label):])
	value = strings.TrimLeft(value, "- ")
	if value == "" {
		return "", false
	}
	return value, true
}

func compactListItem(line string) string {
	line = strings.TrimSpace(line)
	line = strings.TrimPrefix(line, "- ")
	line = numberedItemPattern.ReplaceAllString(line, "")
	return strings.TrimSpace(line)
}

func appendOperationalNote(state *State, item string) {
	switch {
	case strings.HasPrefix(strings.ToLower(item), "files touched:"):
		values := strings.Split(strings.TrimSpace(item[len("files touched:"):]), ",")
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" {
				state.OperationalAnnex.FilesTouched = append(state.OperationalAnnex.FilesTouched, value)
			}
		}
	case strings.HasPrefix(strings.ToLower(item), "commands run:"):
		values := strings.Split(strings.TrimSpace(item[len("commands run:"):]), " | ")
		for _, value := range values {
			value = strings.TrimSpace(value)
			if value != "" {
				state.OperationalAnnex.CommandsRun = append(state.OperationalAnnex.CommandsRun, value)
			}
		}
	default:
		state.CurrentProgress = append(state.CurrentProgress, item)
	}
}

func taskIDForSummary(summary string) string {
	summary = strings.TrimSpace(summary)
	if summary == "" {
		return ""
	}
	parts := strings.Fields(summary)
	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
