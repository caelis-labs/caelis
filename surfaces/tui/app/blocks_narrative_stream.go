package tuiapp

import (
	"strings"
	"time"
)

type narrativeStreamState struct {
	pending pendingNarrativePrefix
}

func (b *MainACPTurnBlock) AppendStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	b.Events = b.narrativeStream.append(b.Events, kind, chunk, narrativeEventTime(occurredAt...))
}

func (b *MainACPTurnBlock) ReplaceFinalStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	b.Events = b.narrativeStream.replaceFinal(b.Events, kind, chunk, narrativeEventTime(occurredAt...))
}

func (b *MainACPTurnBlock) ClearActiveBuffers() {
	if b == nil {
		return
	}
	b.onNarrativeBarrier()
	b.Events = clearActiveNarrativeBuffers(b.Events)
}

func (b *MainACPTurnBlock) onNarrativeBarrier() {
	if b == nil {
		return
	}
	b.narrativeStream.clearPending()
}

func (b *ParticipantTurnBlock) AppendStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	b.Events = b.narrativeStream.append(b.Events, kind, chunk, narrativeEventTime(occurredAt...))
}

func (b *ParticipantTurnBlock) ReplaceFinalStreamChunk(kind SubagentEventKind, chunk string, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	b.Events = b.narrativeStream.replaceFinal(b.Events, kind, chunk, narrativeEventTime(occurredAt...))
}

func (b *ParticipantTurnBlock) onNarrativeBarrier() {
	if b == nil {
		return
	}
	b.narrativeStream.clearPending()
}

func (s *narrativeStreamState) append(events []SubagentEvent, kind SubagentEventKind, chunk string, at time.Time) []SubagentEvent {
	if idx := latestNarrativeAppendTargetIndex(events, kind); idx >= 0 {
		chunk = s.pending.takeAndPrepend(kind, chunk)
		appendNarrativeEventChunk(&events[idx], kind, chunk, at, appendDeltaStreamChunk)
		return events
	}
	if !shouldStartNarrativeStreamEvent(kind, chunk) {
		s.pending.append(kind, chunk)
		return events
	}
	chunk = s.pending.takeAndPrepend(kind, chunk)
	return append(events, newNarrativeEventChunk(kind, chunk, at))
}

func (s *narrativeStreamState) replaceFinal(events []SubagentEvent, kind SubagentEventKind, chunk string, at time.Time) []SubagentEvent {
	chunk = s.pending.takeAndMergeFinal(kind, chunk)
	if !renderableTextHasContent(chunk) {
		return events
	}
	chunk = collapseRepeatedNarrativeText(chunk)
	if cumulativeFinalNarrativeAlreadyRendered(events, kind, chunk) {
		return events
	}
	if idx := latestNarrativeFinalTargetIndex(events, kind); idx >= 0 {
		chunk = cumulativeFinalNarrativeTimelineText(events, kind, chunk, idx)
		if !renderableTextHasContent(chunk) {
			return events
		}
		replaceNarrativeEventFinal(&events[idx], chunk, at)
		return pruneNarrativeEventsCoveredByFinal(events, idx, kind)
	}
	chunk = cumulativeFinalNarrativeTimelineText(events, kind, chunk, len(events))
	if !renderableTextHasContent(chunk) {
		return events
	}
	ev := SubagentEvent{Kind: kind, Text: chunk}
	markNarrativeTiming(&ev, at)
	events = append(events, ev)
	return pruneNarrativeEventsCoveredByFinal(events, len(events)-1, kind)
}

func (s *narrativeStreamState) clearPending() {
	if s == nil {
		return
	}
	s.pending.clear()
}

func clearActiveNarrativeBuffers(events []SubagentEvent) []SubagentEvent {
	out := events[:0]
	for _, ev := range events {
		if ev.ActiveBuffer != nil && activeNarrativeEventKind(ev.Kind) {
			continue
		}
		ev.ActiveBuffer = nil
		out = append(out, ev)
	}
	clear(events[len(out):])
	return out
}

func activeNarrativeEventKind(kind SubagentEventKind) bool {
	return kind == SEAssistant || kind == SEReasoning
}

type pendingNarrativePrefix struct {
	assistant string
	reasoning string
}

func (p *pendingNarrativePrefix) append(kind SubagentEventKind, chunk string) {
	if p == nil || chunk == "" || !activeNarrativeEventKind(kind) {
		return
	}
	switch kind {
	case SEAssistant:
		p.assistant = appendDeltaStreamChunk(p.assistant, chunk)
	case SEReasoning:
		p.reasoning = appendDeltaStreamChunk(p.reasoning, chunk)
	}
}

func (p *pendingNarrativePrefix) takeAndPrepend(kind SubagentEventKind, chunk string) string {
	return p.takeAndMerge(kind, chunk, false)
}

func (p *pendingNarrativePrefix) takeAndMergeFinal(kind SubagentEventKind, chunk string) string {
	return p.takeAndMerge(kind, chunk, true)
}

func (p *pendingNarrativePrefix) takeAndMerge(kind SubagentEventKind, chunk string, final bool) string {
	if p == nil || !activeNarrativeEventKind(kind) {
		return chunk
	}
	prefix := ""
	switch kind {
	case SEAssistant:
		prefix = p.assistant
		p.assistant = ""
	case SEReasoning:
		prefix = p.reasoning
		p.reasoning = ""
	}
	if prefix == "" {
		return chunk
	}
	if final && strings.HasPrefix(chunk, prefix) {
		return chunk
	}
	return appendDeltaStreamChunk(prefix, chunk)
}

func (p *pendingNarrativePrefix) clear() {
	if p == nil {
		return
	}
	*p = pendingNarrativePrefix{}
}

func shouldStartNarrativeStreamEvent(kind SubagentEventKind, chunk string) bool {
	if !activeNarrativeEventKind(kind) {
		return true
	}
	return renderableTextHasContent(chunk)
}
