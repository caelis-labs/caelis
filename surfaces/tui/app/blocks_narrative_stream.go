package tuiapp

import (
	"strings"
	"time"
)

// narrativeSourceIdentity is transient Surface reducer state. MessageID is the
// canonical ACP message identity when present; SourceEventID is the durable
// fallback. SourceProjectionID is retained for diagnostics but is not a merge
// key because sibling projections of one source event may have different
// semantics.
type narrativeSourceIdentity struct {
	MessageID          string
	SourceEventID      string
	SourceProjectionID string
}

func newNarrativeSourceIdentity(messageID, sourceEventID, sourceProjectionID string) narrativeSourceIdentity {
	return narrativeSourceIdentity{
		MessageID:          strings.TrimSpace(messageID),
		SourceEventID:      strings.TrimSpace(sourceEventID),
		SourceProjectionID: strings.TrimSpace(sourceProjectionID),
	}
}

func (s narrativeSourceIdentity) stableKey() string {
	if messageID := strings.TrimSpace(s.MessageID); messageID != "" {
		return "message:" + messageID
	}
	if eventID := strings.TrimSpace(s.SourceEventID); eventID != "" {
		return "event:" + eventID
	}
	return ""
}

type narrativeStreamTarget struct {
	segment  uint64
	kind     SubagentEventKind
	identity string
}

type narrativeStreamIdentity struct {
	kind     SubagentEventKind
	identity string
}

type narrativeStreamState struct {
	segment      uint64
	targets      map[narrativeStreamTarget]int
	pending      map[narrativeStreamTarget]string
	sealedPrefix map[narrativeStreamIdentity]string
}

func (b *MainACPTurnBlock) AppendStreamEvent(kind SubagentEventKind, chunk string, source narrativeSourceIdentity, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	appendNarrativeStreamEvent(&b.Events, &b.narrativeStream, kind, chunk, source, narrativeEventTime(occurredAt...))
}

func (b *MainACPTurnBlock) ReplaceFinalStreamEvent(kind SubagentEventKind, chunk string, source narrativeSourceIdentity, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	replaceFinalNarrativeStreamEvent(&b.Events, &b.narrativeStream, kind, chunk, source, narrativeEventTime(occurredAt...))
}

func (b *MainACPTurnBlock) ClearActiveBuffers() {
	if b == nil {
		return
	}
	clearNarrativeStream(&b.Events, &b.narrativeStream)
}

func (b *MainACPTurnBlock) sealNarrativeSegment() {
	if b == nil {
		return
	}
	b.narrativeStream.sealSegment(b.Events)
}

func (b *MainACPTurnBlock) sealNarrativeSegmentWithGap() {
	if b == nil {
		return
	}
	appendNarrativeSemanticBoundary(&b.Events, &b.narrativeStream)
}

func (b *ParticipantTurnBlock) AppendStreamEvent(kind SubagentEventKind, chunk string, source narrativeSourceIdentity, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	appendNarrativeStreamEvent(&b.Events, &b.narrativeStream, kind, chunk, source, narrativeEventTime(occurredAt...))
}

func (b *ParticipantTurnBlock) ReplaceFinalStreamEvent(kind SubagentEventKind, chunk string, source narrativeSourceIdentity, occurredAt ...time.Time) {
	if b == nil {
		return
	}
	replaceFinalNarrativeStreamEvent(&b.Events, &b.narrativeStream, kind, chunk, source, narrativeEventTime(occurredAt...))
}

func (b *ParticipantTurnBlock) sealNarrativeSegment() {
	if b == nil {
		return
	}
	b.narrativeStream.sealSegment(b.Events)
}

func (b *ParticipantTurnBlock) sealNarrativeSegmentWithGap() {
	if b == nil {
		return
	}
	appendNarrativeSemanticBoundary(&b.Events, &b.narrativeStream)
}

func appendNarrativeStreamEvent(
	events *[]SubagentEvent,
	stream *narrativeStreamState,
	kind SubagentEventKind,
	chunk string,
	source narrativeSourceIdentity,
	at time.Time,
) {
	*events = stream.append(*events, kind, chunk, source, at)
}

