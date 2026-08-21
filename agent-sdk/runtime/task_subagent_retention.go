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
	return subagentSemanticUnitOverhead + len(u.key) + u.frameBytes + u.text.allocatedBytes()
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
	bytes         int
	barrier       uint64
	units         map[string]*subagentSemanticUnit
	order         *list.List
	buckets       [subagentSemanticPriorityCount]*list.List
	assistantKeys map[string]map[string]struct{}
}

type subagentSemanticDescriptor struct {
	key           string
	turnID        string
	assistantTurn string
	priority      subagentSemanticPriority
	text          string
	narrative     bool
	replace       bool
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
	if !descriptor.narrative {
		r.barrier++
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
		unit.frame = subagentBoundSemanticFrame(frame)
		unit.frameBytes = subagentStreamFrameSize(unit.frame)
	}
	r.bytes += unit.allocatedBytes() - before
	r.evict()
}

func (r *subagentSemanticRetention) dropAssistantTurn(turnID string) {
	if r == nil || turnID == "" {
		return
	}
	keys := r.assistantKeys[turnID]
	for key := range keys {
		if unit := r.units[key]; unit != nil {
			r.remove(unit)
		}
	}
	delete(r.assistantKeys, turnID)
}

func (r *subagentSemanticRetention) dropAssistantMessage(turnID string, messageID string) {
	if r == nil {
		return
	}
	turnID = strings.TrimSpace(turnID)
	messageID = strings.TrimSpace(messageID)
	if turnID == "" || messageID == "" {
		return
	}
	if unit := r.units["narrative:"+turnID+":assistant:"+messageID]; unit != nil {
		r.remove(unit)
	}
}

// archiveAssistantMessage closes the active identity key at a producer
// barrier. If the endpoint later reuses that MessageID, the new segment gets a
// fresh active unit; replacing that segment then cannot discard the legitimate
// pre-tool or pre-reasoning narrative retained here.
func (r *subagentSemanticRetention) archiveAssistantMessage(turnID string, messageID string, order int64) {
	if r == nil {
		return
	}
	turnID = strings.TrimSpace(turnID)
	messageID = strings.TrimSpace(messageID)
	if turnID == "" || messageID == "" {
		return
	}
	key := "narrative:" + turnID + ":assistant:" + messageID
	unit := r.units[key]
	if unit == nil {
		return
	}
	archivedKey := fmt.Sprintf("%s:segment:%d", key, order)
	if r.units[archivedKey] != nil {
		return
	}
	before := unit.allocatedBytes()
	delete(r.units, key)
	unit.key = archivedKey
	r.units[archivedKey] = unit
	if keys := r.assistantKeys[turnID]; keys != nil {
		delete(keys, key)
		keys[archivedKey] = struct{}{}
	}
	r.bytes += unit.allocatedBytes() - before
	r.evict()
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
		if identity == "" {
			identity = fmt.Sprintf("anonymous:%d", r.barrier)
		}
		return subagentSemanticDescriptor{
			key:    "narrative:" + turnID + ":" + kind + ":" + identity,
			turnID: turnID, assistantTurn: assistantTurn, priority: priority,
			text: session.EventText(event), narrative: true,
		}
	}

	eventID := firstNonEmpty(strings.TrimSpace(event.ID), fmt.Sprintf("event:%d", order))
	switch session.EventTypeOf(event) {
	case session.EventTypeToolCall, session.EventTypeToolResult:
		callID := subagentSemanticToolCallID(event)
		if callID == "" {
			callID = eventID
		}
		kind := "call"
		if session.EventTypeOf(event) == session.EventTypeToolResult {
			kind = "update"
		}
		return subagentSemanticDescriptor{
			key: "tool:" + turnID + ":" + kind + ":" + callID, turnID: turnID,
			priority: subagentSemanticTool, replace: kind == "update",
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
