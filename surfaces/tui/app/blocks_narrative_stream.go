package tuiapp

import (
	"strings"
	"time"
)

// narrativeSourceIdentity is transient Surface reducer state. MessageID is the
// canonical ACP message identity when present. SourceEventID and
// SourceProjectionID are retained for diagnostics but are not merge keys:
// every delta has a distinct transport event identity. Live deltas, with or
// without MessageID, are correlated only inside the current presentation run.
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
	return ""
}

type narrativeStreamTarget struct {
	epoch    uint64
	segment  uint64
	kind     SubagentEventKind
	identity string
}

type narrativeStreamState struct {
	epoch          uint64
	segment        uint64
	activeKind     SubagentEventKind
	activeIdentity string
	active         bool
	targets        map[narrativeStreamTarget]int
	pending        map[narrativeStreamTarget]string
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
	// attempt_reset removes speculative narrative before the next render may run.
	// Preserve any exploration container that the narrative had already settled
	// so a batched reset followed by running cannot flatten it on first paint.
	b.explorationProjection.preserveBeforeEventRemoval(b.Events, b.Status)
	clearNarrativeStream(&b.Events, &b.narrativeStream)
}

// advanceNarrativeBoundary closes live append routing for every identity.
// Canonical finals can still repair their message in place, but subsequent
// deltas cannot move backward across a newly presented semantic event.
func (b *MainACPTurnBlock) advanceNarrativeBoundary() {
	if b == nil {
		return
	}
	sealNarrativeBuffers(b.Events)
	b.narrativeStream.advanceBoundary()
}

func (b *MainACPTurnBlock) advanceNarrativeBoundaryWithGap() {
	if b == nil {
		return
	}
	sealNarrativeBuffers(b.Events)
	appendNarrativeSemanticBoundary(&b.Events, &b.narrativeStream)
}

// closeNarrativeStream is reserved for a boundary that owns the complete Turn,
// such as terminal lifecycle or moving the main timeline to another Turn.
func (b *MainACPTurnBlock) closeNarrativeStream() {
	if b == nil {
		return
	}
	sealNarrativeBuffers(b.Events)
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
	sealNarrativeBuffers(b.Events)
	b.narrativeStream.advanceBoundary()
}

func (b *ParticipantTurnBlock) advanceNarrativeBoundaryWithGap() {
	if b == nil {
		return
	}
	sealNarrativeBuffers(b.Events)
	appendNarrativeSemanticBoundary(&b.Events, &b.narrativeStream)
}

func (b *ParticipantTurnBlock) closeNarrativeStream() {
	if b == nil {
		return
	}
	sealNarrativeBuffers(b.Events)
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
	clearModelRetryNotices(events)
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
	clearModelRetryNotices(events)
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
	identity := source.stableKey()
	if s.prepareRun(kind, identity) {
		sealNarrativeBuffers(events)
	}
	target := s.target(kind, identity)
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
	sealNarrativeBuffers(events)
	chunk = s.prependPending(target, chunk, false)
	events = append(events, newNarrativeEventChunk(kind, chunk, at))
	bindNarrativeTarget(&events[len(events)-1], target, false)
	s.rememberTarget(target, len(events)-1)
	return events
}

func (s *narrativeStreamState) replaceFinal(events []SubagentEvent, kind SubagentEventKind, chunk string, source narrativeSourceIdentity, at time.Time) []SubagentEvent {
	identity := source.stableKey()
	if identity != "" && renderableTextHasContent(chunk) {
		// A final is a snapshot repair, not a live delta. Keep its latest typed
		// owner even when another run is active; never reopen that owner for
		// append or disturb the current run's pending prefix.
		if idx, ok := s.finalTargetIndex(events, kind, identity); ok {
			replaceNarrativeEventFinal(&events[idx], normalizeNarrativeLineEndings(chunk), at)
			events[idx].narrativeFinal = true
			return events
		}
	}
	// Only an adjacent anonymous run of the same kind can be promoted by a
	// typed final. All other new finals establish their own live boundary.
	adoptAnonymous := identity != "" && s.active && s.activeIdentity == "" && s.activeKind == kind
	if !adoptAnonymous && s.prepareRun(kind, identity) {
		sealNarrativeBuffers(events)
	}
	if adoptAnonymous {
		s.activeIdentity = identity
	}
	target := s.target(kind, identity)
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
	ev := SubagentEvent{Kind: kind}
	replaceNarrativeEventFinal(&ev, chunk, at)
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

func (s *narrativeStreamState) finalTargetIndex(events []SubagentEvent, kind SubagentEventKind, identity string) (int, bool) {
	for idx := len(events) - 1; idx >= 0; idx-- {
		event := events[idx]
		target := event.narrativeTarget
		if event.narrativeTracked && target.epoch == s.epoch && target.kind == kind && target.identity == identity {
			return idx, true
		}
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
	return narrativeStreamTarget{epoch: s.epoch, segment: s.segment, kind: kind, identity: identity}
}

// prepareRun confines live deltas to a contiguous output kind and identity,
// including agents that reuse one MessageID across multiple model steps.
func (s *narrativeStreamState) prepareRun(kind SubagentEventKind, identity string) bool {
	if s == nil || !activeNarrativeEventKind(kind) {
		return false
	}
	changed := s.active && (s.activeKind != kind || s.activeIdentity != identity)
	if changed {
		s.advanceBoundary()
	}
	s.activeKind = kind
	s.activeIdentity = identity
	s.active = true
	return changed
}

func (s *narrativeStreamState) advanceBoundary() {
	if s == nil {
		return
	}
	s.active = false
	s.activeIdentity = ""
	s.segment++
	clear(s.targets)
	clear(s.pending)
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
		if ev.ActiveBuffer != nil && activeNarrativeEventKind(ev.Kind) && !ev.narrativeFinal {
			continue
		}
		if !ev.narrativeFinal {
			ev.ActiveBuffer = nil
		}
		out = append(out, ev)
	}
	clear(events[len(out):])
	return out
}

func sealNarrativeBuffers(events []SubagentEvent) bool {
	changed := false
	for i := range events {
		if !activeNarrativeEventKind(events[i].Kind) || events[i].ActiveBuffer == nil {
			continue
		}
		changed = events[i].ActiveBuffer.Seal() || changed
	}
	return changed
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
