package tuiapp

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
)

func renderACPExplorationStageRows(blockID string, events []SubagentEvent, idx int, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	stage, end := compactExplorationStage(events, idx, status)
	if !compactExplorationStageHasSummary(stage) {
		return nil, idx, false
	}
	key := explorationStageKey(stage)
	token := acpExplorationStageClickToken(key)
	expanded := false
	if opts.ExplorationExpanded != nil {
		expanded = opts.ExplorationExpanded(key)
	}
	header := "• Explored"
	if expanded {
		rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
		expandedRows := explorationStageExpandedRows(blockID, stage, width, ctx, token)
		rows = append(rows, expandedRows...)
		return rows, end, true
	}
	rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
	for _, detail := range explorationGroupDetailRows(explorationToolEvents(stage), width) {
		rows = append(rows, StyledPlainClickableRow(blockID, detail, styleExplorationSummaryRow(detail, ctx), token))
	}
	return rows, end, true
}

func compactExplorationStageHasSummary(stage []SubagentEvent) bool {
	count := countExplorationTools(stage)
	return count > 0 && (count >= 2 || hasExplorationNarrative(stage))
}

func compactExplorationStage(events []SubagentEvent, idx int, status string) ([]SubagentEvent, int) {
	return collectExplorationStage(events, idx, status, false)
}

func potentialExplorationStage(events []SubagentEvent, idx int, status string) ([]SubagentEvent, int) {
	return collectExplorationStage(events, idx, status, true)
}

func collectExplorationStage(events []SubagentEvent, idx int, status string, includeLiveTail bool) ([]SubagentEvent, int) {
	if idx < 0 || idx >= len(events) {
		return nil, idx
	}
	stage := make([]SubagentEvent, 0, 8)
	end := idx - 1
	for i := idx; i < len(events); {
		step, ok := collectTranscriptStep(events, i)
		if !ok || !step.allExploration {
			break
		}
		settled := isTerminalACPTranscriptStatus(status) || (step.allDone && hasLaterTranscriptStep(events, step.end+1))
		if !settled {
			if includeLiveTail && len(stage) == 0 {
				stage = append(stage, events[step.start:step.end+1]...)
				end = step.end
			}
			break
		}
		stage = append(stage, events[step.start:step.end+1]...)
		end = step.end
		i = step.end + 1
	}
	return stage, end
}

type transcriptStep struct {
	start          int
	end            int
	allExploration bool
	allDone        bool
}

func collectTranscriptStep(events []SubagentEvent, idx int) (transcriptStep, bool) {
	if idx < 0 || idx >= len(events) {
		return transcriptStep{}, false
	}
	i := idx
	for i < len(events) && isExplorationNarrativeEvent(events[i]) {
		i++
	}
	if i >= len(events) || events[i].Kind != SEToolCall {
		return transcriptStep{}, false
	}
	firstToolExploration := isCompactExplorationTool(events[i])
	step := transcriptStep{
		start:          idx,
		end:            i,
		allExploration: true,
		allDone:        true,
	}
	for i < len(events) && events[i].Kind == SEToolCall {
		toolExploration := isCompactExplorationTool(events[i])
		if toolExploration != firstToolExploration {
			break
		}
		if !toolExploration {
			step.allExploration = false
		}
		if !events[i].Done {
			step.allDone = false
		}
		step.end = i
		i++
	}
	return step, true
}

func hasLaterTranscriptStep(events []SubagentEvent, start int) bool {
	for i := maxInt(0, start); i < len(events); {
		if step, ok := collectTranscriptStep(events, i); ok {
			return step.end >= i
		}
		if reasoningFoldBoundaryEvent(events[i]) {
			return true
		}
		i++
	}
	return false
}

func isExplorationNarrativeEvent(ev SubagentEvent) bool {
	return ev.Kind == SEReasoning || ev.Kind == SEAssistant
}

func hasExplorationNarrative(events []SubagentEvent) bool {
	for _, ev := range events {
		if isExplorationNarrativeEvent(ev) {
			return true
		}
	}
	return false
}

func countExplorationTools(events []SubagentEvent) int {
	count := 0
	for _, ev := range events {
		if isCompactExplorationTool(ev) {
			count++
		}
	}
	return count
}