func replaceFinalNarrativeStreamEvent(
	events *[]SubagentEvent,
	stream *narrativeStreamState,
	kind SubagentEventKind,
	chunk string,
	source narrativeSourceIdentity,
	at time.Time,
) {
	*events = stream.replaceFinal(*events, kind, chunk, source, at)
}

func clearNarrativeStream(events *[]SubagentEvent, stream *narrativeStreamState) {
	stream.reset()
	*events = clearActiveNarrativeBuffers(*events)
}

func appendNarrativeSemanticBoundary(events *[]SubagentEvent, stream *narrativeStreamState) {
	stream.sealSegment(*events)
	if len(*events) > 0 && (*events)[len(*events)-1].Kind == SESemanticBoundary {
		return
	}
	*events = append(*events, SubagentEvent{Kind: SESemanticBoundary})
}

func (s *narrativeStreamState) append(events []SubagentEvent, kind SubagentEventKind, chunk string, source narrativeSourceIdentity, at time.Time) []SubagentEvent {
	target := s.target(kind, source.stableKey())
	if idx, ok := s.targetIndex(events, target); ok {
		chunk = s.prependPending(target, chunk, false)
		appendNarrativeEventChunk(&events[idx], kind, chunk, at, appendDeltaStreamChunk)
		return events
	}
	if !shouldStartNarrativeStreamEvent(kind, chunk) {
		s.appendPending(target, chunk)
		return events
	}
	chunk = s.prependPending(target, chunk, false)
	events = append(events, newNarrativeEventChunk(kind, chunk, at))
	s.rememberTarget(target, len(events)-1)
	return events
}

func (s *narrativeStreamState) replaceFinal(events []SubagentEvent, kind SubagentEventKind, chunk string, source narrativeSourceIdentity, at time.Time) []SubagentEvent {
	target := s.target(kind, source.stableKey())
	chunk = s.prependPending(target, chunk, true)
	if !renderableTextHasContent(chunk) {
		return events
	}
	chunk = normalizeNarrativeLineEndings(chunk)
	if idx, ok := s.targetIndex(events, target); ok {
		var cumulative bool
		chunk, cumulative = s.currentSegmentFinal(target, chunk)
		if cumulative && !renderableTextHasContent(chunk) {
			return s.removeTargetEvent(events, idx)
		}
		replaceNarrativeEventFinal(&events[idx], chunk, at)
		return events
	}
	// A canonical final snapshot may adopt an anonymous provisional stream only
	// inside the current semantic segment. Once sealSegment has run, the
	// anonymous target is no longer reachable and cannot be rewritten.
	anonymous := s.target(kind, "")
	if target.identity != "" {
		if idx, ok := s.targetIndex(events, anonymous); ok {
			var cumulative bool
			chunk, cumulative = s.currentSegmentFinal(target, chunk)
			if cumulative && !renderableTextHasContent(chunk) {
				return s.removeTargetEvent(events, idx)
			}
			replaceNarrativeEventFinal(&events[idx], chunk, at)
			delete(s.targets, anonymous)
			s.rememberTarget(target, idx)
			return events
		}
	}
	if current, cumulative := s.currentSegmentFinal(target, chunk); cumulative {
		if !renderableTextHasContent(current) {
			return events
		}
		chunk = current
	}
	ev := SubagentEvent{Kind: kind, Text: chunk}
	markNarrativeTiming(&ev, at)
	events = append(events, ev)
	s.rememberTarget(target, len(events)-1)
	return events
}

func (s *narrativeStreamState) targetIndex(events []SubagentEvent, target narrativeStreamTarget) (int, bool) {
	if s == nil {
		return 0, false
	}
	idx, ok := s.targets[target]
	if !ok || idx < 0 || idx >= len(events) || events[idx].Kind != target.kind {
		if ok {
			delete(s.targets, target)
		}
		return 0, false
	}
	return idx, true
}

func (s *narrativeStreamState) removeTargetEvent(events []SubagentEvent, idx int) []SubagentEvent {
	if s == nil || idx < 0 || idx >= len(events) {
		return events
	}
	copy(events[idx:], events[idx+1:])
	clear(events[len(events)-1:])
	events = events[:len(events)-1]
	for target, targetIndex := range s.targets {
		switch {
		case targetIndex == idx:
			delete(s.targets, target)
		case targetIndex > idx:
			s.targets[target] = targetIndex - 1
		}
	}
	return events
}

