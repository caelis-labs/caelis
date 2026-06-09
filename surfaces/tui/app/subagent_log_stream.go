package tuiapp

import (
	"strings"

	"github.com/OnslaughtSnail/caelis/internal/displaypolicy"
	"github.com/OnslaughtSnail/caelis/surfaces/plainactivity"
)

func renderSubagentPanelLogLines(panel *SubagentPanelBlock, ctx BlockRenderContext, width int, limit int) []string {
	if panel == nil {
		return nil
	}
	events := subagentPanelLogEvents(panel, ctx.Workspace)
	lines := plainactivity.Render(events, plainactivity.Options{Width: width, MaxLines: limit})
	if len(lines) == 0 && !isTerminalSubagentState(panel.Status) {
		return []string{"waiting for subagent output"}
	}
	return lines
}

func subagentPanelLogEvents(panel *SubagentPanelBlock, workspace string) []plainactivity.Event {
	if panel == nil {
		return nil
	}
	events := panel.Events
	if strings.EqualFold(strings.TrimSpace(panel.Status), "completed") {
		if ev, ok := latestSubagentNarrativeEvent(panel.Events, SEAssistant); ok {
			events = []SubagentEvent{ev}
		} else if ev, ok := latestSubagentNarrativeEvent(panel.Events, SEReasoning); ok {
			events = []SubagentEvent{ev}
		}
	}
	groups := subagentLogEventGroups(events)
	out := make([]plainactivity.Event, 0, len(groups))
	for _, group := range groups {
		if ev, ok := subagentLogEventForGroup(group, workspace); ok {
			out = append(out, ev)
		}
	}
	if strings.EqualFold(strings.TrimSpace(panel.Status), "waiting_approval") && !subagentLogHasApprovalEvent(events) {
		out = append(out, plainactivity.Event{Kind: plainactivity.ToolCall, Text: "Waiting approval"})
	}
	return out
}

func subagentLogEventGroups(events []SubagentEvent) [][]SubagentEvent {
	groups := make([][]SubagentEvent, 0, len(events))
	for i := 0; i < len(events); i++ {
		ev := events[i]
		if ev.Kind == SEAssistant || ev.Kind == SEReasoning {
			end := i
			for end+1 < len(events) && events[end+1].Kind == ev.Kind {
				end++
			}
			groups = append(groups, events[i:end+1])
			i = end
			continue
		}
		if ev.Kind == SEToolCall {
			callID := strings.TrimSpace(ev.CallID)
			if callID != "" {
				end := i
				for end+1 < len(events) && events[end+1].Kind == SEToolCall && strings.TrimSpace(events[end+1].CallID) == callID {
					end++
				}
				groups = append(groups, events[i:end+1])
				i = end
				continue
			}
		}
		groups = append(groups, []SubagentEvent{ev})
	}
	return groups
}

func subagentLogHasApprovalEvent(events []SubagentEvent) bool {
	for _, ev := range events {
		if ev.Kind == SEApproval {
			return true
		}
	}
	return false
}

func subagentLogEventForGroup(group []SubagentEvent, workspace string) (plainactivity.Event, bool) {
	if len(group) == 0 {
		return plainactivity.Event{}, false
	}
	switch group[0].Kind {
	case SEAssistant:
		if text := subagentLogNarrativeText(group, true); text != "" {
			return plainactivity.Event{Kind: plainactivity.Assistant, Text: text}, true
		}
	case SEReasoning:
		if text := subagentLogNarrativeText(group, false); text != "" {
			return plainactivity.Event{Kind: plainactivity.Reasoning, Text: text}, true
		}
	case SEToolCall:
		if text := subagentLogToolText(group, workspace); text != "" {
			return plainactivity.Event{Kind: plainactivity.ToolCall, Text: text}, true
		}
	case SEApproval:
		if text := subagentLogApprovalText(group[0]); text != "" {
			return plainactivity.Event{Kind: plainactivity.ToolCall, Text: text}, true
		}
	}
	return plainactivity.Event{}, false
}