func explorationToolEvents(events []SubagentEvent) []SubagentEvent {
	out := make([]SubagentEvent, 0, len(events))
	for _, ev := range events {
		if isCompactExplorationTool(ev) {
			out = append(out, ev)
		}
	}
	return out
}

func explorationStageExpandedRows(blockID string, events []SubagentEvent, width int, ctx BlockRenderContext, token string) []RenderedRow {
	rows := make([]RenderedRow, 0, len(events))
	for _, ev := range events {
		first := len(rows) == 0
		switch ev.Kind {
		case SEReasoning:
			rows = append(rows, renderExplorationNarrativeRows(blockID, ev.Text, width, ctx, ctx.Theme.ReasoningStyle(), token, first)...)
		case SEAssistant:
			rows = append(rows, renderExplorationNarrativeRows(blockID, ev.Text, width, ctx, ctx.Theme.TextStyle(), token, first)...)
		case SEToolCall:
			if isCompactExplorationTool(ev) {
				rows = append(rows, renderExplorationToolRow(blockID, ev, width, ctx, token, first))
			}
		}
	}
	return rows
}

func renderExplorationNarrativeRows(blockID string, text string, width int, ctx BlockRenderContext, style lipgloss.Style, token string, first bool) []RenderedRow {
	text = sanitizeRenderableText(text)
	if text == "" {
		return nil
	}
	prefix := explorationChildPrefix(first)
	continuation := strings.Repeat(" ", displayColumns(prefix))
	bodyWidth := maxInt(16, width-displayColumns(prefix))
	rows := make([]RenderedRow, 0, 2)
	firstLine := true
	for _, raw := range strings.Split(text, "\n") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		for _, segment := range strings.Split(hardWrapDisplayLine(raw, bodyWidth), "\n") {
			linePrefix := continuation
			if firstLine {
				linePrefix = prefix
				firstLine = false
			}
			plain := linePrefix + segment
			rows = append(rows, StyledPlainClickableRow(blockID, plain, stylePrefixedContentLine(ctx, linePrefix, segment, width, style), token))
		}
	}
	return rows
}

func renderExplorationToolRow(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext, token string, first bool) RenderedRow {
	verb := explorationToolVerb(toolSemanticName(ev.Name, ev.ToolKind))
	if verb == "" {
		verb = strings.ToUpper(strings.TrimSpace(ev.Name))
	}
	detail := explorationToolDetail(ev)
	prefix := explorationChildPrefix(first)
	detail = truncateTailDisplay(detail, maxInt(16, width-displayColumns(prefix)-displayColumns(verb)-1))
	plain := prefix + strings.TrimSpace(verb+" "+detail)
	styled := ctx.Theme.TranscriptMetaStyle().Render(prefix) +
		toolActionStyle(ctx, verb).Render(verb)
	if detail != "" {
		styled += " " + styleExplorationDetail(detail, ctx)
	}
	return StyledPlainClickableRow(blockID, plain, styled, token)
}

func explorationChildPrefix(first bool) string {
	if first {
		return "  └ "
	}
	return "    "
}

func explorationStageKey(events []SubagentEvent) string {
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.Kind != SEToolCall {
			continue
		}
		if id := strings.TrimSpace(ev.CallID); id != "" {
			ids = append(ids, id)
		}
	}
	return strings.Join(ids, ",")
}

func acpExplorationStageClickToken(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return "acp_exploration_stage:" + key
}

func renderACPExplorationGroupRows(blockID string, events []SubagentEvent, idx int, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	group, end := compactExplorationGroup(events, idx, opts)
	if len(group) < 2 {
		return nil, idx, false
	}
	if shouldDeferLiveTailStageCompaction(events, end, status) {
		return nil, idx, false
	}
	summary := "• Explored"
	token := explorationGroupClickToken(group)
	rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, summary, width, ctx, token)}
	for _, detail := range explorationGroupDetailRows(group, width) {
		rows = append(rows, StyledPlainClickableRow(blockID, detail, styleExplorationSummaryRow(detail, ctx), token))
	}
	return rows, end, true
}

