package tuiapp

import (
	"strconv"
	"strings"

	"github.com/OnslaughtSnail/caelis/internal/displaypolicy"
)

type subagentPreviewLine struct {
	Plain  string
	Styled string
}

type subagentPreviewToolGroup struct {
	Verb    string
	Details []string
	Hidden  int
}

func renderSubagentTailPreviewLines(panel *SubagentPanelBlock, ctx BlockRenderContext, width int, limit int) []string {
	if panel == nil || limit <= 0 {
		return nil
	}
	events := panel.Events
	status := strings.ToLower(strings.TrimSpace(panel.Status))
	final := isTerminalSubagentState(status)
	completed := strings.EqualFold(status, "completed")
	if completed {
		if lines := subagentFinalNarrativePreviewLines(events, width, limit, ctx); len(lines) > 0 {
			return subagentPreviewStyled(lines)
		}
	}
	if approval := subagentApprovalPreviewLine(events, status, width, ctx); approval.Styled != "" {
		return []string{approval.Styled}
	}
	if !final {
		if lines := subagentRecentActivityPreviewLines(events, ctx.Workspace, width, limit, ctx); len(lines) > 0 {
			return subagentPreviewStyled(lines)
		}
	}
	if lines := subagentErrorPreviewLines(events, width, limit, ctx); len(lines) > 0 {
		return subagentPreviewStyled(lines)
	}
	if final {
		if lines := subagentFinalNarrativePreviewLines(events, width, limit, ctx); len(lines) > 0 {
			return subagentPreviewStyled(lines)
		}
	}
	if lines := subagentToolPreviewLines(events, ctx.Workspace, width, limit, ctx); len(lines) > 0 {
		return subagentPreviewStyled(lines)
	}
	return nil
}

func subagentFinalNarrativePreviewLines(events []SubagentEvent, width int, limit int, ctx BlockRenderContext) []subagentPreviewLine {
	if ev, ok := latestSubagentNarrativeEvent(events, SEAssistant); ok {
		return subagentNarrativePreviewLines(ev.Text, SEAssistant, true, width, limit, ctx)
	}
	if ev, ok := latestSubagentNarrativeEvent(events, SEReasoning); ok {
		return subagentNarrativePreviewLines(ev.Text, SEReasoning, true, width, limit, ctx)
	}
	return nil
}

func subagentTailPreviewLineLimit(panel *SubagentPanelBlock) int {
	if panel == nil {
		return acpTerminalPanelMaxLines
	}
	limit := panel.previewLines()
	if limit <= 0 {
		return acpTerminalPanelMaxLines
	}
	return minInt(limit, acpTerminalPanelMaxLines)
}

func subagentPreviewStyled(lines []subagentPreviewLine) []string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line.Plain) == "" && strings.TrimSpace(line.Styled) == "" {
			continue
		}
		out = append(out, firstNonEmpty(line.Styled, line.Plain))
	}
	return out
}

func subagentRecentActivityPreviewLines(events []SubagentEvent, workspace string, width int, limit int, ctx BlockRenderContext) []subagentPreviewLine {
	if limit <= 0 {
		return nil
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		switch ev.Kind {
		case SEAssistant, SEReasoning:
			if lines := subagentRecentNarrativePreviewLines(events[:i+1], width, limit, ctx); len(lines) > 0 {
				return lines
			}
		case SEToolCall:
			if ev.Err {
				if lines := subagentErrorPreviewLinesForEvent(ev, width, limit, ctx); len(lines) > 0 {
					return lines
				}
			}
			if lines := subagentToolPreviewLinesForEvent(events[:i+1], workspace, width, limit, ctx); len(lines) > 0 {
				return lines
			}
		case SEApproval:
			if approval := subagentApprovalPreviewLine(events[:i+1], "", width, ctx); approval.Styled != "" {
				return []subagentPreviewLine{approval}
			}
		}
	}
	return nil
}

