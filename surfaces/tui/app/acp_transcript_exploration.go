package tuiapp

import (
	"strconv"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

type explorationProjectionState struct {
	Containers []explorationContainerState
}

type explorationContainerState struct {
	StableID string
	CallIDs  []string
}

func renderStableExplorationRows(blockID string, events []SubagentEvent, idx int, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	if opts.StableExplorationRows == nil {
		return nil, idx, false
	}
	return opts.StableExplorationRows(blockID, events, idx, status, width, ctx, opts)
}

func (s *explorationProjectionState) reconcile(events []SubagentEvent, status string) {
	if s == nil {
		return
	}
	existing := make(map[string]explorationContainerState, len(s.Containers))
	for _, container := range s.Containers {
		if container.StableID == "" || len(container.CallIDs) == 0 {
			continue
		}
		existing[container.StableID] = container
	}
	candidates := make(map[string][]string)
	for _, callIDs := range collectStableExplorationRuns(events, status) {
		addExplorationCandidate(candidates, callIDs)
	}
	seen := map[string]bool{}
	out := make([]explorationContainerState, 0, len(s.Containers)+len(candidates))
	for stableID, callIDs := range candidates {
		container := explorationContainerState{StableID: stableID, CallIDs: callIDs}
		if prev, ok := existing[stableID]; ok {
			if len(prev.CallIDs) > len(container.CallIDs) && explorationCallIDsPresent(events, prev.CallIDs) {
				container.CallIDs = prev.CallIDs
			}
		}
		out = append(out, container)
		seen[stableID] = true
	}
	for _, prev := range s.Containers {
		if prev.StableID == "" || seen[prev.StableID] || !explorationCallIDsPresent(events, prev.CallIDs) {
			continue
		}
		out = append(out, prev)
		seen[prev.StableID] = true
	}
	sortExplorationContainers(out, events)
	s.Containers = out
}

func collectStableExplorationRuns(events []SubagentEvent, status string) [][]string {
	var runs [][]string
	var current []string
	currentHasNarrative := false
	flush := func() {
		if len(current) == 0 {
			currentHasNarrative = false
			return
		}
		if currentHasNarrative || len(current) >= 2 {
			runs = append(runs, append([]string(nil), current...))
		}
		current = nil
		currentHasNarrative = false
	}
	for i := 0; i < len(events); {
		step, ok := collectExplorationRenderStep(events, i)
		if !ok {
			flush()
			i++
			continue
		}
		settled := isTerminalACPTranscriptStatus(status) || hasLaterTranscriptStep(events, step.end+1)
		if step.completedExploration && settled {
			current = append(current, step.callIDs...)
			currentHasNarrative = currentHasNarrative || step.hasNarrative
		} else {
			flush()
		}
		i = step.end + 1
	}
	flush()
	return runs
}

type explorationRenderStep struct {
	start                int
	end                  int
	callIDs              []string
	hasNarrative         bool
	completedExploration bool
}

func collectExplorationRenderStep(events []SubagentEvent, idx int) (explorationRenderStep, bool) {
	if idx < 0 || idx >= len(events) {
		return explorationRenderStep{}, false
	}
	start := idx
	i := idx
	for i < len(events) && isExplorationNarrativeEvent(events[i]) {
		i++
	}
	if i >= len(events) || events[i].Kind != SEToolCall {
		return explorationRenderStep{}, false
	}
	step := explorationRenderStep{
		start:                start,
		end:                  i,
		completedExploration: true,
	}
	for j := start; j < i; j++ {
		if strings.TrimSpace(events[j].Text) != "" {
			step.hasNarrative = true
			break
		}
	}
	for i < len(events) && events[i].Kind == SEToolCall {
		ev := events[i]
		if !isExplorationToolEvent(ev) {
			break
		}
		if isCompactExplorationTool(ev) {
			step.callIDs = append(step.callIDs, strings.TrimSpace(ev.CallID))
		} else {
			step.completedExploration = false
		}
		step.end = i
		i++
	}
	if len(step.callIDs) == 0 {
		step.completedExploration = false
	}
	return step, true
}

func addExplorationCandidate(candidates map[string][]string, callIDs []string) {
	if len(callIDs) == 0 {
		return
	}
	stableID := callIDs[0]
	if len(callIDs) > len(candidates[stableID]) {
		candidates[stableID] = callIDs
	}
}

func explorationCallIDs(events []SubagentEvent) []string {
	out := make([]string, 0, len(events))
	seen := map[string]bool{}
	for _, ev := range events {
		if ev.Kind != SEToolCall {
			continue
		}
		callID := strings.TrimSpace(ev.CallID)
		if callID == "" || seen[callID] {
			continue
		}
		seen[callID] = true
		out = append(out, callID)
	}
	return out
}

func explorationCallIDsPresent(events []SubagentEvent, callIDs []string) bool {
	if len(callIDs) == 0 {
		return false
	}
	present := map[string]bool{}
	for _, ev := range events {
		if ev.Kind == SEToolCall {
			present[strings.TrimSpace(ev.CallID)] = true
		}
	}
	for _, callID := range callIDs {
		if !present[strings.TrimSpace(callID)] {
			return false
		}
	}
	return true
}

func sortExplorationContainers(containers []explorationContainerState, events []SubagentEvent) {
	for i := 1; i < len(containers); i++ {
		item := containers[i]
		itemIdx := firstExplorationCallIndex(events, item.StableID)
		j := i - 1
		for ; j >= 0 && firstExplorationCallIndex(events, containers[j].StableID) > itemIdx; j-- {
			containers[j+1] = containers[j]
		}
		containers[j+1] = item
	}
}

func firstExplorationCallIndex(events []SubagentEvent, callID string) int {
	callID = strings.TrimSpace(callID)
	for i, ev := range events {
		if ev.Kind == SEToolCall && strings.TrimSpace(ev.CallID) == callID {
			return i
		}
	}
	return len(events)
}

func (s *explorationProjectionState) renderContainerRows(blockID string, events []SubagentEvent, idx int, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	if s == nil || idx < 0 || idx >= len(events) {
		return nil, idx, false
	}
	for i := range s.Containers {
		start, end, ok := explorationContainerRange(events, idx, s.Containers[i].CallIDs)
		if !ok || start != idx {
			continue
		}
		return s.renderContainerAt(blockID, events[start:end+1], end, width, ctx, opts, &s.Containers[i])
	}
	return nil, idx, false
}

func explorationContainerRange(events []SubagentEvent, idx int, callIDs []string) (int, int, bool) {
	if len(callIDs) == 0 || idx < 0 || idx >= len(events) {
		return idx, idx, false
	}
	firstID := strings.TrimSpace(callIDs[0])
	if firstID == "" {
		return idx, idx, false
	}
	if events[idx].Kind == SEReasoning || events[idx].Kind == SEAssistant {
		j := idx
		for j < len(events) && isExplorationNarrativeEvent(events[j]) {
			j++
		}
		if j >= len(events) || events[j].Kind != SEToolCall || strings.TrimSpace(events[j].CallID) != firstID {
			return idx, idx, false
		}
	} else if events[idx].Kind != SEToolCall || strings.TrimSpace(events[idx].CallID) != firstID {
		return idx, idx, false
	}
	needed := map[string]bool{}
	for _, callID := range callIDs {
		needed[strings.TrimSpace(callID)] = true
	}
	seen := map[string]bool{}
	for end := idx; end < len(events); end++ {
		if events[end].Kind != SEToolCall {
			continue
		}
		callID := strings.TrimSpace(events[end].CallID)
		if needed[callID] {
			seen[callID] = true
			if len(seen) == len(needed) {
				return idx, end, true
			}
		}
	}
	return idx, idx, false
}

func (s *explorationProjectionState) renderContainerAt(blockID string, events []SubagentEvent, end int, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions, container *explorationContainerState) ([]RenderedRow, int, bool) {
	if container == nil || len(container.CallIDs) == 0 {
		return nil, end, false
	}
	key := strings.TrimSpace(container.StableID)
	if key == "" {
		return nil, end, false
	}
	token := acpStableExplorationClickToken(key)
	expanded := false
	if opts.ExplorationExpanded != nil {
		expanded = opts.ExplorationExpanded(key) || opts.ExplorationExpanded(explorationStageKey(events))
	}
	header := "• Explored"
	if expanded {
		rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
		rows = append(rows, explorationContainerExpandedRows(blockID, events, width, ctx, token)...)
		return rows, end, true
	}
	toolEvents := explorationContainerToolEvents(events, container.CallIDs)
	rows := []RenderedRow{renderACPTranscriptHeaderRow(blockID, header, width, ctx, token)}
	for _, detail := range explorationGroupDetailRowsWithWorkspace(toolEvents, width, ctx.Workspace) {
		rows = append(rows, StyledPlainClickableRow(blockID, detail, styleExplorationSummaryRow(detail, ctx), token))
	}
	return rows, end, true
}

func explorationContainerExpandedRows(blockID string, events []SubagentEvent, width int, ctx BlockRenderContext, token string) []RenderedRow {
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

func explorationContainerToolEvents(events []SubagentEvent, callIDs []string) []SubagentEvent {
	needed := map[string]bool{}
	for _, callID := range callIDs {
		needed[strings.TrimSpace(callID)] = true
	}
	out := make([]SubagentEvent, 0, len(callIDs))
	for _, ev := range events {
		if ev.Kind == SEToolCall && needed[strings.TrimSpace(ev.CallID)] {
			out = append(out, ev)
		}
	}
	return out
}

func acpStableExplorationClickToken(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	return "acp_exploration_stable:" + key
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

func renderACPLiveExplorationStageRows(blockID string, events []SubagentEvent, idx int, status string, width int, ctx BlockRenderContext) ([]RenderedRow, int, bool) {
	if idx < 0 || idx >= len(events) || isTerminalACPTranscriptStatus(status) {
		return nil, idx, false
	}
	step, ok := collectTranscriptStep(events, idx)
	if !ok || step.start != idx || !step.allExploration {
		return nil, idx, false
	}
	if step.allDone && hasLaterTranscriptStep(events, step.end+1) {
		return nil, idx, false
	}
	stage := events[step.start : step.end+1]
	toolEvents, verb, ok := liveExplorationRepeatedToolSummary(stage)
	if !ok {
		return nil, idx, false
	}
	rows := make([]RenderedRow, 0, len(stage)+1)
	for offset, ev := range stage {
		eventIdx := step.start + offset
		switch ev.Kind {
		case SEReasoning:
			if strings.TrimSpace(ev.Text) != "" {
				rows = append(rows, renderACPReasoningNarrativeRows(blockID, ev.Text, ev.ActiveBuffer, width, ctx, participantNarrativeEventActive(events, eventIdx, status))...)
			}
		case SEAssistant:
			if strings.TrimSpace(ev.Text) != "" {
				rows = append(rows, renderParticipantTurnNarrativeEventRows(blockID, ev, tuikit.LineStyleAssistant, width, ctx, participantNarrativeEventActive(events, eventIdx, status))...)
			}
		}
	}
	if summary := liveExplorationRepeatedToolSummaryRow(blockID, toolEvents, verb, width, ctx); strings.TrimSpace(summary.Plain) != "" {
		rows = append(rows, summary)
	}
	if len(rows) == 0 {
		return nil, idx, false
	}
	return rows, step.end, true
}

func liveExplorationRepeatedToolSummary(stage []SubagentEvent) ([]SubagentEvent, string, bool) {
	tools := make([]SubagentEvent, 0, len(stage))
	verb := ""
	for _, ev := range stage {
		if !isExplorationToolEvent(ev) {
			continue
		}
		nextVerb := explorationToolVerb(toolSemanticName(ev.Name, ev.ToolKind))
		if nextVerb == "" {
			return nil, "", false
		}
		if verb == "" {
			verb = nextVerb
		} else if nextVerb != verb {
			return nil, "", false
		}
		tools = append(tools, ev)
	}
	if len(tools) < 2 {
		return nil, "", false
	}
	return tools, verb, true
}

func liveExplorationRepeatedToolSummaryRow(blockID string, tools []SubagentEvent, verb string, width int, ctx BlockRenderContext) RenderedRow {
	details := make([]string, 0, len(tools))
	for _, ev := range tools {
		if detail := explorationToolDetailWithWorkspace(ev, ctx.Workspace); detail != "" {
			details = append(details, detail)
		}
	}
	detail := strings.Join(details, ", ")
	plain := strings.TrimSpace("• " + strings.TrimSpace(verb))
	if detail != "" {
		plain = strings.TrimSpace(plain + " " + detail)
	}
	plain = truncateTailDisplay(plain, maxInt(16, width))
	return renderACPTranscriptHeaderRow(blockID, plain, width, ctx, "")
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
			if prefix, prefixEnd := settledExplorationPrefixWithinLiveStep(events, step); len(prefix) > 0 {
				stage = append(stage, prefix...)
				end = prefixEnd
			}
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

func settledExplorationPrefixWithinLiveStep(events []SubagentEvent, step transcriptStep) ([]SubagentEvent, int) {
	if !step.allExploration || step.allDone || step.start < 0 || step.end >= len(events) || step.start > step.end {
		return nil, -1
	}
	prefixEnd := -1
	for i := step.start; i <= step.end; i++ {
		ev := events[i]
		if ev.Kind == SEToolCall && !ev.Done {
			break
		}
		prefixEnd = i
	}
	if prefixEnd < step.start || prefixEnd >= step.end {
		return nil, -1
	}
	prefix := events[step.start : prefixEnd+1]
	if !compactExplorationStageHasSummary(prefix) {
		return nil, -1
	}
	return prefix, prefixEnd
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
	firstToolExploration := isExplorationToolEvent(events[i])
	step := transcriptStep{
		start:          idx,
		end:            i,
		allExploration: true,
		allDone:        true,
	}
	for i < len(events) && events[i].Kind == SEToolCall {
		toolExploration := isExplorationToolEvent(events[i])
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
	detail := explorationToolDetailWithWorkspace(ev, ctx.Workspace)
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

func isCompactExplorationTool(ev SubagentEvent) bool {
	if ev.Kind != SEToolCall || !ev.Done {
		return false
	}
	return isExplorationToolEvent(ev)
}

func isExplorationToolEvent(ev SubagentEvent) bool {
	if ev.Kind != SEToolCall {
		return false
	}
	if strings.TrimSpace(ev.CallID) == "" {
		return false
	}
	return shouldDefaultCollapseToolEvent(ev)
}

func explorationGroupDetailRows(events []SubagentEvent, width int) []string {
	return explorationGroupDetailRowsWithWorkspace(events, width, "")
}

func explorationGroupDetailRowsWithWorkspace(events []SubagentEvent, width int, workspace string) []string {
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
		item := explorationToolDetailWithWorkspace(ev, workspace)
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
	return explorationToolDetailWithWorkspace(ev, "")
}

func explorationToolDetailWithWorkspace(ev SubagentEvent, workspace string) string {
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
	item = compactExplorationToolDetailWithWorkspace(ev, item, workspace)
	if ev.Err && item != "" && !fromOutput && !hasExplorationFailedStatus(item) {
		item = strings.TrimSpace(item + " failed")
	}
	return item
}

func compactExplorationToolDetail(ev SubagentEvent, detail string) string {
	return compactExplorationToolDetailWithWorkspace(ev, detail, "")
}

func compactExplorationToolDetailWithWorkspace(ev SubagentEvent, detail string, workspace string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	switch explorationToolVerb(toolSemanticName(ev.Name, ev.ToolKind)) {
	case "Read", "List", "Glob", "Search":
		return compactExplorationPathDetailWithBase(detail, workspace)
	default:
		return detail
	}
}

func compactExplorationPathDetail(detail string) string {
	return compactExplorationPathDetailWithBase(detail, "")
}

func compactExplorationPathDetailWithBase(detail string, workspace string) string {
	workspace = strings.TrimSpace(workspace)
	parts := strings.Split(detail, ",")
	if len(parts) > 1 {
		out := make([]string, 0, len(parts))
		changed := false
		for _, part := range parts {
			trimmed := strings.TrimSpace(part)
			compacted := compactExplorationPathDetailWithBase(trimmed, workspace)
			if compacted != trimmed {
				changed = true
			}
			if compacted != "" {
				out = append(out, compacted)
			}
		}
		if changed && len(out) > 0 {
			return strings.Join(out, ", ")
		}
		return detail
	}
	if query, path, tagged, ok := splitExplorationQueryInPath(detail); ok {
		compacted := compactExplorationPathDetailWithBase(path, workspace)
		if compacted != "" && (compacted != path || tagged) {
			return query + " in " + compacted
		}
		return detail
	}
	pathPart, rest, ok, tagged := splitLeadingPathHeaderParts(detail)
	if !ok {
		return detail
	}
	if !isAbsoluteDisplayPath(pathPart) {
		if displayPathHasGlobMeta(pathPart) {
			if tagged {
				return pathPart + rest
			}
			return detail
		}
		compact := displayPathBase(pathPart)
		if compact == "" || compact == pathPart {
			if tagged {
				return pathPart + rest
			}
			return detail
		}
		return compact + rest
	}
	compact := displayPathBase(pathPart)
	if compact == "" || compact == pathPart {
		if tagged {
			return pathPart + rest
		}
		return detail
	}
	return compact + rest
}

func splitExplorationQueryInPath(detail string) (query string, path string, tagged bool, ok bool) {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return "", "", false, false
	}
	idx := strings.LastIndex(strings.ToLower(detail), " in ")
	if idx < 0 {
		return "", "", false, false
	}
	before := strings.TrimSpace(detail[:idx])
	after := strings.TrimSpace(detail[idx+len(" in "):])
	if before == "" || after == "" || !strings.HasPrefix(before, `"`) {
		return "", "", false, false
	}
	pathPart, rest, pathOK, pathTagged := splitLeadingPathHeaderParts(after)
	if !pathOK || (!pathTagged && !isLikelyDisplayPath(pathPart)) || strings.TrimSpace(rest) != "" {
		return "", "", false, false
	}
	return before, pathPart, pathTagged, true
}

func displayPathHasGlobMeta(path string) bool {
	return strings.ContainsAny(path, "*?[")
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
