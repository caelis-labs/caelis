package session

import (
	"slices"
	"strings"
)

const maxReplayTraceEvents = 64

// IsTransient reports whether one event is runtime-transient only.
func IsTransient(event *Event) bool {
	if event == nil {
		return true
	}
	return IsUIOnly(event) || IsOverlay(event) || IsNotice(event)
}

// IsCanonicalHistoryEvent reports whether one event belongs to durable history.
func IsCanonicalHistoryEvent(event *Event) bool {
	if event == nil {
		return false
	}
	if IsTransient(event) || IsMirror(event) || IsJournal(event) {
		return false
	}
	return true
}

// IsInvocationVisibleEvent reports whether one event may participate in the
// current invocation context. Overlay events are transient display overlays, so
// they are not model-visible even when they mirror otherwise canonical shapes.
func IsInvocationVisibleEvent(event *Event) bool {
	if event == nil || IsUIOnly(event) || IsOverlay(event) || IsNotice(event) || IsMirror(event) || IsJournal(event) {
		return false
	}
	return true
}

// IsSharedDialogueEvent reports whether one event belongs to the public
// user/final-assistant ledger shared by all agents in the session.
func IsSharedDialogueEvent(event *Event) bool {
	if event == nil || !IsCanonicalHistoryEvent(event) {
		return false
	}
	switch EventTypeOf(event) {
	case EventTypeUser, EventTypeAssistant:
		return true
	default:
		return false
	}
}

// IsMainInvocationVisibleEvent reports whether one event belongs to the main
// controller context. Delegated subagent tool work remains private to its owner,
// while public user/final assistant dialogue is visible across participants.
func IsMainInvocationVisibleEvent(event *Event) bool {
	if !IsInvocationVisibleEvent(event) {
		return false
	}
	if EventTypeOf(event) == EventTypeContext {
		return true
	}
	if event.Scope == nil {
		return true
	}
	if strings.TrimSpace(event.Scope.Participant.ID) == "" {
		return true
	}
	if event.Scope.Participant.Role == ParticipantRoleDelegated {
		return false
	}
	source := strings.ToLower(strings.TrimSpace(event.Scope.Source))
	if source == "agent_spawn" || strings.Contains(source, "spawn") {
		return false
	}
	return IsSharedDialogueEvent(event)
}

// IsReplayDialogueEvent reports whether one event belongs to the lightweight
// replay transcript shown for all turns.
func IsReplayDialogueEvent(event *Event) bool {
	if IsSharedDialogueEvent(event) {
		return true
	}
	if !IsMirror(event) {
		return false
	}
	switch EventTypeOf(event) {
	case EventTypeUser, EventTypeAssistant:
		return true
	default:
		return false
	}
}

// IsMainReplayTraceEvent reports whether one event is durable main-run trace
// that may be restored for the latest replayed turn.
func IsMainReplayTraceEvent(event *Event) bool {
	if event == nil || (!IsCanonicalHistoryEvent(event) && !IsMirror(event)) {
		return false
	}
	switch EventTypeOf(event) {
	case EventTypeToolCall, EventTypeToolResult, EventTypePlan:
	case EventTypeLifecycle:
		if event.Lifecycle == nil {
			return false
		}
	default:
		return false
	}
	if IsCanonicalHistoryEvent(event) {
		return IsMainInvocationVisibleEvent(event)
	}
	return event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) == ""
}

// FilterReplayTranscriptEvents returns the bounded transcript replay view:
// all-turn dialogue plus latest-turn durable main trace.
func FilterReplayTranscriptEvents(events []*Event, includeTransient bool) []*Event {
	if includeTransient {
		return events
	}
	latestTurnID := latestReplayTurnID(events)
	traceIndexes := latestTurnReplayTraceIndexes(events, latestTurnID, maxReplayTraceEvents)
	out := make([]*Event, 0, len(events))
	for i, event := range events {
		if event == nil {
			continue
		}
		if IsReplayDialogueEvent(event) || traceIndexes[i] {
			out = append(out, event)
		}
	}
	return out
}

func latestReplayTurnID(events []*Event) string {
	for i := len(events) - 1; i >= 0; i-- {
		event := events[i]
		if !replayLatestTurnAnchor(event) {
			continue
		}
		if turnID := eventTurnID(event); turnID != "" {
			return turnID
		}
	}
	return ""
}

func replayLatestTurnAnchor(event *Event) bool {
	if IsMainReplayTraceEvent(event) {
		return true
	}
	if !IsReplayDialogueEvent(event) {
		return false
	}
	// Participant dialogue is replayed as dialogue, but it must not move the
	// latest main-turn trace window away from the main controller turn.
	return event.Scope == nil || strings.TrimSpace(event.Scope.Participant.ID) == ""
}