func subagentRecentNarrativePreviewLines(events []SubagentEvent, width int, limit int, ctx BlockRenderContext) []subagentPreviewLine {
	if limit <= 0 {
		return nil
	}
	reversed := make([]subagentPreviewLine, 0, limit)
	for i := len(events) - 1; i >= 0 && len(reversed) < limit; i-- {
		ev := events[i]
		if ev.Kind != SEAssistant && ev.Kind != SEReasoning {
			continue
		}
		lines := subagentNarrativePreviewLines(ev.Text, ev.Kind, false, width, limit-len(reversed), ctx)
		for j := len(lines) - 1; j >= 0 && len(reversed) < limit; j-- {
			reversed = append(reversed, lines[j])
		}
	}
	if len(reversed) == 0 {
		return nil
	}
	out := make([]subagentPreviewLine, len(reversed))
	for i := range reversed {
		out[len(reversed)-1-i] = reversed[i]
	}
	return out
}

func subagentNarrativePreviewLines(text string, kind SubagentEventKind, final bool, width int, limit int, ctx BlockRenderContext) []subagentPreviewLine {
	if limit <= 0 {
		return nil
	}
	if final {
		if cleaned := strings.TrimSpace(displaypolicy.CleanSubagentFinalOutput(text)); cleaned != "" {
			text = cleaned
		}
	}
	segments := tailWrappedTerminalSegmentsFromEnd(text, maxInt(1, width), limit)
	if len(segments) == 0 {
		return nil
	}
	out := make([]subagentPreviewLine, 0, len(segments))
	for _, segment := range segments {
		plain := strings.TrimSpace(segment)
		if plain == "" {
			continue
		}
		style := ctx.Theme.ReasoningStyle()
		if kind == SEAssistant {
			style = ctx.Theme.AssistantStyle()
		}
		out = append(out, subagentPreviewLine{
			Plain:  plain,
			Styled: style.Render(plain),
		})
	}
	return out
}

func subagentApprovalPreviewLine(events []SubagentEvent, status string, width int, ctx BlockRenderContext) subagentPreviewLine {
	waiting := strings.EqualFold(strings.TrimSpace(status), "waiting_approval")
	if !waiting && !subagentLatestEventKind(events, SEApproval) {
		return subagentPreviewLine{}
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind != SEApproval {
			continue
		}
		tool := firstNonEmpty(ev.ApprovalTool, "approval")
		detail := strings.TrimSpace(ev.ApprovalCommand)
		plain := "Waiting approval: " + tool
		if detail != "" {
			plain += " " + detail
		}
		plain = truncateTailDisplay(plain, maxInt(1, width))
		return subagentPreviewLine{Plain: plain, Styled: ctx.Theme.WarnStyle().Render(plain)}
	}
	plain := truncateTailDisplay("Waiting approval", maxInt(1, width))
	return subagentPreviewLine{Plain: plain, Styled: ctx.Theme.WarnStyle().Render(plain)}
}

func subagentLatestEventKind(events []SubagentEvent, kind SubagentEventKind) bool {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Kind {
		case SEAssistant, SEReasoning, SEToolCall, SEPlan, SEApproval:
			return events[i].Kind == kind
		}
	}
	return false
}

func subagentErrorPreviewLines(events []SubagentEvent, width int, limit int, ctx BlockRenderContext) []subagentPreviewLine {
	if limit <= 0 {
		return nil
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind != SEToolCall || !ev.Err {
			continue
		}
		if lines := subagentErrorPreviewLinesForEvent(ev, width, limit, ctx); len(lines) > 0 {
			return lines
		}
	}
	return nil
}

func subagentErrorPreviewLinesForEvent(ev SubagentEvent, width int, limit int, ctx BlockRenderContext) []subagentPreviewLine {
	if limit <= 0 {
		return nil
	}
	if verb := explorationToolVerb(toolSemanticName(ev.Name, ev.ToolKind)); verb != "" {
		detail := subagentExplorationPreviewDetail(ev, ctx.Workspace)
		if detail == "" {
			detail = "failed"
		}
		plain := subagentPreviewToolPlain(verb, detail, width)
		return []subagentPreviewLine{{
			Plain:  plain,
			Styled: styleExplorationSummaryRow(plain, ctx),
		}}
	}
	text := strings.TrimSpace(firstNonEmpty(ev.Output, ev.Args, toolEventDisplayName(toolSemanticName(ev.Name, ev.ToolKind))+" failed"))
	segments := tailWrappedTerminalSegmentsFromEnd(text, maxInt(1, width-len("Error: ")), limit)
	if len(segments) == 0 {
		return nil
	}
	out := make([]subagentPreviewLine, 0, len(segments))
	for i, segment := range segments {
		plain := strings.TrimSpace(segment)
		if plain == "" {
			continue
		}
		if i == 0 && !strings.HasPrefix(strings.ToLower(plain), "error") {
			plain = "Error: " + plain
		}
		plain = truncateTailDisplay(plain, maxInt(1, width))
		out = append(out, subagentPreviewLine{Plain: plain, Styled: ctx.Theme.ToolErrorStyle().Render(plain)})
	}
	return out
}

