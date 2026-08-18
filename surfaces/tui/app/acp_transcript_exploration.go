package tuiapp

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/caelis-labs/caelis/agent-sdk/display"
)

type explorationProjectionState struct {
	Containers []explorationContainerState
}

type explorationContainerState struct {
	StableID string
	CallIDs  []string
	Pending  bool
}

func (s *explorationProjectionState) preserveBeforeEventRemoval(events []SubagentEvent, status string) {
	if s == nil {
		return
	}
	s.reconcile(visibleNarrativeEvents(events, status), status)
}

func (m *Model) seedReconnectReplayExploration() {
	if m == nil || m.doc == nil {
		return
	}
	for _, docBlock := range m.doc.Blocks() {
		block, ok := docBlock.(*MainACPTurnBlock)
		if !ok || block == nil {
			continue
		}
		block.explorationProjection.seedReconnectReplay(block.Events, block.Status)
	}
}

func (s *explorationProjectionState) seedReconnectReplay(events []SubagentEvent, status string) {
	if s == nil {
		return
	}
	visible := visibleNarrativeEvents(events, status)
	if isTerminalACPTranscriptStatus(status) {
		s.reconcile(visible, status)
		return
	}
	// Reconnect replay paints an existing timeline for the first time, so the
	// live anti-jump rule does not need to leave its completed tail expanded.
	// A synthetic zero-row boundary lets the ordinary collector recover that
	// settled tail without persisting transient retry UI state.
	withBoundary := append([]SubagentEvent(nil), visible...)
	withBoundary = append(withBoundary, SubagentEvent{Kind: SESemanticBoundary})
	current := collectExplorationContainers(withBoundary, status)
	completed := current[:0]
	for _, container := range current {
		if !container.Pending {
			completed = append(completed, container)
		}
	}
	s.Containers = reconcileLiveExplorationContainers(visible, s.Containers, completed)
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
	current := collectExplorationContainers(events, status)
	if isTerminalACPTranscriptStatus(status) {
		// A terminal Turn cannot retain a formerly pending container. Only the
		// explicit terminal tool lifecycles collected from the current events may
		// render as settled exploration.
		s.Containers = current
		return
	}
	// Rebuild current membership from tool identity and lifecycle on every
	// render, then retain an already-present container only while every member
	// still forms the same valid exploration range. This makes settlement
	// monotonic across transient boundary removal (for example, a retry notice
	// cleared by a late in-place tool update) without allowing a reclassified
	// call to consume the ordinary lifecycle row that now owns it.
	s.Containers = reconcileLiveExplorationContainers(events, s.Containers, current)
}

