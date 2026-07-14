package tuiapp

import (
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/display"
)

func toolPanelExpanded(state map[string]bool, callID string) bool {
	callID = strings.TrimSpace(callID)
	if callID == "" || state == nil {
		return true
	}
	expanded, ok := state[callID]
	if !ok {
		return true
	}
	return expanded
}

func toolPanelFullOutput(state map[string]bool, callID string) bool {
	callID = strings.TrimSpace(callID)
	if callID == "" || state == nil {
		return false
	}
	return state[callID]
}

func toggleToolPanelExpanded(state *map[string]bool, callID string) bool {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return false
	}
	if *state == nil {
		*state = map[string]bool{}
	}
	next := !toolPanelExpanded(*state, callID)
	(*state)[callID] = next
	return true
}

func toggleToolPanelClick(expandedState *map[string]bool, fullOutputState *map[string]bool, events []SubagentEvent, callID string) bool {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return false
	}
	if !toolPanelExpanded(mapValue(expandedState), callID) {
		if !toolPanelCanExpandCollapsed(events, callID) {
			return false
		}
		setToolPanelExpandedWithOutput(expandedState, fullOutputState, callID, true)
		return true
	}
	if toolPanelFullOutput(mapValue(fullOutputState), callID) {
		if toolPanelCanCollapseExpanded(events, callID) {
			setToolPanelExpandedWithOutput(expandedState, fullOutputState, callID, false)
			return true
		}
		if fullOutputState != nil && *fullOutputState != nil {
			delete(*fullOutputState, callID)
			return true
		}
		return false
	}
	if toolPanelHasHiddenToolArgs(events, callID) || toolPanelHasHiddenSummary(events, callID) {
		if fullOutputState == nil {
			return false
		}
		if *fullOutputState == nil {
			*fullOutputState = map[string]bool{}
		}
		(*fullOutputState)[callID] = true
		return true
	}
	if toolPanelCanCollapseExpanded(events, callID) {
		setToolPanelExpandedWithOutput(expandedState, fullOutputState, callID, false)
		return true
	}
	return false
}

func mapValue(ptr *map[string]bool) map[string]bool {
	if ptr == nil {
		return nil
	}
	return *ptr
}

func setToolPanelExpandedState(state *map[string]bool, callID string, expanded bool) {
	if state == nil || strings.TrimSpace(callID) == "" {
		return
	}
	if *state == nil {
		*state = map[string]bool{}
	}
	(*state)[strings.TrimSpace(callID)] = expanded
}

func setToolPanelExpandedWithOutput(expandedState *map[string]bool, fullOutputState *map[string]bool, callID string, expanded bool) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return
	}
	setToolPanelExpandedState(expandedState, callID, expanded)
	if !expanded && fullOutputState != nil && *fullOutputState != nil {
		delete(*fullOutputState, callID)
	}
}

func keyedExpansion(state map[string]bool, key string) bool {
	key = strings.TrimSpace(key)
	if key == "" || state == nil {
		return false
	}
	return state[key]
}

func toggleKeyedExpansion(state *map[string]bool, key string) bool {
	key = strings.TrimSpace(key)
	if state == nil || key == "" {
		return false
	}
	if *state == nil {
		*state = map[string]bool{}
	}
	(*state)[key] = !(*state)[key]
	return true
}

type toolPanelScrollState struct {
	Offset                int
	FollowTail            bool
	ScrollbarVisibleUntil time.Time
}

func defaultToolPanelScrollState() toolPanelScrollState {
	return toolPanelScrollState{FollowTail: true}
}

func toolPanelScrollStateFromMap(state map[string]toolPanelScrollState, callID string) toolPanelScrollState {
	callID = strings.TrimSpace(callID)
	if callID == "" || state == nil {
		return defaultToolPanelScrollState()
	}
	value, ok := state[callID]
	if !ok {
		return defaultToolPanelScrollState()
	}
	return value
}