func subagentToolPreviewLines(events []SubagentEvent, workspace string, width int, limit int, ctx BlockRenderContext) []subagentPreviewLine {
	if limit <= 0 {
		return nil
	}
	groups := subagentExplorationPreviewGroups(events, workspace)
	if len(groups) > 0 {
		return subagentRenderToolPreviewGroups(groups, width, limit, ctx)
	}
	if line := subagentGenericToolPreviewLine(events, width, ctx); line.Styled != "" {
		return []subagentPreviewLine{line}
	}
	return nil
}

func subagentToolPreviewLinesForEvent(events []SubagentEvent, workspace string, width int, limit int, ctx BlockRenderContext) []subagentPreviewLine {
	if len(events) == 0 || limit <= 0 {
		return nil
	}
	ev := events[len(events)-1]
	if ev.Kind != SEToolCall {
		return nil
	}
	if verb := explorationToolVerb(toolSemanticName(ev.Name, ev.ToolKind)); verb != "" {
		detail := subagentExplorationPreviewDetail(ev, workspace)
		if detail == "" || strings.EqualFold(strings.TrimSpace(detail), "completed") {
			return nil
		}
		return subagentToolPreviewLines(events, workspace, width, limit, ctx)
	}
	semantic := toolSemanticName(ev.Name, ev.ToolKind)
	name := toolEventDisplayName(semantic)
	args := strings.TrimSpace(ev.Args)
	output := strings.TrimSpace(ev.Output)
	if ev.Done && output != "" && !strings.EqualFold(output, "completed") {
		args = output
	}
	plain := strings.TrimSpace(name + " " + args)
	if plain == "" {
		return nil
	}
	plain = truncateTailDisplay(plain, maxInt(1, width))
	return []subagentPreviewLine{{Plain: plain, Styled: ctx.Theme.ToolArgsStyle().Render(plain)}}
}