func subagentLogNarrativeText(group []SubagentEvent, assistant bool) string {
	parts := make([]string, 0, len(group))
	for _, ev := range group {
		if assistant && ev.Kind != SEAssistant {
			continue
		}
		if !assistant && ev.Kind != SEReasoning {
			continue
		}
		if text := strings.TrimSpace(sanitizeRenderableText(ev.Text)); text != "" {
			parts = append(parts, text)
		}
	}
	text := strings.Join(parts, "\n")
	if assistant {
		if cleaned := strings.TrimSpace(displaypolicy.CleanSubagentFinalOutput(text)); cleaned != "" {
			text = cleaned
		}
	}
	return text
}

func subagentLogToolText(group []SubagentEvent, workspace string) string {
	start, final, hasFinal := subagentLogToolLifecycle(group)
	if hasFinal && final.Err {
		return subagentLogToolErrorText(final, workspace)
	}
	ev := start
	if hasFinal {
		ev = toolLifecycleHeaderEvent(start, final, true)
	}
	if ev.Err {
		return subagentLogToolErrorText(ev, workspace)
	}
	semantic := toolSemanticName(ev.Name, ev.ToolKind)
	verb := subagentLogToolVerb(semantic)
	detail := subagentLogToolDetail(ev, workspace)
	return strings.TrimSpace(strings.TrimSpace(verb) + " " + strings.TrimSpace(detail))
}

func subagentLogApprovalText(ev SubagentEvent) string {
	tool := firstNonEmpty(ev.ApprovalTool, "approval")
	detail := strings.TrimSpace(ev.ApprovalCommand)
	return strings.TrimSpace("Waiting approval: " + strings.TrimSpace(tool+" "+detail))
}

func subagentLogToolErrorText(ev SubagentEvent, workspace string) string {
	text := strings.TrimSpace(sanitizeRenderableText(ev.Output))
	if text == "" {
		if verb := explorationToolVerb(toolSemanticName(ev.Name, ev.ToolKind)); verb != "" {
			if detail := subagentExplorationPreviewDetail(ev, workspace); detail != "" {
				text = strings.TrimSpace(verb + " " + detail + " failed")
			}
		}
	}
	if text == "" {
		text = strings.TrimSpace(firstNonEmpty(ev.Args, ev.TaskInput, toolEventDisplayName(toolSemanticName(ev.Name, ev.ToolKind))+" failed"))
	}
	if text == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(text), "error") {
		return text
	}
	return "Error: " + text
}

func subagentLogToolLifecycle(group []SubagentEvent) (start SubagentEvent, final SubagentEvent, hasFinal bool) {
	for _, ev := range group {
		if ev.Kind != SEToolCall {
			continue
		}
		if start.Kind != SEToolCall || (!ev.Done && start.Done) {
			start = ev
		}
		if ev.Done && shouldRenderToolEvent(ev) {
			final = ev
			hasFinal = true
		}
	}
	if start.Kind != SEToolCall && hasFinal {
		start = final
	}
	return start, final, hasFinal
}

func subagentLogToolVerb(name string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "RUN_COMMAND":
		return "Run"
	case "TASK":
		return "Task"
	case "READ":
		return "Read"
	case "LIST":
		return "List"
	case "GLOB":
		return "Glob"
	case "SEARCH", "RG", "FIND":
		return "Search"
	case "WRITE":
		return "Write"
	case "PATCH":
		return "Patch"
	default:
		return toolEventDisplayName(name)
	}
}

func subagentLogToolDetail(ev SubagentEvent, workspace string) string {
	if verb := explorationToolVerb(toolSemanticName(ev.Name, ev.ToolKind)); verb != "" {
		if detail := subagentExplorationPreviewDetail(ev, workspace); detail != "" {
			return detail
		}
	}
	if strings.EqualFold(toolSemanticName(ev.Name, ev.ToolKind), "TASK") {
		if action := taskEventAction(ev); action != "" {
			if target := taskHandleDisplay(firstNonEmpty(ev.TaskID, ev.TaskTargetKind)); target != "" {
				return strings.TrimSpace(action + " " + target)
			}
			return action
		}
	}
	return trimSubagentLogToolStatusSuffix(strings.TrimSpace(firstNonEmpty(ev.Args, ev.TaskInput, ev.TaskID)))
}

func trimSubagentLogToolStatusSuffix(detail string) string {
	detail = strings.TrimSpace(detail)
	for _, suffix := range []string{" completed", " done"} {
		if strings.HasSuffix(strings.ToLower(detail), suffix) {
			return strings.TrimSpace(detail[:len(detail)-len(suffix)])
		}
	}
	return detail
}