func scrollToolPanelState(state *map[string]toolPanelScrollState, callID string, total int, delta int) bool {
	callID = strings.TrimSpace(callID)
	if state == nil || callID == "" {
		return false
	}
	value := defaultToolPanelScrollState()
	if *state != nil {
		value = toolPanelScrollStateFromMap(*state, callID)
	}
	if !scrollPanelState(&value.Offset, &value.FollowTail, total, acpTerminalPanelMaxLines, delta) {
		return false
	}
	value.ScrollbarVisibleUntil = time.Now().Add(scrollbarVisibleDuration)
	if *state == nil {
		*state = map[string]toolPanelScrollState{}
	}
	(*state)[callID] = value
	return true
}

func (b *MainACPTurnBlock) toolPanelExpanded(callID string) bool {
	if b == nil {
		return true
	}
	return toolPanelExpanded(b.ExpandedTools, callID)
}

func (b *MainACPTurnBlock) toolPanelFullOutput(callID string) bool {
	if b == nil {
		return false
	}
	return toolPanelFullOutput(b.ExpandedToolOutput, callID)
}

func (b *MainACPTurnBlock) renderToolPanelRows(request toolPanelRenderRequest) []RenderedRow {
	if b == nil {
		return request.renderUncached()
	}
	return renderCachedToolPanelRows(&b.toolPanelRenderCache, request, b.toolPanelScrollState(request.CallID))
}

func (b *MainACPTurnBlock) toggleToolPanelExpanded(callID string) bool {
	if b == nil {
		return false
	}
	return toggleToolPanelExpanded(&b.ExpandedTools, callID)
}

func (b *MainACPTurnBlock) toggleToolPanelClick(callID string) bool {
	if b == nil {
		return false
	}
	return toggleToolPanelClick(&b.ExpandedTools, &b.ExpandedToolOutput, b.Events, callID)
}

func (b *MainACPTurnBlock) setToolPanelExpanded(callID string, expanded bool) {
	if b == nil {
		return
	}
	setToolPanelExpandedWithOutput(&b.ExpandedTools, &b.ExpandedToolOutput, callID, expanded)
}

func (b *MainACPTurnBlock) reasoningExpanded(key string) bool {
	if b == nil {
		return false
	}
	return keyedExpansion(b.ExpandedThought, key)
}

func (b *MainACPTurnBlock) toggleReasoningExpanded(key string) bool {
	if b == nil {
		return false
	}
	return toggleKeyedExpansion(&b.ExpandedThought, key)
}

func (b *MainACPTurnBlock) explorationExpanded(key string) bool {
	if b == nil {
		return false
	}
	return keyedExpansion(b.ExpandedExplore, key)
}

func (b *MainACPTurnBlock) toggleExplorationExpanded(key string) bool {
	if b == nil {
		return false
	}
	return toggleKeyedExpansion(&b.ExpandedExplore, key)
}

func (b *MainACPTurnBlock) stableExplorationRows(blockID string, events []SubagentEvent, idx int, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	if b == nil {
		return nil, idx, false
	}
	return b.explorationProjection.renderContainerRows(blockID, events, idx, status, width, ctx, opts)
}

func (b *MainACPTurnBlock) toolPanelScrollState(callID string) toolPanelScrollState {
	if b == nil {
		return defaultToolPanelScrollState()
	}
	return toolPanelScrollStateFromMap(b.ToolPanelScroll, callID)
}

func (b *MainACPTurnBlock) ScrollToolPanel(callID string, delta int, ctx BlockRenderContext) bool {
	return false
}

func (b *MainACPTurnBlock) CanScrollToolPanel(callID string, delta int, ctx BlockRenderContext) bool {
	return false
}

func (b *MainACPTurnBlock) collapseAllToolPanels() {
	if b == nil {
		return
	}
	b.ExpandedTools = collapseToolPanelsForEvents(b.ExpandedTools, b.Events)
}

func (b *ParticipantTurnBlock) toolPanelExpanded(callID string) bool {
	if b == nil {
		return true
	}
	return toolPanelExpanded(b.ExpandedTools, callID)
}