func subagentExplorationPreviewGroups(events []SubagentEvent, workspace string) []subagentPreviewToolGroup {
	const detailsPerGroup = 2
	byVerb := map[string]*subagentPreviewToolGroup{}
	order := make([]string, 0, 4)
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind != SEToolCall {
			continue
		}
		verb := explorationToolVerb(toolSemanticName(ev.Name, ev.ToolKind))
		if verb == "" {
			continue
		}
		detail := subagentExplorationPreviewDetail(ev, workspace)
		if detail == "" || strings.EqualFold(strings.TrimSpace(detail), "completed") {
			continue
		}
		group := byVerb[verb]
		if group == nil {
			group = &subagentPreviewToolGroup{Verb: verb}
			byVerb[verb] = group
			order = append(order, verb)
		}
		key := normalizeSubagentTerminalPreviewLineKey(detail)
		duplicate := false
		for _, existing := range group.Details {
			if normalizeSubagentTerminalPreviewLineKey(existing) == key {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		if len(group.Details) < detailsPerGroup {
			group.Details = append(group.Details, detail)
		} else {
			group.Hidden++
		}
	}
	if len(order) == 0 {
		return nil
	}
	maxGroups := minInt(len(order), 3)
	selected := order[:maxGroups]
	hiddenGroups := order[maxGroups:]
	groups := make([]subagentPreviewToolGroup, 0, len(selected))
	for i := len(selected) - 1; i >= 0; i-- {
		group := *byVerb[selected[i]]
		reverseStrings(group.Details)
		groups = append(groups, group)
	}
	if len(hiddenGroups) > 0 && len(groups) > 0 {
		groups[len(groups)-1].Hidden += len(hiddenGroups)
	}
	return groups
}

func subagentExplorationPreviewDetail(ev SubagentEvent, workspace string) string {
	verb := explorationToolVerb(toolSemanticName(ev.Name, ev.ToolKind))
	if verb == "Search" {
		raw := firstNonEmpty(sanitizeRenderableText(ev.Args), sanitizeRenderableText(ev.Output))
		if detail := normalizeSubagentSearchPreviewDetail(raw, workspace); detail != "" {
			return detail
		}
	}
	return explorationToolDetailWithWorkspace(ev, workspace)
}

func normalizeSubagentSearchPreviewDetail(raw string, workspace string) string {
	detail := strings.TrimSpace(raw)
	if detail == "" {
		return ""
	}
	for _, prefix := range []string{"SEARCH ", "RG ", "FIND "} {
		if len(detail) >= len(prefix) && strings.EqualFold(detail[:len(prefix)], prefix) {
			detail = strings.TrimSpace(detail[len(prefix):])
			break
		}
	}
	if strings.HasSuffix(strings.ToLower(detail), " completed") {
		detail = strings.TrimSpace(detail[:len(detail)-len(" completed")])
	}
	if query, path, tagged, ok := splitExplorationQueryInPath(detail); ok {
		compacted := compactExplorationPathDetailWithBase(path, workspace)
		if compacted != "" && (compacted != path || tagged) {
			return query + " in " + compacted
		}
		return detail
	}
	quote := strings.Index(detail, `"`)
	if quote <= 0 {
		return ""
	}
	candidate := strings.TrimSpace(detail[quote:])
	query, path, tagged, ok := splitExplorationQueryInPath(candidate)
	if !ok {
		return ""
	}
	compacted := compactExplorationPathDetailWithBase(path, workspace)
	if compacted != "" && (compacted != path || tagged) {
		return query + " in " + compacted
	}
	return candidate
}

func subagentRenderToolPreviewGroups(groups []subagentPreviewToolGroup, width int, limit int, ctx BlockRenderContext) []subagentPreviewLine {
	if limit <= 0 {
		return nil
	}
	if len(groups) > limit {
		hidden := len(groups) - limit
		groups = groups[len(groups)-limit:]
		groups[len(groups)-1].Hidden += hidden
	}
	out := make([]subagentPreviewLine, 0, len(groups))
	for _, group := range groups {
		detail := strings.Join(group.Details, ", ")
		if group.Hidden > 0 {
			if detail != "" {
				detail += " "
			}
			detail += "(" + subagentPreviewMoreSuffix(group.Hidden) + ")"
		}
		plain := subagentPreviewToolPlain(group.Verb, detail, width)
		out = append(out, subagentPreviewLine{
			Plain:  plain,
			Styled: styleExplorationSummaryRow(plain, ctx),
		})
	}
	return out
}

func subagentPreviewToolPlain(verb string, detail string, width int) string {
	plain := strings.TrimSpace(strings.TrimSpace(verb) + " " + strings.TrimSpace(detail))
	return truncateTailDisplay(plain, maxInt(1, width))
}

func subagentPreviewMoreSuffix(n int) string {
	if n <= 1 {
		return "+1 more"
	}
	return "+" + strconv.Itoa(n) + " more"
}

func subagentGenericToolPreviewLine(events []SubagentEvent, width int, ctx BlockRenderContext) subagentPreviewLine {
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind != SEToolCall {
			continue
		}
		semantic := toolSemanticName(ev.Name, ev.ToolKind)
		name := toolEventDisplayName(semantic)
		args := strings.TrimSpace(ev.Args)
		output := strings.TrimSpace(ev.Output)
		if ev.Done && output != "" && !strings.EqualFold(output, "completed") {
			args = output
		}
		plain := strings.TrimSpace(name + " " + args)
		if plain == "" {
			continue
		}
		plain = truncateTailDisplay(plain, maxInt(1, width))
		return subagentPreviewLine{Plain: plain, Styled: ctx.Theme.ToolArgsStyle().Render(plain)}
	}
	return subagentPreviewLine{}
}

func reverseStrings(values []string) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
}
