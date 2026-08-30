package runtime

import (
	"container/list"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

const (
	subagentSemanticUnitCap       = 1024
	subagentSemanticUnitByteCap   = 256 * 1024
	subagentSemanticHeadByteCap   = 64 * 1024
	subagentSemanticTailByteCap   = subagentSemanticUnitByteCap - subagentSemanticHeadByteCap
	subagentSemanticUnitOverhead  = 256
	subagentSemanticPriorityCount = 6
)

type subagentSemanticPriority uint8

const (
	subagentSemanticNoise subagentSemanticPriority = iota
	subagentSemanticReasoning
	subagentSemanticTool
	subagentSemanticAssistant
	subagentSemanticTerminal
	// Completed Final Messages are the last semantic units eligible for
	// eviction. The latest Final remains separately protected by the Task result
	// contract; older Finals consume the shared semantic byte budget and leave
	// only after every lower-priority context unit has been removed.
	subagentSemanticFinal
)

type subagentSemanticText struct {
	full      []byte
	head      []byte
	tail      []byte
	tailStart int
	tailLen   int
	total     int64
	compact   bool
}

func (t *subagentSemanticText) append(data []byte) {
	if len(data) == 0 {
		return
	}
	t.total += int64(len(data))
	if !t.compact && len(t.full)+len(data) <= subagentSemanticUnitByteCap {
		t.full = append(t.full, data...)
		return
	}
	if !t.compact {
		previous := t.full
		t.full = nil
		t.compact = true
		t.appendCompacted(previous)
	}
	t.appendCompacted(data)
}

func (t *subagentSemanticText) appendCompacted(data []byte) {
	if len(data) == 0 {
		return
	}
	if remaining := subagentSemanticHeadByteCap - len(t.head); remaining > 0 {
		if t.head == nil {
			t.head = make([]byte, 0, subagentSemanticHeadByteCap)
		}
		keep := min(remaining, len(data))
		t.head = append(t.head, data[:keep]...)
		data = data[keep:]
	}
	if len(data) == 0 || subagentSemanticTailByteCap == 0 {
		return
	}
	if t.tail == nil {
		t.tail = make([]byte, subagentSemanticTailByteCap)
	}
	if len(data) >= len(t.tail) {
		copy(t.tail, data[len(data)-len(t.tail):])
		t.tailStart = 0
		t.tailLen = len(t.tail)
		return
	}
	if overflow := t.tailLen + len(data) - len(t.tail); overflow > 0 {
		t.tailStart = (t.tailStart + overflow) % len(t.tail)
		t.tailLen -= overflow
	}
	writeAt := (t.tailStart + t.tailLen) % len(t.tail)
	first := min(len(data), len(t.tail)-writeAt)
	copy(t.tail[writeAt:], data[:first])
	copy(t.tail, data[first:])
	t.tailLen += len(data)
}

func (t *subagentSemanticText) reset() {
	t.full = nil
	t.head = nil
	t.tail = nil
	t.tailStart = 0
	t.tailLen = 0
	t.total = 0
	t.compact = false
}

func (t *subagentSemanticText) allocatedBytes() int {
	if !t.compact {
		return cap(t.full)
	}
	return cap(t.head) + len(t.tail)
}

func (t *subagentSemanticText) materialize() string {
	if !t.compact {
		return string(t.full)
	}
	omitted := max(t.total-int64(len(t.head)+t.tailLen), 0)
	marker := []byte(fmt.Sprintf("\n\n[... %d bytes of transient output omitted ...]\n\n", omitted))
	out := make([]byte, 0, len(t.head)+len(marker)+t.tailLen)
	out = append(out, t.head...)
	out = append(out, marker...)
	if t.tailLen > 0 {
		first := min(t.tailLen, len(t.tail)-t.tailStart)
		out = append(out, t.tail[t.tailStart:t.tailStart+first]...)
		out = append(out, t.tail[:t.tailLen-first]...)
	}
	return string(out)
}

type subagentSemanticUnit struct {
	key           string
	turnID        string
	assistantTurn string
	priority      subagentSemanticPriority
	order         int64
	frame         stream.Frame
	frameBytes    int
	toolUpdate    *session.ProtocolUpdate
	toolBytes     int
	text          subagentSemanticText
	narrative     bool
	latestFinal   bool
	orderElement  *list.Element
	bucketElement *list.Element
}

func (u *subagentSemanticUnit) allocatedBytes() int {
	if u == nil {
		return 0
	}
	if u.latestFinal {
		return 0
	}
	return subagentSemanticUnitOverhead + len(u.key) + u.frameBytes + u.toolBytes + u.text.allocatedBytes()
}

func (u *subagentSemanticUnit) materialize() stream.Frame {
	if u == nil {
		return stream.Frame{}
	}
	frame := stream.CloneFrame(u.frame)
	if u.narrative {
		frame = subagentSemanticFrameWithText(frame, u.text.materialize())
	}
	return frame
}

type subagentSemanticRetention struct {
	bytes             int
	barrier           uint64
	narrativeRunKey   string
	narrativeRunTurn  string
	narrativeIdentity string
	narrativePriority subagentSemanticPriority
	narrativeSeq      uint64
	units             map[string]*subagentSemanticUnit
	order             *list.List
	buckets           [subagentSemanticPriorityCount]*list.List
	assistantKeys     map[string]map[string]struct{}
}

type subagentSemanticDescriptor struct {
	key           string
	turnID        string
	assistantTurn string
	priority      subagentSemanticPriority
	identity      string
	text          string
	narrative     bool
	replace       bool
	mergeTool     bool
	skip          bool
}

func (r *subagentSemanticRetention) observe(frame stream.Frame, order int64) {
	if r == nil {
		return
	}
	r.ensure()
	frame = subagentSemanticDisplayFrame(frame)
	descriptor := r.describe(frame, order)
	if descriptor.skip || descriptor.key == "" {
		return
	}
	if descriptor.narrative {
		// ACP MessageID identifies a message, but it does not authorize moving a
		// later delta back across another message or semantic event. Retain one
		// unit per contiguous wire-order run so current-state recovery preserves
		// the same ordering as exact delivery. An anonymous chunk followed by a
		// producer identity is promotion of the active message, not a barrier.
		base := descriptor.key
		continueRun := r.narrativeRunKey != "" &&
			r.narrativeRunTurn == descriptor.turnID &&
			r.narrativePriority == descriptor.priority &&
			(descriptor.identity == "" || r.narrativeIdentity == "" ||
				descriptor.identity == r.narrativeIdentity)
		if !continueRun {
			r.narrativeSeq++
			r.narrativeRunKey = fmt.Sprintf("%s:run:%d", base, r.narrativeSeq)
			r.narrativeRunTurn = descriptor.turnID
			r.narrativeIdentity = descriptor.identity
			r.narrativePriority = descriptor.priority
		} else if descriptor.identity != "" {
			r.narrativeIdentity = descriptor.identity
		}
		descriptor.key = r.narrativeRunKey
	} else {
		r.barrier++
		r.narrativeRunKey = ""
		r.narrativeRunTurn = ""
		r.narrativeIdentity = ""
	}
	r.observeDescriptor(frame, order, descriptor)
}

func (r *subagentSemanticRetention) protectLatestFinal(turnID string, order int64) {
	if r == nil || turnID == "" {
		return
	}
	r.ensure()
	key := "final:" + turnID
	if unit := r.units[key]; unit != nil {
		if unit.bucketElement != nil {
			r.buckets[unit.priority].Remove(unit.bucketElement)
			unit.bucketElement = nil
		}
		r.bytes -= unit.allocatedBytes()
		r.placeByOrder(unit, order)
		unit.frame = stream.Frame{}
		unit.frameBytes = 0
		unit.text.reset()
		unit.latestFinal = true
		return
	}
	unit := &subagentSemanticUnit{
		key: key, turnID: turnID, priority: subagentSemanticFinal,
		order: order, latestFinal: true,
	}
	r.units[key] = unit
	unit.orderElement = r.order.PushBack(unit)
	r.evict()
}

// archiveLatestFinal moves the previous Task-result-protected Final into the
// bounded semantic view without changing its chronological position. Final
// text remains exact: per-unit head/tail compaction is for transient narrative,
// not completed user-visible answers.
func (r *subagentSemanticRetention) archiveLatestFinal(frame stream.Frame, order int64) {
	if r == nil || frame.Event == nil {
		return
	}
	turnID := subagentFrameTurnID(frame)
	if turnID == "" {
		return
	}
	r.ensure()
	key := "final:" + turnID
	unit := r.units[key]
	if unit == nil {
		unit = &subagentSemanticUnit{
			key: key, turnID: turnID, priority: subagentSemanticFinal, order: order,
		}
		r.units[key] = unit
		r.insertByOrder(unit)
	} else {
		r.bytes -= unit.allocatedBytes()
		if unit.bucketElement != nil {
			r.buckets[unit.priority].Remove(unit.bucketElement)
			unit.bucketElement = nil
		}
		r.placeByOrder(unit, order)
	}
	unit.priority = subagentSemanticFinal
	unit.latestFinal = false
	unit.narrative = false
	unit.frame = stream.CloneFrame(frame)
	unit.frameBytes = subagentStreamFrameSize(unit.frame)
	unit.text.reset()
	unit.bucketElement = r.buckets[subagentSemanticFinal].PushBack(unit)
	r.bytes += unit.allocatedBytes()
	r.evict()
}

func (r *subagentSemanticRetention) insertByOrder(unit *subagentSemanticUnit) {
	if r == nil || unit == nil {
		return
	}
	for element := r.order.Back(); element != nil; element = element.Prev() {
		candidate, _ := element.Value.(*subagentSemanticUnit)
		if candidate != nil && candidate.order <= unit.order {
			unit.orderElement = r.order.InsertAfter(unit, element)
			return
		}
	}
	unit.orderElement = r.order.PushFront(unit)
}

func (r *subagentSemanticRetention) placeByOrder(unit *subagentSemanticUnit, order int64) {
	if r == nil || unit == nil {
		return
	}
	if unit.orderElement != nil {
		r.order.Remove(unit.orderElement)
		unit.orderElement = nil
	}
	unit.order = order
	r.insertByOrder(unit)
}

func (r *subagentSemanticRetention) observeDescriptor(frame stream.Frame, order int64, descriptor subagentSemanticDescriptor) {
	unit := r.units[descriptor.key]
	before := 0
	if unit == nil {
		unit = &subagentSemanticUnit{
			key: descriptor.key, turnID: descriptor.turnID, assistantTurn: descriptor.assistantTurn,
			priority: descriptor.priority, order: order, narrative: descriptor.narrative,
		}
		r.units[unit.key] = unit
		unit.orderElement = r.order.PushBack(unit)
		unit.bucketElement = r.buckets[unit.priority].PushBack(unit)
		if unit.assistantTurn != "" {
			keys := r.assistantKeys[unit.assistantTurn]
			if keys == nil {
				keys = map[string]struct{}{}
				r.assistantKeys[unit.assistantTurn] = keys
			}
			keys[unit.key] = struct{}{}
		}
	} else {
		before = unit.allocatedBytes()
		if unit.priority != descriptor.priority {
			r.buckets[unit.priority].Remove(unit.bucketElement)
			unit.priority = descriptor.priority
			unit.bucketElement = r.buckets[unit.priority].PushBack(unit)
		}
		if descriptor.replace {
			r.order.MoveToBack(unit.orderElement)
			r.buckets[unit.priority].MoveToBack(unit.bucketElement)
			unit.order = order
		}
	}

	if descriptor.narrative {
		if descriptor.replace {
			unit.text.reset()
		}
		unit.frame = subagentSemanticNarrativeTemplate(frame)
		unit.frameBytes = subagentStreamFrameSize(unit.frame)
		unit.text.append([]byte(descriptor.text))
	} else {
		unit.text.reset()
		if descriptor.mergeTool {
			previous := unit.frame
			if session.ProtocolUpdateOf(previous.Event) == nil && unit.toolUpdate != nil {
				previous = stream.Frame{Event: &session.Event{
					Protocol: &session.EventProtocol{Update: unit.toolUpdate},
				}}
			}
			frame = subagentMergeSemanticToolFrame(previous, frame)
		}
		unit.frame = subagentBoundSemanticFrame(frame)
		unit.frameBytes = subagentStreamFrameSize(unit.frame)
		unit.toolUpdate = nil
		unit.toolBytes = 0
		if descriptor.mergeTool && session.ProtocolUpdateOf(unit.frame.Event) == nil {
			unit.toolUpdate = subagentBoundSemanticToolUpdate(session.ProtocolUpdateOf(frame.Event))
			unit.toolBytes = subagentProtocolUpdateSize(unit.toolUpdate)
		}
	}
	r.bytes += unit.allocatedBytes() - before
	r.evict()
}

// dropLatestAssistantRun removes only the contiguous assistant run replaced by
// the Task's authoritative Final Message. Earlier ACP messages in the same Turn
// remain distinct user-visible output and must survive current-state recovery.
func (r *subagentSemanticRetention) dropLatestAssistantRun(turnID string) {
	if r == nil || strings.TrimSpace(turnID) == "" {
		return
	}
	r.ensure()
	for element := r.order.Back(); element != nil; element = element.Prev() {
		unit, _ := element.Value.(*subagentSemanticUnit)
		if unit != nil && unit.assistantTurn == turnID {
			r.remove(unit)
			return
		}
	}
}

func (r *subagentSemanticRetention) frames(
	boundary stream.Cursor,
	currentTurnID string,
	running bool,
	truncatedBefore int64,
	latestFinal stream.Frame,
	latestFinalOrder int64,
) []stream.Frame {
	if r == nil {
		if latestFinal.Event == nil {
			return nil
		}
		return []stream.Frame{subagentCurrentStateFrame(latestFinal, boundary, currentTurnID, running, truncatedBefore)}
	}
	r.ensure()
	frames := make([]stream.Frame, 0, len(r.units)+1)
	finalAppended := latestFinal.Event == nil
	appendFinal := func() {
		if finalAppended {
			return
		}
		frames = append(frames, subagentCurrentStateFrame(latestFinal, boundary, currentTurnID, running, truncatedBefore))
		finalAppended = true
	}
	for element := r.order.Front(); element != nil; element = element.Next() {
		unit, _ := element.Value.(*subagentSemanticUnit)
		if unit == nil {
			continue
		}
		if unit.latestFinal {
			appendFinal()
			continue
		}
		if !running && unit.turnID == currentTurnID && subagentSemanticTurnBoundary(unit.frame) {
			// The current Turn's raw terminal frame is derived from the Task
			// snapshot below this semantic layer. Replaying its display boundary
			// as well would duplicate one lifecycle event.
			continue
		}
		if !finalAppended && latestFinalOrder <= unit.order {
			appendFinal()
		}
		frames = append(frames, subagentCurrentStateFrame(unit.materialize(), boundary, currentTurnID, running, truncatedBefore))
	}
	appendFinal()
	return frames
}

func (r *subagentSemanticRetention) ensure() {
	if r.units == nil {
		r.units = map[string]*subagentSemanticUnit{}
	}
	if r.order == nil {
		r.order = list.New()
	}
	for index := range r.buckets {
		if r.buckets[index] == nil {
			r.buckets[index] = list.New()
		}
	}
	if r.assistantKeys == nil {
		r.assistantKeys = map[string]map[string]struct{}{}
	}
}

func (r *subagentSemanticRetention) evict() {
	for {
		bytePressure := r.bytes > subagentStreamByteCap
		unitPressure := r.contextUnitCount() > subagentSemanticUnitCap
		if !bytePressure && !unitPressure {
			return
		}
		var victim *subagentSemanticUnit
		// Historical Turn boundaries are more useful than replaceable progress
		// noise, but current narrative and tool state must survive before old
		// separators under either unit or byte pressure.
		priorities := []subagentSemanticPriority{
			subagentSemanticNoise,
			subagentSemanticTerminal,
			subagentSemanticReasoning,
			subagentSemanticTool,
			subagentSemanticAssistant,
		}
		if bytePressure {
			priorities = append(priorities, subagentSemanticFinal)
		}
		for _, priority := range priorities {
			if front := r.buckets[priority].Front(); front != nil {
				victim, _ = front.Value.(*subagentSemanticUnit)
				break
			}
		}
		if victim == nil {
			return
		}
		r.remove(victim)
	}
}

// contextUnitCount excludes exact completed Finals. The unit cap bounds
// transient structural/narrative bookkeeping; Final retention is governed by
// the shared 4 MiB byte budget so many small completed Turns are not discarded
// merely because they crossed an arbitrary frame count.
func (r *subagentSemanticRetention) contextUnitCount() int {
	if r == nil {
		return 0
	}
	count := 0
	for priority := subagentSemanticNoise; priority <= subagentSemanticTerminal; priority++ {
		if bucket := r.buckets[priority]; bucket != nil {
			count += bucket.Len()
		}
	}
	return count
}

func (r *subagentSemanticRetention) remove(unit *subagentSemanticUnit) {
	if r == nil || unit == nil {
		return
	}
	r.bytes -= unit.allocatedBytes()
	if unit.orderElement != nil {
		r.order.Remove(unit.orderElement)
	}
	if unit.bucketElement != nil {
		r.buckets[unit.priority].Remove(unit.bucketElement)
	}
	delete(r.units, unit.key)
	if unit.assistantTurn != "" {
		delete(r.assistantKeys[unit.assistantTurn], unit.key)
		if len(r.assistantKeys[unit.assistantTurn]) == 0 {
			delete(r.assistantKeys, unit.assistantTurn)
		}
	}
}

func (r *subagentSemanticRetention) describe(frame stream.Frame, order int64) subagentSemanticDescriptor {
	turnID := subagentFrameTurnID(frame)
	event := frame.Event
	if event == nil {
		if frame.Closed {
			// subagentSemanticDisplayFrame normally converts Task terminal
			// transport into a typed UI-only Turn boundary before classification.
			return subagentSemanticDescriptor{skip: true}
		}
		if frame.Text == "" {
			return subagentSemanticDescriptor{skip: true}
		}
		return subagentSemanticDescriptor{
			key: fmt.Sprintf("narrative:%s:legacy:%d", turnID, r.barrier), turnID: turnID,
			assistantTurn: turnID, priority: subagentSemanticAssistant,
			text: frame.Text, narrative: true,
		}
	}
	if event.Scope != nil && strings.TrimSpace(event.Scope.Source) == "subagent_result" {
		return subagentSemanticDescriptor{skip: true}
	}
	updateType := strings.TrimSpace(session.ProtocolSessionUpdateType(event))
	if updateType == string(session.ProtocolUpdateTypeAgentMessage) ||
		updateType == string(session.ProtocolUpdateTypeAgentThought) ||
		(updateType == "" && session.EventTypeOf(event) == session.EventTypeAssistant) {
		kind := "assistant"
		priority := subagentSemanticAssistant
		assistantTurn := turnID
		if updateType == string(session.ProtocolUpdateTypeAgentThought) {
			kind = "reasoning"
			priority = subagentSemanticReasoning
			assistantTurn = ""
		}
		identity := session.EventMessageID(event)
		keyIdentity := identity
		if keyIdentity == "" {
			keyIdentity = fmt.Sprintf("anonymous:%d", r.barrier)
		}
		return subagentSemanticDescriptor{
			key:    "narrative:" + turnID + ":" + kind + ":" + keyIdentity,
			turnID: turnID, assistantTurn: assistantTurn, priority: priority,
			identity: identity, text: session.EventText(event), narrative: true,
		}
	}

	eventID := firstNonEmpty(strings.TrimSpace(event.ID), fmt.Sprintf("event:%d", order))
	switch session.EventTypeOf(event) {
	case session.EventTypeToolCall, session.EventTypeToolResult:
		callID := subagentSemanticToolCallID(event)
		if callID == "" {
			callID = eventID
		}
		return subagentSemanticDescriptor{
			key: "tool:" + turnID + ":" + callID, turnID: turnID,
			priority: subagentSemanticTool, mergeTool: true,
		}
	case session.EventTypePlan:
		return subagentSemanticDescriptor{
			key: "plan:" + turnID, turnID: turnID,
			priority: subagentSemanticNoise, replace: true,
		}
	case session.EventTypeLifecycle:
		status := "unspecified"
		if event.Lifecycle != nil {
			status = firstNonEmpty(strings.ToLower(strings.TrimSpace(event.Lifecycle.Status)), status)
		}
		if stream.IsTerminalState(status) {
			return subagentSemanticDescriptor{
				key: "lifecycle:" + turnID + ":terminal:" + eventID, turnID: turnID,
				priority: subagentSemanticTerminal,
			}
		}
		return subagentSemanticDescriptor{
			key: "lifecycle:" + turnID + ":progress:" + status, turnID: turnID,
			priority: subagentSemanticNoise, replace: true,
		}
	case session.EventTypeParticipant, session.EventTypeHandoff:
		return subagentSemanticDescriptor{
			key: "structure:" + turnID + ":" + eventID, turnID: turnID,
			priority: subagentSemanticTerminal,
		}
	default:
		return subagentSemanticDescriptor{
			key: "event:" + turnID + ":" + eventID, turnID: turnID,
			priority: subagentSemanticNoise,
		}
	}
}

func subagentFrameTurnID(frame stream.Frame) string {
	if frame.Event != nil && frame.Event.Scope != nil {
		if turnID := strings.TrimSpace(frame.Event.Scope.TurnID); turnID != "" {
			return turnID
		}
	}
	return strings.TrimSpace(frame.Ref.TerminalID)
}

func subagentSemanticToolCallID(event *session.Event) string {
	if event == nil {
		return ""
	}
	if tool := session.EventToolProjection(event); tool != nil && tool.ID != "" {
		return tool.ID
	}
	if update := session.ProtocolUpdateOf(event); update != nil {
		return strings.TrimSpace(update.ToolCallID)
	}
	return ""
}

// subagentMergeSemanticToolFrame materializes one current-state snapshot for a
// sparse ACP tool lifecycle. Exact live delivery retains every original frame;
// this merge is used only after that bounded delta window has been crossed.
// Later fields replace earlier fields only when present, and a terminal status
// cannot be reopened by a stale progress patch.
func subagentMergeSemanticToolFrame(previous stream.Frame, current stream.Frame) stream.Frame {
	current = stream.CloneFrame(current)
	previousUpdate := session.ProtocolUpdateOf(previous.Event)
	currentUpdate := session.ProtocolUpdateOf(current.Event)
	if previousUpdate == nil || currentUpdate == nil || current.Event == nil {
		return current
	}
	previousType := strings.TrimSpace(previousUpdate.SessionUpdate)
	currentType := strings.TrimSpace(currentUpdate.SessionUpdate)
	if (previousType != string(session.ProtocolUpdateTypeToolCall) && previousType != string(session.ProtocolUpdateTypeToolUpdate)) ||
		(currentType != string(session.ProtocolUpdateTypeToolCall) && currentType != string(session.ProtocolUpdateTypeToolUpdate)) ||
		strings.TrimSpace(previousUpdate.ToolCallID) == "" ||
		strings.TrimSpace(previousUpdate.ToolCallID) != strings.TrimSpace(currentUpdate.ToolCallID) {
		return current
	}

	merged := *previousUpdate
	merged.SessionUpdate = currentUpdate.SessionUpdate
	merged.ToolCallID = currentUpdate.ToolCallID
	if currentUpdate.MessageID != "" {
		merged.MessageID = currentUpdate.MessageID
	}
	if currentUpdate.Title != "" {
		merged.Title = currentUpdate.Title
	}
	if currentUpdate.Kind != "" {
		merged.Kind = currentUpdate.Kind
	}
	if currentUpdate.Status != "" &&
		(!stream.IsTerminalState(merged.Status) || stream.IsTerminalState(currentUpdate.Status)) {
		merged.Status = currentUpdate.Status
	}
	if currentUpdate.RawInput != nil {
		merged.RawInput = currentUpdate.RawInput
	}
	if currentUpdate.RawOutput != nil {
		merged.RawOutput = currentUpdate.RawOutput
	}
	if currentUpdate.Content != nil {
		merged.Content = currentUpdate.Content
	}
	if currentUpdate.Locations != nil {
		merged.Locations = currentUpdate.Locations
	}
	if currentUpdate.Entries != nil {
		merged.Entries = currentUpdate.Entries
	}
	if currentUpdate.Meta != nil {
		merged.Meta = subagentMergeSemanticMeta(previousUpdate.Meta, currentUpdate.Meta)
	}
	protocol := session.CloneEventProtocol(*current.Event.Protocol)
	protocol.Update = &merged
	current.Event.Protocol = &protocol
	return current
}

func subagentMergeSemanticMeta(previous map[string]any, current map[string]any) map[string]any {
	if current == nil {
		return session.CloneState(previous)
	}
	merged := session.CloneState(previous)
	if merged == nil {
		merged = map[string]any{}
	}
	for key, value := range current {
		currentMap, currentIsMap := value.(map[string]any)
		previousMap, previousIsMap := merged[key].(map[string]any)
		if currentIsMap && previousIsMap {
			merged[key] = subagentMergeSemanticMeta(previousMap, currentMap)
			continue
		}
		merged[key] = session.CloneState(map[string]any{"value": value})["value"]
	}
	return merged
}

// subagentBoundSemanticToolUpdate preserves the mergeable ACP lifecycle header
// when the display frame itself is too large and becomes a bounded Notice. The
// fallback participates in the shared semantic byte budget and admits open
// payload fields only when each still fits inside the smaller header budget.
func subagentBoundSemanticToolUpdate(update *session.ProtocolUpdate) *session.ProtocolUpdate {
	if update == nil {
		return nil
	}
	protocol := session.CloneEventProtocol(session.EventProtocol{Update: update})
	cloned := protocol.Update
	if subagentProtocolUpdateSize(cloned) <= subagentSemanticHeadByteCap {
		return cloned
	}
	bounded := &session.ProtocolUpdate{
		SessionUpdate: cloned.SessionUpdate,
		ToolCallID:    cloned.ToolCallID,
		Status:        cloned.Status,
	}
	tryField := func(apply func(*session.ProtocolUpdate)) {
		candidate := *bounded
		apply(&candidate)
		if subagentProtocolUpdateSize(&candidate) <= subagentSemanticHeadByteCap {
			bounded = &candidate
		}
	}
	tryField(func(candidate *session.ProtocolUpdate) { candidate.Title = cloned.Title })
	tryField(func(candidate *session.ProtocolUpdate) { candidate.Kind = cloned.Kind })
	tryField(func(candidate *session.ProtocolUpdate) { candidate.MessageID = cloned.MessageID })
	tryField(func(candidate *session.ProtocolUpdate) { candidate.RawInput = cloned.RawInput })
	tryField(func(candidate *session.ProtocolUpdate) { candidate.Content = cloned.Content })
	tryField(func(candidate *session.ProtocolUpdate) { candidate.RawOutput = cloned.RawOutput })
	tryField(func(candidate *session.ProtocolUpdate) { candidate.Locations = cloned.Locations })
	tryField(func(candidate *session.ProtocolUpdate) { candidate.Entries = cloned.Entries })
	tryField(func(candidate *session.ProtocolUpdate) { candidate.Meta = cloned.Meta })
	return bounded
}

func subagentProtocolUpdateSize(update *session.ProtocolUpdate) int {
	if update == nil {
		return 0
	}
	raw, err := json.Marshal(update)
	if err != nil {
		return subagentSemanticHeadByteCap + 1
	}
	return len(raw)
}

func subagentSemanticNarrativeTemplate(frame stream.Frame) stream.Frame {
	frame = stream.CloneFrame(frame)
	frame.Text = ""
	if frame.Event == nil {
		return frame
	}
	frame.Event.Text = ""
	frame.Event.Message = nil
	if frame.Event.Protocol != nil {
		protocol := session.CloneEventProtocol(*frame.Event.Protocol)
		if protocol.Update != nil {
			protocol.Update.Content = nil
		}
		frame.Event.Protocol = &protocol
	}
	return frame
}

func subagentSemanticFrameWithText(frame stream.Frame, text string) stream.Frame {
	frame.Text = text
	if frame.Event == nil {
		return frame
	}
	frame.Event.Text = text
	if frame.Event.Protocol != nil {
		protocol := session.CloneEventProtocol(*frame.Event.Protocol)
		if protocol.Update != nil {
			protocol.Update.Content = session.ProtocolTextContent(text)
		}
		frame.Event.Protocol = &protocol
	}
	return frame
}

func subagentBoundSemanticFrame(frame stream.Frame) stream.Frame {
	frame = stream.CloneFrame(frame)
	size := subagentStreamFrameSize(frame)
	if size <= subagentSemanticUnitByteCap {
		return frame
	}
	raw, _ := json.Marshal(frame.Event)
	text := subagentSemanticText{}
	text.append(raw)
	turnID := subagentFrameTurnID(frame)
	eventID := ""
	actor := session.ActorRef{}
	var scope *session.EventScope
	if frame.Event != nil {
		eventID = strings.TrimSpace(frame.Event.ID)
		actor = frame.Event.Actor
		if frame.Event.Scope != nil {
			cloned := session.CloneEvent(frame.Event)
			scope = cloned.Scope
		}
	}
	frame.Text = ""
	frame.Event = &session.Event{
		ID: eventID, Type: session.EventTypeNotice, Visibility: session.VisibilityUIOnly,
		Actor: actor, Scope: scope,
		Notice: &session.EventNotice{
			Level: "info", Kind: "task_stream_oversized_semantic_unit",
			Text: fmt.Sprintf("Oversized transient event for %s retained as bounded head/tail:\n%s", turnID, text.materialize()),
		},
	}
	return frame
}

func subagentSemanticTerminalFrame(frame stream.Frame) bool {
	return frame.Closed || stream.IsTerminalState(frame.State)
}

const subagentTurnBoundarySource = "task_stream_turn_boundary"

// subagentSemanticDisplayFrame separates Task lifecycle authority from the
// historical Turn boundary needed to rebuild a detached child transcript. A
// raw Closed/State frame may describe only the current Task state; replaying it
// for an older Turn would regress Control's descriptor. The semantic cache
// therefore retains the same completion as a typed UI-only lifecycle event
// with all Task-terminal transport fields removed.
func subagentSemanticDisplayFrame(frame stream.Frame) stream.Frame {
	if frame.Event != nil || !subagentSemanticTerminalFrame(frame) {
		return frame
	}
	frame = stream.CloneFrame(frame)
	state := strings.ToLower(strings.TrimSpace(frame.State))
	if state == "canceled" {
		state = "cancelled"
	}
	if !stream.IsTerminalState(state) {
		state = "unknown_outcome"
	}
	at := frame.UpdatedAt
	if at.IsZero() {
		at = time.Now()
	}
	taskID := strings.TrimSpace(frame.Ref.TaskID)
	turnID := subagentFrameTurnID(frame)
	participantID := firstNonEmpty(taskID, turnID)
	frame.Text = ""
	frame.State = ""
	frame.Running = false
	frame.Closed = false
	frame.ExitCode = nil
	frame.UpdatedAt = at
	frame.Event = &session.Event{
		ID:         "subagent-turn-boundary:" + firstNonEmpty(taskID, "task") + ":" + firstNonEmpty(turnID, "turn"),
		Type:       session.EventTypeLifecycle,
		Visibility: session.VisibilityUIOnly,
		Time:       at,
		Actor: session.ActorRef{
			Kind: session.ActorKindParticipant,
			ID:   participantID,
		},
		Scope: &session.EventScope{
			TurnID: turnID,
			Source: subagentTurnBoundarySource,
			Participant: session.ParticipantRef{
				ID:           participantID,
				Kind:         session.ParticipantKindSubagent,
				Role:         session.ParticipantRoleDelegated,
				DelegationID: taskID,
			},
		},
		Lifecycle: &session.EventLifecycle{Status: state},
	}
	return frame
}

func subagentSemanticTurnBoundary(frame stream.Frame) bool {
	event := frame.Event
	return event != nil && event.Scope != nil &&
		strings.TrimSpace(event.Scope.Source) == subagentTurnBoundarySource &&
		session.EventTypeOf(event) == session.EventTypeLifecycle && event.Lifecycle != nil &&
		stream.IsTerminalState(event.Lifecycle.Status)
}

func subagentCurrentStateFrame(
	frame stream.Frame,
	boundary stream.Cursor,
	currentTurnID string,
	running bool,
	truncatedBefore int64,
) stream.Frame {
	frame = stream.CloneFrame(frame)
	frame.Cursor = boundary
	switch {
	case subagentSemanticTerminalFrame(frame):
		frame.Running = false
	case subagentFrameTurnID(frame) == strings.TrimSpace(currentTurnID):
		frame.Running = running
	default:
		frame.Running = false
	}
	frame.EventsTruncatedBefore = max(frame.EventsTruncatedBefore, truncatedBefore)
	return frame
}
