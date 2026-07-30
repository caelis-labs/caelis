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
	epoch    uint64
	segment  uint64
	kind     SubagentEventKind
	identity string
}

type narrativeStreamState struct {
	epoch   uint64
	segment uint64
	targets map[narrativeStreamTarget]int
	pending map[narrativeStreamTarget]string
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

// advanceNarrativeBoundary closes only identity-free streams. A typed ACP
// MessageID or source EventID owns its narrative event across unrelated tool,
// plan, approval, and notice mutations; those events cannot close another
// message.
func (b *MainACPTurnBlock) advanceNarrativeBoundary() {
	if b == nil {
		return
	}
	b.narrativeStream.advanceBoundary()
}

func (b *MainACPTurnBlock) advanceNarrativeBoundaryWithGap() {
	if b == nil {
		return
	}
	appendNarrativeSemanticBoundary(&b.Events, &b.narrativeStream)
}

// closeNarrativeStream is reserved for a boundary that owns the complete Turn,
// such as terminal lifecycle or moving the main timeline to another Turn.
func (b *MainACPTurnBlock) closeNarrativeStream() {
	if b == nil {
		return
	}
	b.narrativeStream.reset()
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

func (b *ParticipantTurnBlock) advanceNarrativeBoundary() {
	if b == nil {
		return
	}
	b.narrativeStream.advanceBoundary()
}

func (b *ParticipantTurnBlock) advanceNarrativeBoundaryWithGap() {
	if b == nil {
		return
	}
	appendNarrativeSemanticBoundary(&b.Events, &b.narrativeStream)
}

func (b *ParticipantTurnBlock) closeNarrativeStream() {
	if b == nil {
		return
	}
	b.narrativeStream.reset()
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
	stream.advanceBoundary()
	if len(*events) > 0 && (*events)[len(*events)-1].Kind == SESemanticBoundary {
		return
	}
	*events = append(*events, SubagentEvent{Kind: SESemanticBoundary})
}

func (s *narrativeStreamState) append(events []SubagentEvent, kind SubagentEventKind, chunk string, source narrativeSourceIdentity, at time.Time) []SubagentEvent {
	target := s.target(kind, source.stableKey())
	if idx, ok := s.targetIndex(events, target); ok {
		if events[idx].narrativeFinal {
			return events
		}
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
	bindNarrativeTarget(&events[len(events)-1], target, false)
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
		replaceNarrativeEventFinal(&events[idx], chunk, at)
		bindNarrativeTarget(&events[idx], target, true)
		return events
	}
	// A canonical final snapshot may adopt an anonymous provisional stream only
	// inside the current anonymous segment. Once a presentation boundary advances
	// that segment, the anonymous target is no longer reachable and cannot be
	// rewritten.
	anonymous := s.target(kind, "")
	if target.identity != "" {
		if idx, ok := s.targetIndex(events, anonymous); ok {
			replaceNarrativeEventFinal(&events[idx], chunk, at)
			delete(s.targets, anonymous)
			bindNarrativeTarget(&events[idx], target, true)
			s.rememberTarget(target, idx)
			return events
		}
	}
	ev := SubagentEvent{Kind: kind, Text: chunk}
	markNarrativeTiming(&ev, at)
	bindNarrativeTarget(&ev, target, true)
	events = append(events, ev)
	s.rememberTarget(target, len(events)-1)
	return events
}

func (s *narrativeStreamState) targetIndex(events []SubagentEvent, target narrativeStreamTarget) (int, bool) {
	if s == nil {
		return 0, false
	}
	idx, ok := s.targets[target]
	if ok && validNarrativeTargetEvent(events, idx, target) {
		return idx, true
	}
	for index := range events {
		if validNarrativeTargetEvent(events, index, target) {
			s.rememberTarget(target, index)
			return index, true
		}
	}
	if ok {
		delete(s.targets, target)
	}
	return 0, false
}

func validNarrativeTargetEvent(events []SubagentEvent, idx int, target narrativeStreamTarget) bool {
	if idx < 0 || idx >= len(events) {
		return false
	}
	event := events[idx]
	return event.narrativeTracked && event.Kind == target.kind && event.narrativeTarget == target
}

func bindNarrativeTarget(event *SubagentEvent, target narrativeStreamTarget, final bool) {
	if event == nil {
		return
	}
	event.narrativeTarget = target
	event.narrativeTracked = true
	event.narrativeFinal = final
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
	if strings.TrimSpace(identity) != "" {
		// Stable typed identity is the message boundary. It is deliberately not
		// scoped by the anonymous presentation segment.
		return narrativeStreamTarget{epoch: s.epoch, kind: kind, identity: identity}
	}
	return narrativeStreamTarget{epoch: s.epoch, segment: s.segment, kind: kind, identity: identity}
}

func (s *narrativeStreamState) advanceBoundary() {
	if s == nil {
		return
	}
	s.segment++
	for target := range s.targets {
		if target.identity == "" {
			delete(s.targets, target)
		}
	}
	for target := range s.pending {
		if target.identity == "" {
			delete(s.pending, target)
		}
	}
}

func (s *narrativeStreamState) reset() {
	if s == nil {
		return
	}
	*s = narrativeStreamState{epoch: s.epoch + 1, segment: s.segment + 1}
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