func (b *ParticipantTurnBlock) toolPanelFullOutput(callID string) bool {
	if b == nil {
		return false
	}
	return toolPanelFullOutput(b.ExpandedToolOutput, callID)
}

func (b *ParticipantTurnBlock) renderToolPanelRows(request toolPanelRenderRequest) []RenderedRow {
	if b == nil {
		return request.renderUncached()
	}
	return renderCachedToolPanelRows(&b.toolPanelRenderCache, request, b.toolPanelScrollState(request.CallID))
}

func (b *ParticipantTurnBlock) toggleToolPanelExpanded(callID string) bool {
	if b == nil {
		return false
	}
	return toggleToolPanelExpanded(&b.ExpandedTools, callID)
}

func (b *ParticipantTurnBlock) toggleToolPanelClick(callID string) bool {
	if b == nil {
		return false
	}
	return toggleToolPanelClick(&b.ExpandedTools, &b.ExpandedToolOutput, b.Events, callID)
}

func (b *ParticipantTurnBlock) setToolPanelExpanded(callID string, expanded bool) {
	if b == nil {
		return
	}
	setToolPanelExpandedWithOutput(&b.ExpandedTools, &b.ExpandedToolOutput, callID, expanded)
}

func (b *ParticipantTurnBlock) reasoningExpanded(key string) bool {
	if b == nil {
		return false
	}
	return keyedExpansion(b.ExpandedThought, key)
}

func (b *ParticipantTurnBlock) toggleReasoningExpanded(key string) bool {
	if b == nil {
		return false
	}
	return toggleKeyedExpansion(&b.ExpandedThought, key)
}

func (b *ParticipantTurnBlock) explorationExpanded(key string) bool {
	if b == nil {
		return false
	}
	return keyedExpansion(b.ExpandedExplore, key)
}

func (b *ParticipantTurnBlock) toggleExplorationExpanded(key string) bool {
	if b == nil {
		return false
	}
	return toggleKeyedExpansion(&b.ExpandedExplore, key)
}

func (b *ParticipantTurnBlock) stableExplorationRows(blockID string, events []SubagentEvent, idx int, status string, width int, ctx BlockRenderContext, opts acpTranscriptRenderOptions) ([]RenderedRow, int, bool) {
	if b == nil {
		return nil, idx, false
	}
	return b.explorationProjection.renderContainerRows(blockID, events, idx, status, width, ctx, opts)
}

func (b *ParticipantTurnBlock) toolPanelScrollState(callID string) toolPanelScrollState {
	if b == nil {
		return defaultToolPanelScrollState()
	}
	return toolPanelScrollStateFromMap(b.ToolPanelScroll, callID)
}

func (b *ParticipantTurnBlock) ScrollToolPanel(callID string, delta int, ctx BlockRenderContext) bool {
	return false
}

func (b *ParticipantTurnBlock) CanScrollToolPanel(callID string, delta int, ctx BlockRenderContext) bool {
	return false
}

func (b *ParticipantTurnBlock) collapseAllToolPanels() {
	if b == nil {
		return
	}
	b.ExpandedTools = collapseToolPanelsForEvents(b.ExpandedTools, b.Events)
}

func collapseToolPanelsForEvents(state map[string]bool, events []SubagentEvent) map[string]bool {
	callIDs := collectToolPanelCallIDs(events)
	if len(callIDs) == 0 {
		return state
	}
	if state == nil {
		state = map[string]bool{}
	}
	for _, callID := range callIDs {
		if !shouldDefaultCollapseCallID(events, callID) {
			continue
		}
		state[callID] = false
	}
	return state
}

func shouldDefaultCollapseCallID(events []SubagentEvent, callID string) bool {
	var candidate SubagentEvent
	for _, ev := range events {
		if ev.Kind != SEToolCall || strings.TrimSpace(ev.CallID) != strings.TrimSpace(callID) {
			continue
		}
		if strings.TrimSpace(candidate.Name) == "" {
			candidate = ev
		}
	}
	return shouldDefaultCollapseToolEvent(candidate)
}