func latestTurnReplayTraceIndexes(events []*Event, latestTurnID string, limit int) map[int]bool {
	if latestTurnID == "" || limit <= 0 {
		return nil
	}
	units := replayTraceUnits(events, latestTurnID)
	return boundedReplayTraceIndexes(units, limit)
}

type replayTraceUnit struct {
	indexes   []int
	lastIndex int
}

type replayTraceToolUnit struct {
	callIndex   int
	resultIndex int
}

func replayTraceUnits(events []*Event, latestTurnID string) []replayTraceUnit {
	tools := map[string]*replayTraceToolUnit{}
	standalone := []replayTraceUnit(nil)
	var lastPlan *replayTraceUnit
	var lastLifecycle *replayTraceUnit
	for i, event := range events {
		if eventTurnID(event) != latestTurnID || !IsMainReplayTraceEvent(event) {
			continue
		}
		switch EventTypeOf(event) {
		case EventTypeToolCall, EventTypeToolResult:
			callID := replayTraceToolCallID(event)
			if callID == "" {
				standalone = append(standalone, replayTraceUnit{indexes: []int{i}, lastIndex: i})
				continue
			}
			unit := tools[callID]
			if unit == nil {
				unit = &replayTraceToolUnit{callIndex: -1, resultIndex: -1}
				tools[callID] = unit
			}
			switch EventTypeOf(event) {
			case EventTypeToolCall:
				if unit.callIndex < 0 {
					unit.callIndex = i
				}
			case EventTypeToolResult:
				unit.resultIndex = i
			}
		case EventTypePlan:
			lastPlan = &replayTraceUnit{indexes: []int{i}, lastIndex: i}
		case EventTypeLifecycle:
			lastLifecycle = &replayTraceUnit{indexes: []int{i}, lastIndex: i}
		}
	}
	units := make([]replayTraceUnit, 0, len(tools)+len(standalone)+2)
	for _, tool := range tools {
		indexes := []int(nil)
		if tool.callIndex >= 0 {
			indexes = append(indexes, tool.callIndex)
		}
		if tool.resultIndex >= 0 && tool.resultIndex != tool.callIndex {
			indexes = append(indexes, tool.resultIndex)
		}
		if len(indexes) == 0 {
			continue
		}
		slices.Sort(indexes)
		units = append(units, replayTraceUnit{indexes: indexes, lastIndex: indexes[len(indexes)-1]})
	}
	units = append(units, standalone...)
	if lastPlan != nil {
		units = append(units, *lastPlan)
	}
	if lastLifecycle != nil {
		units = append(units, *lastLifecycle)
	}
	slices.SortFunc(units, func(a, b replayTraceUnit) int {
		switch {
		case a.lastIndex < b.lastIndex:
			return -1
		case a.lastIndex > b.lastIndex:
			return 1
		default:
			return 0
		}
	})
	return units
}

func boundedReplayTraceIndexes(units []replayTraceUnit, limit int) map[int]bool {
	if len(units) == 0 || limit <= 0 {
		return nil
	}
	total := 0
	for _, unit := range units {
		total += len(unit.indexes)
	}
	selected := map[int]bool{}
	if total <= limit {
		for _, unit := range units {
			for _, idx := range unit.indexes {
				selected[idx] = true
			}
		}
		return selected
	}
	// Keep the newest logical trace units first. Lifecycle events are expected
	// to be terminal for a turn, so terminal lifecycle is naturally retained as
	// the newest unit when it is present.
	used := 0
	for i := len(units) - 1; i >= 0; i-- {
		unit := units[i]
		if len(unit.indexes) == 0 || used+len(unit.indexes) > limit {
			continue
		}
		for _, idx := range unit.indexes {
			selected[idx] = true
		}
		used += len(unit.indexes)
	}
	return selected
}

func replayTraceToolCallID(event *Event) string {
	if event == nil {
		return ""
	}
	if toolPayload := EventToolProjection(event); toolPayload != nil {
		if id := strings.TrimSpace(toolPayload.ID); id != "" {
			return id
		}
	}
	if update := ProtocolUpdateOf(event); update != nil {
		if id := strings.TrimSpace(update.ToolCallID); id != "" {
			return id
		}
	}
	return ""
}

func eventTurnID(event *Event) string {
	if event == nil || event.Scope == nil {
		return ""
	}
	return strings.TrimSpace(event.Scope.TurnID)
}