func (s *narrativeStreamState) rememberTarget(target narrativeStreamTarget, idx int) {
	if s == nil {
		return
	}
	if s.targets == nil {
		s.targets = make(map[narrativeStreamTarget]int)
	}
	s.targets[target] = idx
}

func (s *narrativeStreamState) appendPending(target narrativeStreamTarget, chunk string) {
	if s == nil || chunk == "" || !activeNarrativeEventKind(target.kind) {
		return
	}
	if s.pending == nil {
		s.pending = make(map[narrativeStreamTarget]string)
	}
	s.pending[target] = appendDeltaStreamChunk(s.pending[target], chunk)
}

func (s *narrativeStreamState) takePending(target narrativeStreamTarget, allowAnonymous bool) string {
	if s == nil || s.pending == nil {
		return ""
	}
	prefix := s.pending[target]
	delete(s.pending, target)
	if prefix == "" && allowAnonymous && target.identity != "" {
		anonymous := s.target(target.kind, "")
		prefix = s.pending[anonymous]
		delete(s.pending, anonymous)
	}
	return prefix
}

func (s *narrativeStreamState) prependPending(target narrativeStreamTarget, chunk string, final bool) string {
	prefix := s.takePending(target, final)
	if prefix == "" {
		return chunk
	}
	if final && strings.HasPrefix(chunk, prefix) {
		return chunk
	}
	return appendDeltaStreamChunk(prefix, chunk)
}

func (s *narrativeStreamState) target(kind SubagentEventKind, identity string) narrativeStreamTarget {
	if s == nil {
		return narrativeStreamTarget{kind: kind, identity: identity}
	}
	return narrativeStreamTarget{segment: s.segment, kind: kind, identity: identity}
}

// currentSegmentFinal accepts cumulative final snapshots only when stable ACP
// identity proves that the final belongs to a narrative spanning semantic
// segments. The complete sealed prefix must match byte-for-byte at offset zero.
// Identity-free or divergent snapshots fail closed and remain intact.
// cumulative reports whether the prefix matched even when the current-segment
// suffix is empty, so callers can remove a provisional event or avoid creating
// a duplicate structural event.
func (s *narrativeStreamState) currentSegmentFinal(target narrativeStreamTarget, finalText string) (text string, cumulative bool) {
	if s == nil || target.identity == "" {
		return finalText, false
	}
	sealedPrefix := s.sealedPrefix[narrativeStreamIdentity{
		kind:     target.kind,
		identity: target.identity,
	}]
	if !renderableTextHasContent(sealedPrefix) || !strings.HasPrefix(finalText, sealedPrefix) {
		return finalText, false
	}
	suffix := strings.TrimLeft(finalText[len(sealedPrefix):], " \t\r\n")
	return suffix, true
}

func (s *narrativeStreamState) sealSegment(events []SubagentEvent) {
	if s == nil {
		return
	}
	if len(s.targets) > 0 {
		if s.sealedPrefix == nil {
			s.sealedPrefix = make(map[narrativeStreamIdentity]string)
		}
		for target, idx := range s.targets {
			if target.identity == "" || idx < 0 || idx >= len(events) {
				continue
			}
			event := events[idx]
			if event.Kind != target.kind || !activeNarrativeEventKind(event.Kind) || !renderableTextHasContent(event.Text) {
				continue
			}
			identity := narrativeStreamIdentity{kind: target.kind, identity: target.identity}
			s.sealedPrefix[identity] += event.Text
		}
	}
	s.segment++
	s.targets = nil
	s.pending = nil
}

func (s *narrativeStreamState) reset() {
	if s == nil {
		return
	}
	*s = narrativeStreamState{segment: s.segment + 1}
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

func shouldStartNarrativeStreamEvent(kind SubagentEventKind, chunk string) bool {
	if !activeNarrativeEventKind(kind) {
		return true
	}
	return renderableTextHasContent(chunk)
}