func toolPanelHasHiddenSummary(events []SubagentEvent, callID string) bool {
	final, ok := finalToolEventForCallID(events, callID)
	if !ok {
		return false
	}
	return toolPanelEventHasHiddenOutputSummary(final)
}

func toolPanelCanExpandCollapsed(events []SubagentEvent, callID string) bool {
	ev, ok := finalToolEventForCallID(events, callID)
	if !ok {
		ev, ok = latestToolEventForCallID(events, callID)
		if !ok {
			return false
		}
	}
	if toolPanelEventHasHiddenToolArgs(ev) {
		return true
	}
	return shouldRenderACPToolPanel(ev.Output, ev.Err)
}

func toolPanelCanCollapseExpanded(events []SubagentEvent, callID string) bool {
	ev, ok := finalToolEventForCallID(events, callID)
	if !ok {
		return false
	}
	if shouldDefaultCollapseToolEvent(ev) {
		return true
	}
	if !isMutationPanelToolEvent(ev) {
		return false
	}
	text := sanitizeRenderableText(ev.Output)
	if !shouldRenderACPToolPanel(text, ev.Err) || mutationPanelTextIsHeaderOnly(ev, text) {
		return false
	}
	return true
}

func toolPanelHasHiddenToolArgs(events []SubagentEvent, callID string) bool {
	ev, ok := latestToolEventForCallID(events, callID)
	if !ok {
		return false
	}
	return toolPanelEventHasHiddenToolArgs(ev)
}

func toolPanelEventHasHiddenToolArgs(ev SubagentEvent) bool {
	fullArgs := strings.TrimSpace(ev.FullArgs)
	if fullArgs == "" {
		return false
	}
	return fullArgs != strings.TrimSpace(ev.Args)
}

func toolPanelEventHasHiddenOutputSummary(ev SubagentEvent) bool {
	if ev.Kind != SEToolCall || !ev.Done || !shouldRenderToolEvent(ev) || isMutationPanelToolEvent(ev) {
		return false
	}
	if strings.EqualFold(toolSemanticName(ev.Name, ev.ToolKind), "SPAWN") {
		// Completed Spawn calls default to the same bounded terminal preview used
		// while the child is running. The exact canonical Final Message remains
		// available through the panel's full-output state.
		return shouldRenderACPToolPanel(ev.Output, ev.Err)
	}
	return finalToolOutputSummaryHidesLines(ev.Output)
}

func finalToolOutputSummaryHidesLines(text string) bool {
	return len(nonEmptyToolOutputLines(text)) > acpTerminalPanelMaxLines
}

func latestToolEventForCallID(events []SubagentEvent, callID string) (SubagentEvent, bool) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return SubagentEvent{}, false
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind == SEToolCall && strings.TrimSpace(ev.CallID) == callID {
			return ev, true
		}
	}
	return SubagentEvent{}, false
}

func finalToolEventForCallID(events []SubagentEvent, callID string) (SubagentEvent, bool) {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return SubagentEvent{}, false
	}
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		if ev.Kind != SEToolCall || strings.TrimSpace(ev.CallID) != callID {
			continue
		}
		if ev.Done {
			return ev, true
		}
		return SubagentEvent{}, false
	}
	return SubagentEvent{}, false
}

func collectToolPanelCallIDs(events []SubagentEvent) []string {
	if len(events) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	callIDs := make([]string, 0, len(events))
	for _, ev := range events {
		if ev.Kind != SEToolCall {
			continue
		}
		callID := strings.TrimSpace(ev.CallID)
		if callID == "" {
			continue
		}
		if _, ok := seen[callID]; ok {
			continue
		}
		seen[callID] = struct{}{}
		callIDs = append(callIDs, callID)
	}
	return callIDs
}

func shouldDefaultCollapseToolPanel(name string) bool {
	return display.IsExplorationTool(name)
}

func shouldDefaultCollapseToolEvent(ev SubagentEvent) bool {
	return shouldDefaultCollapseToolPanel(toolSemanticName(ev.Name, ev.ToolKind))
}