func compactExplorationGroup(events []SubagentEvent, idx int, opts acpTranscriptRenderOptions) ([]SubagentEvent, int) {
	if idx < 0 || idx >= len(events) {
		return nil, idx
	}
	group := make([]SubagentEvent, 0, 4)
	end := idx - 1
	for i := idx; i < len(events); i++ {
		ev := events[i]
		if !isCompactExplorationTool(ev) {
			break
		}
		callID := strings.TrimSpace(ev.CallID)
		if opts.ToolPanelExpanded != nil && opts.ToolPanelExpanded(callID) {
			break
		}
		group = append(group, ev)
		end = i
	}
	return group, end
}

func isCompactExplorationTool(ev SubagentEvent) bool {
	if ev.Kind != SEToolCall || !ev.Done {
		return false
	}
	if strings.TrimSpace(ev.CallID) == "" {
		return false
	}
	return shouldDefaultCollapseToolEvent(ev)
}

func explorationGroupDetailRows(events []SubagentEvent, width int) []string {
	grouped := map[string][]string{}
	order := make([]string, 0, 4)
	for _, ev := range events {
		verb := explorationToolVerb(toolSemanticName(ev.Name, ev.ToolKind))
		if verb == "" {
			continue
		}
		if _, ok := grouped[verb]; !ok {
			order = append(order, verb)
		}
		item := explorationToolDetail(ev)
		if item != "" {
			grouped[verb] = append(grouped[verb], item)
		}
	}
	if len(order) == 0 {
		return nil
	}
	rows := make([]string, 0, len(order))
	for i, verb := range order {
		detail := strings.Join(grouped[verb], ", ")
		if strings.TrimSpace(verb+" "+detail) == "" {
			continue
		}
		prefix := "  "
		if i == 0 {
			prefix += "└ "
		} else {
			prefix += "  "
		}
		rows = append(rows, wrapExplorationSummaryDetail(prefix, verb, detail, width)...)
	}
	return rows
}

func explorationToolDetail(ev SubagentEvent) string {
	item := sanitizeRenderableText(ev.Args)
	fromArgs := item != ""
	if item == "" {
		item = sanitizeRenderableText(ev.Output)
	}
	fromOutput := !fromArgs && item != ""
	if item == "" {
		if explorationToolVerb(toolSemanticName(ev.Name, ev.ToolKind)) != "" {
			return ""
		}
		item = strings.ToUpper(strings.TrimSpace(ev.Name))
	}
	item = normalizeExplorationFailedDetail(item)
	if ev.Err && item != "" && !fromOutput && !hasExplorationFailedStatus(item) {
		item = strings.TrimSpace(item + " failed")
	}
	return item
}

func normalizeExplorationFailedDetail(detail string) string {
	trimmed := strings.TrimSpace(detail)
	lower := strings.ToLower(trimmed)
	if lower == "failed failed" {
		return "failed"
	}
	const duplicateSuffix = " failed failed"
	if strings.HasSuffix(lower, duplicateSuffix) {
		return strings.TrimSpace(trimmed[:len(trimmed)-len(duplicateSuffix)] + " failed")
	}
	return trimmed
}

func hasExplorationFailedStatus(detail string) bool {
	_, ok := splitExplorationFailedStatus(detail)
	return ok
}

func wrapExplorationSummaryDetail(prefix string, verb string, detail string, width int) []string {
	verb = strings.TrimSpace(verb)
	detail = strings.Join(strings.Fields(strings.TrimSpace(detail)), " ")
	if verb == "" {
		if detail == "" {
			return nil
		}
		available := maxInt(8, width-displayColumns(prefix))
		segments := wrapToolOutputText(detail, available)
		rows := make([]string, 0, len(segments))
		for i, segment := range segments {
			linePrefix := prefix
			if i > 0 {
				linePrefix = strings.Repeat(" ", displayColumns(prefix))
			}
			rows = append(rows, linePrefix+segment)
		}
		return rows
	}
	if detail == "" {
		return []string{prefix + verb}
	}
	continuation := strings.Repeat(" ", displayColumns(prefix)+displayColumns(verb)+1)
	available := maxInt(8, width-displayColumns(continuation))
	segments := wrapToolOutputText(detail, available)
	if len(segments) == 0 {
		return []string{prefix + verb}
	}
	rows := make([]string, 0, len(segments))
	rows = append(rows, prefix+verb+" "+segments[0])
	for _, segment := range segments[1:] {
		rows = append(rows, continuation+segment)
	}
	return rows
}