func reconcileLiveExplorationContainers(events []SubagentEvent, previous, current []explorationContainerState) []explorationContainerState {
	if len(previous) == 0 {
		return current
	}
	claimed := make(map[string]bool)
	candidates := make(map[string]explorationContainerState, len(current)+len(previous))
	add := func(container explorationContainerState) {
		key := strings.TrimSpace(container.StableID)
		if key == "" || len(container.CallIDs) < 2 {
			return
		}
		candidates[key] = container
		for _, callID := range container.CallIDs {
			claimed[strings.TrimSpace(callID)] = true
		}
	}
	for _, container := range current {
		add(container)
	}
	for _, container := range previous {
		overlapsCurrent := false
		for _, callID := range container.CallIDs {
			if claimed[strings.TrimSpace(callID)] {
				overlapsCurrent = true
				break
			}
		}
		if overlapsCurrent {
			continue
		}
		retained, ok := retainExplorationContainer(events, container)
		if ok {
			add(retained)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	ordered := make([]explorationContainerState, 0, len(candidates))
	for _, event := range events {
		if event.Kind != SEToolCall {
			continue
		}
		key := strings.TrimSpace(event.CallID)
		container, ok := candidates[key]
		if !ok || strings.TrimSpace(container.StableID) != key {
			continue
		}
		ordered = append(ordered, container)
		delete(candidates, key)
	}
	return ordered
}

func retainExplorationContainer(events []SubagentEvent, previous explorationContainerState) (explorationContainerState, bool) {
	if len(previous.CallIDs) < 2 {
		return explorationContainerState{}, false
	}
	wanted := make(map[string]int, len(previous.CallIDs))
	for index, callID := range previous.CallIDs {
		callID = strings.TrimSpace(callID)
		if callID == "" {
			return explorationContainerState{}, false
		}
		wanted[callID] = index
	}
	first := -1
	last := -1
	pending := false
	next := 0
	for index, event := range events {
		if event.Kind != SEToolCall {
			continue
		}
		callID := strings.TrimSpace(event.CallID)
		position, ok := wanted[callID]
		if !ok {
			continue
		}
		if position != next || !isExplorationToolEvent(event) {
			return explorationContainerState{}, false
		}
		if first < 0 {
			first = index
		}
		last = index
		pending = pending || !event.Done
		next++
		if next == len(previous.CallIDs) {
			break
		}
	}
	if next != len(previous.CallIDs) || first < 0 || last < first {
		return explorationContainerState{}, false
	}
	for _, event := range events[first : last+1] {
		switch event.Kind {
		case SEReasoning, SEAssistant:
			continue
		case SEToolCall:
			if _, ok := wanted[strings.TrimSpace(event.CallID)]; ok && isExplorationToolEvent(event) {
				continue
			}
		}
		return explorationContainerState{}, false
	}
	return explorationContainerState{
		StableID: strings.TrimSpace(previous.CallIDs[0]),
		CallIDs:  append([]string(nil), previous.CallIDs...),
		Pending:  pending,
	}, true
}

func collectExplorationContainers(events []SubagentEvent, status string) []explorationContainerState {
	var containers []explorationContainerState
	var current []string
	currentPending := false
	flush := func() {
		// One exploration tool has no density benefit: keep its narrative and
		// tool row directly visible. Containers are reserved for runs with at
		// least two physical tool calls.
		if len(current) >= 2 {
			containers = append(containers, explorationContainerState{
				StableID: current[0],
				CallIDs:  append([]string(nil), current...),
				Pending:  currentPending,
			})
		}
		current = nil
		currentPending = false
	}
	terminal := isTerminalACPTranscriptStatus(status)
	for i := 0; i < len(events); {
		step, ok := collectExplorationRenderStep(events, i)
		if !ok {
			flush()
			i++
			continue
		}
		hasLaterStep := hasLaterTranscriptStep(events, step.end+1)
		// A later step only moves this batch out of the anti-jump live tail. It
		// does not complete a pending tool. Group such a batch under the present-
		// tense "Exploring" header, and promote it to "Explored" only after every
		// member has an explicit terminal tool update.
		settledForLayout := hasLaterStep
		if terminal {
			// A terminal Turn cannot keep advertising active exploration. Preserve
			// the prior behavior: only explicitly completed tool lifecycles compact.
			settledForLayout = step.completedExploration
		}
		if len(step.callIDs) > 0 && settledForLayout {
			current = append(current, step.callIDs...)
			currentPending = currentPending || !step.completedExploration
		} else {
			flush()
		}
		i = step.end + 1
	}
	flush()
	return containers
}

func collectStableExplorationRuns(events []SubagentEvent, status string) [][]string {
	containers := collectExplorationContainers(events, status)
	runs := make([][]string, 0, len(containers))
	for _, container := range containers {
		if !container.Pending {
			runs = append(runs, append([]string(nil), container.CallIDs...))
		}
	}
	return runs
}

type explorationRenderStep struct {
	start                int
	end                  int
	callIDs              []string
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
	for i < len(events) && events[i].Kind == SEToolCall {
		ev := events[i]
		if !isExplorationToolEvent(ev) {
			break
		}
		step.callIDs = append(step.callIDs, strings.TrimSpace(ev.CallID))
		if !ev.Done {
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
		if events[end].Kind == SESemanticBoundary {
			return idx, idx, false
		}
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
	mode := explorationToolDetailSettled
	header := "• Explored"
	if container.Pending {
		mode = explorationToolDetailLiveSummary
		header = "• Exploring"
	}
	expandable := explorationContainerCanExpand(events, width, ctx, mode)
	token := ""
	if expandable {
		token = acpStableExplorationClickToken(key)
	}
	expanded := false
	if expandable && opts.ExplorationExpanded != nil {
		expanded = opts.ExplorationExpanded(key) || opts.ExplorationExpanded(explorationStageKey(events))
	}
	tone, dim := acpToolHeaderMark(ctx, false, !container.Pending)
	if expanded {
		rows := []RenderedRow{renderACPTranscriptHeaderRowMarked(blockID, header, width, ctx, token, tone, dim)}
		rows = append(rows, explorationContainerExpandedRows(blockID, events, width, ctx, token, mode)...)
		return rows, end, true
	}
	toolEvents := explorationContainerToolEvents(events, container.CallIDs)
	rows := []RenderedRow{renderACPTranscriptHeaderRowMarked(blockID, header, width, ctx, token, tone, dim)}
	for _, detail := range explorationGroupDetailRowsWithWorkspaceMode(toolEvents, width, ctx.Workspace, mode) {
		rows = append(rows, StyledPlainClickableRow(blockID, detail, styleExplorationSummaryRow(detail, ctx), token))
	}
	return rows, end, true
}

func explorationContainerExpandedRows(blockID string, events []SubagentEvent, width int, ctx BlockRenderContext, token string, mode explorationToolDetailMode) []RenderedRow {
	rows := make([]RenderedRow, 0, len(events))
	for _, ev := range events {
		first := len(rows) == 0
		switch ev.Kind {
		case SEReasoning:
			rows = append(rows, renderExplorationNarrativeRows(blockID, ev.Text, width, ctx, ctx.Theme.ReasoningStyle(), token, first)...)
		case SEAssistant:
			rows = append(rows, renderExplorationNarrativeRows(blockID, ev.Text, width, ctx, ctx.Theme.TextStyle(), token, first)...)
		case SEToolCall:
			if isExplorationToolEvent(ev) {
				rows = append(rows, renderExplorationToolRowWithMode(blockID, ev, width, ctx, token, first, mode))
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

func explorationContainerCanExpand(events []SubagentEvent, width int, ctx BlockRenderContext, mode explorationToolDetailMode) bool {
	collapsed := explorationGroupDetailRowsWithWorkspaceMode(events, width, ctx.Workspace, mode)
	expanded := explorationRenderedPlainRows(explorationContainerExpandedRows("", events, width, ctx, "", mode))
	if len(collapsed) != len(expanded) {
		return true
	}
	for i := range collapsed {
		if collapsed[i] != expanded[i] {
			return true
		}
	}
	return false
}

func explorationRenderedPlainRows(rows []RenderedRow) []string {
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Plain)
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
	return countExplorationTools(stage) >= 2
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
		if reasoningFoldBoundaryEvent(events[i]) || isModelRetryNotice(events[i]) {
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

func renderExplorationToolRowWithMode(blockID string, ev SubagentEvent, width int, ctx BlockRenderContext, token string, first bool, mode explorationToolDetailMode) RenderedRow {
	verb := surfaceExplorationVerb(ev.Name)
	if verb == "" {
		verb = ev.Name
	}
	detail := explorationToolDetailForDisplay(ev, ctx.Workspace, mode)
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

func explorationGroupDetailRowsWithWorkspaceMode(events []SubagentEvent, width int, workspace string, mode explorationToolDetailMode) []string {
	grouped := map[string][]string{}
	order := make([]string, 0, 4)
	for _, ev := range events {
		verb := surfaceExplorationVerb(ev.Name)
		if verb == "" {
			continue
		}
		if _, ok := grouped[verb]; !ok {
			order = append(order, verb)
		}
		item := explorationToolDetailForDisplay(ev, workspace, mode)
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
	return explorationToolDetailForDisplay(ev, workspace, explorationToolDetailSettled)
}

type explorationToolDetailMode int

const (
	explorationToolDetailSettled explorationToolDetailMode = iota
	explorationToolDetailLiveSummary
)

func explorationToolDetailForDisplay(ev SubagentEvent, workspace string, mode explorationToolDetailMode) string {
	item := sanitizeRenderableText(ev.Args)
	if mode != explorationToolDetailSettled {
		if startArgs := sanitizeRenderableText(ev.StartArgs); startArgs != "" {
			item = startArgs
		}
	}
	fromArgs := item != ""
	if item == "" {
		item = sanitizeRenderableText(ev.Output)
	}
	fromOutput := !fromArgs && item != ""
	if item == "" {
		if surfaceExplorationVerb(ev.Name) != "" {
			return ""
		}
		item = ev.Name
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
	semanticName := ev.Name
	if semanticName == surfaceToolWebSearch {
		return detail
	}
	switch surfaceExplorationVerb(semanticName) {
	case "Read", "View", "List", "Glob", "Search":
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
		return styled + styleExplorationDetail(content, ctx)
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
	failIdx := nextExplorationFailedWordIndex(detail)
	numIdx, _ := nextToolDetailNumberIndex(detail)
	if failIdx < 0 && numIdx < 0 {
		return ctx.Theme.SecondaryTextStyle().Render(detail)
	}
	var styled strings.Builder
	remaining := detail
	for len(remaining) > 0 {
		nextFailIdx := nextExplorationFailedWordIndex(remaining)
		nextNumIdx, nextNumLen := nextToolDetailNumberIndex(remaining)
		if nextFailIdx < 0 && nextNumIdx < 0 {
			styled.WriteString(ctx.Theme.SecondaryTextStyle().Render(remaining))
			break
		}
		idx := nextFailIdx
		tokenLen := len("failed")
		tokenStyle := ctx.Theme.ToolErrorStyle()
		if idx < 0 || (nextNumIdx >= 0 && nextNumIdx < idx) {
			idx = nextNumIdx
			tokenLen = nextNumLen
			tokenStyle = toolDetailNumberStyle(ctx)
		}
		if idx > 0 {
			styled.WriteString(ctx.Theme.SecondaryTextStyle().Render(remaining[:idx]))
		}
		styled.WriteString(tokenStyle.Render(remaining[idx : idx+tokenLen]))
		remaining = remaining[idx+tokenLen:]
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
	case "read", "view", "glob", "search", "fetch", "skill":
		return true
	default:
		return false
	}
}

func toolSignalDisplayVerb(name string) string {
	if verb := surfaceExplorationVerb(name); verb != "" {
		return verb
	}
	info, ok := surfaceToolProfile(name)
	if !ok {
		return ""
	}
	switch info.Name {
	case surfaceToolWrite:
		return "Write"
	case surfaceToolPatch:
		return "Patch"
	case surfaceToolRunCommand:
		return "Ran"
	case surfaceToolSpawn:
		return "Spawned"
	case surfaceToolTask:
		return "Task"
	default:
		return ""
	}
}

func pluralizeUnit(n int, unit string) string {
	return display.Pluralize(n, unit)
}