func styleExplorationSummaryRow(row string, ctx BlockRenderContext) string {
	plainPrefix := ""
	content := row
	if strings.HasPrefix(row, "  └ ") {
		plainPrefix = "  └ "
		content = strings.TrimPrefix(row, plainPrefix)
	} else if strings.HasPrefix(row, "    ") {
		plainPrefix = "    "
		content = strings.TrimPrefix(row, plainPrefix)
	} else if strings.HasPrefix(row, "  ") {
		plainPrefix = "  "
		content = strings.TrimPrefix(row, plainPrefix)
	}
	verb, detail, _ := strings.Cut(strings.TrimSpace(content), " ")
	styled := ctx.Theme.TranscriptMetaStyle().Render(plainPrefix)
	if verb != "" && !isExplorationSummaryVerb(verb) {
		return styled + ctx.Theme.SecondaryTextStyle().Render(content)
	}
	if verb != "" {
		styled += toolActionStyle(ctx, verb).Render(verb)
	}
	if detail != "" {
		styled += " " + styleExplorationDetail(detail, ctx)
	}
	return styled
}

func styleExplorationDetail(detail string, ctx BlockRenderContext) string {
	if !containsExplorationFailedWord(detail) {
		return ctx.Theme.SecondaryTextStyle().Render(detail)
	}
	var styled strings.Builder
	remaining := detail
	for len(remaining) > 0 {
		idx := nextExplorationFailedWordIndex(remaining)
		if idx < 0 {
			styled.WriteString(ctx.Theme.SecondaryTextStyle().Render(remaining))
			break
		}
		if idx > 0 {
			styled.WriteString(ctx.Theme.SecondaryTextStyle().Render(remaining[:idx]))
		}
		styled.WriteString(ctx.Theme.ToolErrorStyle().Render(remaining[idx : idx+len("failed")]))
		remaining = remaining[idx+len("failed"):]
	}
	return styled.String()
}

func splitExplorationFailedStatus(detail string) (string, bool) {
	trimmed := strings.TrimSpace(detail)
	lower := strings.ToLower(trimmed)
	if lower == "failed" {
		return "", true
	}
	const suffix = " failed"
	if strings.HasSuffix(lower, suffix) {
		return strings.TrimSpace(trimmed[:len(trimmed)-len(suffix)]), true
	}
	return "", false
}

func containsExplorationFailedWord(detail string) bool {
	return nextExplorationFailedWordIndex(detail) >= 0
}

func nextExplorationFailedWordIndex(detail string) int {
	lower := strings.ToLower(detail)
	const marker = "failed"
	for offset := 0; offset < len(lower); {
		idx := strings.Index(lower[offset:], marker)
		if idx < 0 {
			return -1
		}
		idx += offset
		beforeOK := idx == 0 || !isASCIIAlphaNum(lower[idx-1])
		after := idx + len(marker)
		afterOK := after >= len(lower) || !isASCIIAlphaNum(lower[after])
		if beforeOK && afterOK {
			return idx
		}
		offset = idx + len(marker)
	}
	return -1
}

func isASCIIAlphaNum(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9'
}

func isExplorationSummaryVerb(verb string) bool {
	switch strings.ToLower(strings.TrimSpace(verb)) {
	case "read", "list", "glob", "search":
		return true
	default:
		return false
	}
}

func explorationToolVerb(name string) string {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "READ":
		return "Read"
	case "LIST":
		return "List"
	case "GLOB":
		return "Glob"
	case "RG", "SEARCH", "FIND":
		return "Search"
	default:
		return ""
	}
}

func explorationGroupClickToken(events []SubagentEvent) string {
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		if callID := strings.TrimSpace(ev.CallID); callID != "" {
			ids = append(ids, callID)
		}
	}
	if len(ids) == 0 {
		return ""
	}
	return "acp_exploration_group:" + strings.Join(ids, ",")
}

func pluralizeUnit(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	switch unit {
	case "entry":
		return strconv.Itoa(n) + " entries"
	case "match":
		return strconv.Itoa(n) + " matches"
	case "search":
		return strconv.Itoa(n) + " searches"
	}
	return strconv.Itoa(n) + " " + unit + "s"
}
