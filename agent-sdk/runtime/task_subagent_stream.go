package runtime

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/delegation"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

func (tm *taskRuntime) PublishStream(frame stream.Frame) {
	if tm == nil {
		return
	}
	taskID := strings.TrimSpace(frame.Ref.TaskID)
	sessionID := strings.TrimSpace(frame.Ref.SessionID)
	// Resolve-or-enqueue is one atomic decision with task installation. If a
	// publish raced the install between separate read/write locks, it could be
	// queued after the install had already drained pending frames.
	tm.mu.Lock()
	var task *subagentTask
	if taskID != "" {
		task = tm.subagents[taskID]
	}
	if task == nil && taskID == "" && sessionID != "" {
		var matched *subagentTask
		ambiguous := false
		for _, candidate := range tm.subagents {
			if candidate == nil {
				continue
			}
			if strings.TrimSpace(candidate.anchor.SessionID) == sessionID {
				if matched != nil {
					ambiguous = true
					break
				}
				matched = candidate
			}
		}
		if !ambiguous {
			task = matched
		}
	}
	if task == nil && taskID != "" {
		tm.pending[taskID] = append(tm.pending[taskID], stream.CloneFrame(frame))
	}
	tm.mu.Unlock()
	if task == nil {
		return
	}
	task.applyStreamFrames([]stream.Frame{frame})
}

func (t *subagentTask) seedStreamFromResult(result delegation.Result) {
	if t == nil {
		return
	}
	if taskOutputHasNonBlankLine(t.stdout) || taskOutputHasNonBlankLine(t.stderr) {
		return
	}
	text := result.Result
	turnID := subagentTurnID(t.ref.TaskID, t.turnSeq)
	if taskOutputHasNonBlankLine(text) && subagentFramesContainAssistantTextForTurn(t.streamFrames, turnID) {
		return
	}
	if !taskOutputHasNonBlankLine(text) {
		if len(t.streamFrames) > 0 {
			return
		}
		text = result.OutputPreview
		if taskOutputHasNonBlankLine(text) {
			// A preview is useful to a transient reader but is not proof of a
			// canonical assistant result. Keep it out of the structured frame
			// set used by side-agent dialogue persistence.
			t.appendStreamLocked(text)
		}
		return
	}
	if !taskOutputHasNonBlankLine(text) {
		return
	}
	t.appendStreamFrameLocked(stream.Frame{
		ActivityID: strings.TrimSpace(t.activityID),
		Running:    false,
		Event:      t.resultStreamEvent(text),
	})
}

// resultStreamEvent turns a runner-only final result into the same semantic
// child event shape used by live ACP updates. This is a compatibility fallback
// for runners that return a Result without publishing a final assistant event;
// delegated Task streams intentionally do not interpret untyped text frames as
// child dialogue.
func (t *subagentTask) resultStreamEvent(text string) *session.Event {
	return t.resultStreamEventForTurn(text, t.turnSeq)
}

func (t *subagentTask) resultStreamEventForTurn(text string, turnSeq int64) *session.Event {
	return t.resultStreamEventForTurnAt(text, turnSeq, time.Now())
}

func (t *subagentTask) resultStreamEventForTurnAt(text string, turnSeq int64, at time.Time) *session.Event {
	if t == nil || !taskOutputHasNonBlankLine(text) {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	turnID := subagentTurnID(t.ref.TaskID, turnSeq)
	role := subagentParticipantRole(t)
	participantID := strings.TrimSpace(t.anchor.AgentID)
	handle := strings.TrimPrefix(strings.TrimSpace(t.handle), "@")
	actorName := ""
	if handle != "" {
		actorName = "@" + handle
	}
	messageID := fmt.Sprintf("subagent-result:%s:%d", strings.TrimSpace(t.ref.TaskID), max(turnSeq, 1))
	return &session.Event{
		ID:         messageID,
		MessageID:  messageID,
		Type:       session.EventTypeAssistant,
		Visibility: session.VisibilityUIOnly,
		Time:       at,
		Actor: session.ActorRef{
			Kind: session.ActorKindParticipant,
			ID:   participantID,
			Role: string(role),
			Name: actorName,
		},
		Scope: &session.EventScope{
			TurnID: turnID,
			Source: "subagent_result",
			Participant: session.ParticipantRef{
				ID:           participantID,
				Kind:         session.ParticipantKindSubagent,
				Role:         role,
				DelegationID: strings.TrimSpace(t.ref.TaskID),
			},
		},
		Text: text,
		Protocol: &session.EventProtocol{
			Method: session.ProtocolMethodSessionUpdate,
			Update: &session.ProtocolUpdate{
				SessionUpdate: string(session.ProtocolUpdateTypeAgentMessage),
				MessageID:     messageID,
				Content:       session.ProtocolTextContent(text),
			},
		},
	}
}

// retainCompletedFinalLocked keeps the current Final Message outside the
// evictable transient budget through the Task result contract. When a newer
// Turn completes, the previous Final moves into the semantic byte budget as an
// exact, highest-priority unit; only real byte pressure may evict it after all
// lower-priority context has been removed.
func (t *subagentTask) retainCompletedFinalLocked(text string) {
	if t == nil || !taskOutputHasNonBlankLine(text) {
		return
	}
	turnSeq := max(t.turnSeq, 1)
	if t.latestFinalTurnSeq == turnSeq && t.latestFinalText == text {
		return
	}
	t.invalidateDivergedAssistantStreamLocked(text)
	turnID := subagentTurnID(t.ref.TaskID, turnSeq)
	order := t.streamEventBase + int64(len(t.streamFrames))
	if t.latestFinalTurnSeq != 0 && t.latestFinalTurnSeq != turnSeq {
		previousTurnID := subagentTurnID(t.ref.TaskID, t.latestFinalTurnSeq)
		t.semanticRetention.archiveLatestFinal(stream.Frame{
			Ref: stream.Ref{
				SessionID: strings.TrimSpace(t.sessionRef.SessionID),
				TaskID:    strings.TrimSpace(t.ref.TaskID), TerminalID: previousTurnID,
			},
			ActivityID: strings.TrimSpace(t.latestFinalActivityID),
			Event:      t.resultStreamEventForTurnAt(t.latestFinalText, t.latestFinalTurnSeq, t.latestFinalAt),
			UpdatedAt:  t.latestFinalAt,
		}, t.latestFinalOrder)
	}
	completedAt := time.Now()
	t.latestFinalText = text
	t.latestFinalTurnSeq = turnSeq
	t.latestFinalOrder = order
	t.latestFinalAt = completedAt
	t.latestFinalActivityID = strings.TrimSpace(t.activityID)
	t.semanticRetention.dropAssistantTurn(turnID)
	t.semanticRetention.protectLatestFinal(turnID, order)
}

// invalidateDivergedAssistantStreamLocked retracts a transient assistant delta
// chain that does not converge to the producer's authoritative Final Message.
// Some ACP endpoints publish a provisional prefix and then a complete final
// snapshot after changing message identity. Those are both valid observations,
// but concatenating them produces corrupt presentation such as
// "Here's the Here's the result:". Task stream cursors are append-only, so the
// only honest retraction is a recoverable semantic-current-state boundary.
// The semantic cache then rebuilds this Turn from its tool/reasoning state plus the exact
// Task result retained below.
func (t *subagentTask) invalidateDivergedAssistantStreamLocked(finalText string) {
	if t == nil {
		return
	}
	turnID := subagentTurnID(t.ref.TaskID, max(t.turnSeq, 1))
	if !subagentAssistantStreamDiverged(t.streamFrames, turnID, finalText) {
		return
	}
	// Advance past one explicit reconciliation boundary so a consumer that
	// already acknowledged every provisional frame must still reset.
	t.advanceSubagentStreamBoundaryLocked(1)
	t.resetAssistantStreamIdentityLocked("")
}

// subagentAssistantStreamDiverged recognizes only producer-shaped ACP
// assistant reconciliation. Untyped Task output and generic assistant events
// may legitimately differ from the final Task result, so they are not enough
// to retract an exact stream. Explicit ACP agent-message chunks are narrower:
// tools, plans, and thoughts reset the final-answer segment, and the remaining
// exact deltas must converge to the producer's authoritative Final Message.
func subagentAssistantStreamDiverged(frames []stream.Frame, turnID string, finalText string) bool {
	turnID = strings.TrimSpace(turnID)
	finalText = strings.TrimSpace(finalText)
	if turnID == "" || finalText == "" {
		return false
	}
	var presented strings.Builder
	seenAssistant := false
	reset := func() {
		presented.Reset()
		seenAssistant = false
	}
	for _, frame := range frames {
		if subagentFrameTurnID(frame) != turnID || frame.Event == nil {
			continue
		}
		event := frame.Event
		updateType := strings.TrimSpace(session.ProtocolSessionUpdateType(event))
		if updateType == string(session.ProtocolUpdateTypeAgentThought) {
			reset()
			continue
		}
		if updateType != string(session.ProtocolUpdateTypeAgentMessage) {
			switch session.EventTypeOf(event) {
			case session.EventTypeToolCall, session.EventTypeToolResult, session.EventTypePlan:
				reset()
			}
			continue
		}
		text := session.EventText(event)
		if text == "" {
			continue
		}
		presented.WriteString(text)
		seenAssistant = true
	}
	return seenAssistant && strings.TrimSpace(presented.String()) != finalText
}

func subagentFramesContainAssistantTextForTurn(frames []stream.Frame, turnID string) bool {
	turnID = strings.TrimSpace(turnID)
	for _, frame := range frames {
		if strings.TrimSpace(frame.Ref.TerminalID) != turnID {
			continue
		}
		if strings.TrimSpace(subagentFrameAssistantText(frame)) != "" {
			return true
		}
	}
	return false
}

func subagentFramesContainAssistantText(frames []stream.Frame) bool {
	for _, frame := range frames {
		if strings.TrimSpace(subagentFrameAssistantText(frame)) != "" {
			return true
		}
	}
	return false
}

func subagentFramesContainStructuredAssistantText(frames []stream.Frame) bool {
	for _, frame := range frames {
		if frame.Event != nil && session.EventTypeOf(frame.Event) == session.EventTypeAssistant &&
			strings.TrimSpace(subagentFrameAssistantText(frame)) != "" {
			return true
		}
	}
	return false
}

// discardInitialResultStreamFallbackLocked removes the synthetic boundary
// seeded before the durable post_spawn boundary. A Result larger than the
// exact-stream budget has already advanced streamEventBase while evicting its
// own raw frame, so both retained and oversized seed shapes are recognized.
// The Task is not discoverable yet, so replacing either shape with pending real
// ACP frames cannot invalidate a reader cursor. The Task-result-protected latest
// Final remains available for crash recovery and semantic reconstruction.
func (t *subagentTask) discardInitialResultStreamFallbackLocked() {
	if t == nil {
		return
	}
	retainedFallback := false
	if t.streamEventBase == 0 && len(t.streamFrames) == 1 {
		frame := t.streamFrames[0]
		retainedFallback = frame.Event != nil && frame.Event.Scope != nil &&
			strings.TrimSpace(frame.Event.Scope.Source) == "subagent_result"
	}
	oversizedFallback := t.streamEventBase == 1 && len(t.streamFrames) == 0 && t.streamOutputCursor > 0
	if !retainedFallback && !oversizedFallback {
		return
	}
	for index := range t.streamFrames {
		t.streamFrames[index] = stream.Frame{}
	}
	t.streamFrames = nil
	t.streamFrameSizes = nil
	t.streamEventBase = 0
	t.streamBytes = 0
	t.streamOutputCursor = 0
	t.stdout = ""
	t.stdoutCursor = 0
	t.semanticRetention = subagentSemanticRetention{}
	t.resetAssistantStreamIdentityLocked("")
}

func (t *subagentTask) applyStreamFrames(frames []stream.Frame) {
	if t == nil || len(frames) == 0 {
		return
	}
	t.streamMu.Lock()
	defer t.streamMu.Unlock()
	t.applyStreamFramesLocked(frames)
}

// markSubagentActivityObservationGap advances the transient Task-stream
// boundary after the endpoint journal discarded presentation-only frames. It
// intentionally leaves Task lifecycle untouched; later frames and the exact
// child terminal result continue on the same activity.
func (t *subagentTask) markSubagentActivityObservationGap(dropped uint64) {
	if t == nil {
		return
	}
	advance := int64(dropped)
	if advance <= 0 {
		advance = 1
	}
	t.streamMu.Lock()
	t.mu.Lock()
	t.advanceSubagentStreamBoundaryLocked(advance)
	t.resetAssistantStreamIdentityLocked("")
	t.mu.Unlock()
	t.streamMu.Unlock()
}

func (t *subagentTask) applyStreamFramesLocked(frames []stream.Frame) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, frame := range frames {
		if t.streamTerminalFramed {
			continue
		}
		// Task stream frames are bounded observation. Only the child producer's
		// completion sink or an explicit Task control observation may advance
		// durable lifecycle.
		frame.Closed = false
		if stream.IsTerminalState(frame.State) {
			frame.State = ""
		}
		frame.Running = t.running
		text := subagentFrameAssistantText(frame)
		if text == "" {
			text = frame.Text
		}
		if frame.Event != nil || text != "" || frame.Closed {
			cloned := stream.CloneFrame(frame)
			if cloned.ActivityID == "" {
				cloned.ActivityID = strings.TrimSpace(t.activityID)
			}
			if cloned.Text == "" {
				cloned.Text = text
			}
			cloned.Ref.TaskID = firstNonEmpty(strings.TrimSpace(cloned.Ref.TaskID), strings.TrimSpace(t.ref.TaskID))
			cloned.Ref.SessionID = firstNonEmpty(strings.TrimSpace(cloned.Ref.SessionID), strings.TrimSpace(t.sessionRef.SessionID))
			cloned.Ref.TerminalID = firstNonEmpty(strings.TrimSpace(cloned.Ref.TerminalID), subagentTurnID(t.ref.TaskID, t.turnSeq))
			if cloned.Event != nil {
				if cloned.Event.Scope == nil {
					cloned.Event.Scope = &session.EventScope{}
				}
				cloned.Event.Scope.TurnID = firstNonEmpty(strings.TrimSpace(cloned.Event.Scope.TurnID), subagentTurnID(t.ref.TaskID, t.turnSeq))
			}
			t.appendStreamFrameLocked(cloned)
			t.streamTerminalFramed = t.streamTerminalFramed || cloned.Closed
		}
		if text == "" {
			continue
		}
		if t.result == nil {
			t.result = map[string]any{}
		}
		t.result["output_preview"] = compactFinalOutput(t.stdout, t.stderr)
	}
}

func subagentFrameAssistantText(frame stream.Frame) string {
	if strings.TrimSpace(frame.Text) != "" {
		return frame.Text
	}
	event := frame.Event
	if event == nil || session.EventTypeOf(event) != session.EventTypeAssistant {
		return ""
	}
	if update := session.ProtocolUpdateOf(event); update != nil &&
		strings.TrimSpace(update.SessionUpdate) == string(session.ProtocolUpdateTypeAgentThought) {
		return ""
	}
	if event.Message != nil {
		return event.Message.TextContent()
	}
	return event.Text
}

func (t *subagentTask) appendStreamLocked(text string) {
	if t == nil || text == "" {
		return
	}
	t.streamOutputCursor += int64(len([]byte(text)))
	t.appendBoundedSubagentTextLocked(text, subagentStreamByteCap)
}

func (t *subagentTask) appendRetainedSubagentTextLocked(text string) {
	t.appendBoundedSubagentTextLocked(text, subagentOutputPreviewByteCap)
}

func (t *subagentTask) appendBoundedSubagentTextLocked(text string, byteCap int) {
	if t == nil || text == "" {
		return
	}
	raw := append([]byte(t.stdout), []byte(text)...)
	if len(raw) > byteCap {
		dropped := len(raw) - byteCap
		for dropped < len(raw) && !utf8.RuneStart(raw[dropped]) {
			dropped++
		}
		raw = raw[dropped:]
	}
	t.stdout = string(raw)
	t.stdoutCursor = int64(len([]byte(t.stdout)))
}

func (t *subagentTask) appendStreamFrameLocked(frame stream.Frame) {
	if t == nil || t.streamTerminalFramed {
		return
	}
	frame = stream.CloneFrame(frame)
	text := subagentFrameAssistantText(frame)
	if text == "" {
		text = frame.Text
	}
	if frame.Text == "" {
		frame.Text = text
	}
	frame.Ref.TaskID = firstNonEmpty(strings.TrimSpace(frame.Ref.TaskID), strings.TrimSpace(t.ref.TaskID))
	frame.Ref.SessionID = firstNonEmpty(strings.TrimSpace(frame.Ref.SessionID), strings.TrimSpace(t.sessionRef.SessionID))
	frame.Ref.TerminalID = firstNonEmpty(strings.TrimSpace(frame.Ref.TerminalID), subagentTurnID(t.ref.TaskID, t.turnSeq))
	t.reconcileAssistantStreamIdentityLocked(frame)
	frame.Cursor = stream.Cursor{
		Output: t.streamOutputCursor + int64(len([]byte(text))),
		Events: t.streamEventBase + int64(len(t.streamFrames)) + 1,
	}
	t.semanticRetention.observe(frame, frame.Cursor.Events)
	frameBytes := subagentStreamFrameSize(frame)
	if frameBytes > subagentExactStreamByteCap {
		t.streamOutputCursor = frame.Cursor.Output
		if text != "" {
			t.appendRetainedSubagentTextLocked(text)
		}
		// An exact delta chain cannot cross one frame that is too large to retain.
		// Drop the raw ring through this cursor; lagging readers rebuild from the
		// independently bounded semantic current-state view.
		for index := range t.streamFrames {
			t.streamFrames[index] = stream.Frame{}
		}
		t.streamFrames = nil
		t.streamFrameSizes = nil
		t.streamBytes = 0
		t.streamEventBase = frame.Cursor.Events
		t.notifyStreamChangeLocked()
		return
	} else if text != "" {
		t.streamOutputCursor = frame.Cursor.Output
		t.appendRetainedSubagentTextLocked(text)
	}
	t.streamFrames = append(t.streamFrames, frame)
	t.streamFrameSizes = append(t.streamFrameSizes, frameBytes)
	t.streamBytes += frameBytes
	for len(t.streamFrames) > 0 && (len(t.streamFrames) > subagentStreamFrameCap || t.streamBytes > subagentExactStreamByteCap) {
		evictedBytes := subagentStreamFrameSize(t.streamFrames[0])
		if len(t.streamFrameSizes) > 0 {
			evictedBytes = t.streamFrameSizes[0]
			t.streamFrameSizes[0] = 0
			t.streamFrameSizes = t.streamFrameSizes[1:]
		}
		t.streamBytes -= evictedBytes
		t.streamFrames[0] = stream.Frame{}
		t.streamFrames = t.streamFrames[1:]
		t.streamEventBase++
	}
	t.notifyStreamChangeLocked()
}

// reconcileAssistantStreamIdentityLocked mirrors FinalAssistantAccumulator's
// typed message boundary while the Task is still running. ACP deltas with one
// MessageID remain byte-exact. A different non-empty MessageID in the same
// answer segment means the producer replaced its provisional answer; advance a
// recoverable current-state boundary before publishing the replacement so no
// subscriber can render both chains as one sentence.
func (t *subagentTask) reconcileAssistantStreamIdentityLocked(frame stream.Frame) {
	if t == nil || frame.Event == nil {
		return
	}
	event := frame.Event
	turnID := subagentFrameTurnID(frame)
	updateType := strings.TrimSpace(session.ProtocolSessionUpdateType(event))
	if updateType == string(session.ProtocolUpdateTypeAgentThought) {
		t.closeAssistantStreamSegmentLocked(turnID)
		return
	}
	if updateType != string(session.ProtocolUpdateTypeAgentMessage) {
		switch session.EventTypeOf(event) {
		case session.EventTypeToolCall, session.EventTypeToolResult, session.EventTypePlan:
			t.closeAssistantStreamSegmentLocked(turnID)
		}
		return
	}

	if t.assistantStreamTurnID != turnID {
		t.assistantStreamTurnID = turnID
		t.assistantStreamMessageID = ""
		t.assistantStreamHasText = false
		t.assistantStreamPreviewPrefix = t.stdout
	}
	messageID := strings.TrimSpace(session.EventMessageID(event))
	if messageID != "" && t.assistantStreamMessageID != "" && messageID != t.assistantStreamMessageID {
		if t.assistantStreamHasText {
			t.semanticRetention.dropAssistantMessage(turnID, t.assistantStreamMessageID)
			t.restoreAssistantStreamPreviewLocked()
			t.advanceSubagentStreamBoundaryLocked(1)
		}
		t.assistantStreamMessageID = messageID
		t.assistantStreamHasText = false
	} else if messageID != "" {
		t.assistantStreamMessageID = messageID
	}
	if subagentFrameAssistantText(frame) != "" {
		t.assistantStreamHasText = true
	}
}

func (t *subagentTask) closeAssistantStreamSegmentLocked(turnID string) {
	if t == nil {
		return
	}
	turnID = strings.TrimSpace(turnID)
	if turnID == "" || t.assistantStreamTurnID != turnID {
		return
	}
	if t.assistantStreamHasText && t.assistantStreamMessageID != "" {
		order := t.streamEventBase + int64(len(t.streamFrames)) + 1
		t.semanticRetention.archiveAssistantMessage(turnID, t.assistantStreamMessageID, order)
	}
	t.resetAssistantStreamIdentityLocked(turnID)
}

func (t *subagentTask) resetAssistantStreamIdentityLocked(turnID string) {
	if t == nil {
		return
	}
	turnID = strings.TrimSpace(turnID)
	if turnID != "" && t.assistantStreamTurnID != turnID {
		return
	}
	t.assistantStreamTurnID = ""
	t.assistantStreamMessageID = ""
	t.assistantStreamHasText = false
	t.assistantStreamPreviewPrefix = ""
}

func (t *subagentTask) restoreAssistantStreamPreviewLocked() {
	if t == nil {
		return
	}
	t.stdout = t.assistantStreamPreviewPrefix
	t.stdoutCursor = int64(len([]byte(t.stdout)))
	if t.result == nil {
		return
	}
	if preview := compactFinalOutput(t.stdout, t.stderr); taskOutputHasNonBlankLine(preview) {
		t.result["output_preview"] = preview
	} else {
		delete(t.result, "output_preview")
	}
}

func (t *subagentTask) advanceSubagentStreamBoundaryLocked(advance int64) {
	if t == nil {
		return
	}
	if advance <= 0 {
		advance = 1
	}
	current := t.streamEventBase + int64(len(t.streamFrames))
	for index := range t.streamFrames {
		t.streamFrames[index] = stream.Frame{}
	}
	t.streamFrames = nil
	t.streamFrameSizes = nil
	t.streamBytes = 0
	t.streamEventBase = current + advance
	t.notifyStreamChangeLocked()
}

func (t *subagentTask) ensureTerminalStreamFrameLocked() {
	if t == nil || t.running || !stream.IsTerminalState(string(t.state)) || t.streamTerminalFramed {
		return
	}
	t.appendStreamFrameLocked(stream.Frame{
		Ref: stream.Ref{
			SessionID:  strings.TrimSpace(t.sessionRef.SessionID),
			TaskID:     strings.TrimSpace(t.ref.TaskID),
			TerminalID: subagentTurnID(t.ref.TaskID, t.turnSeq),
		},
		ActivityID: strings.TrimSpace(t.activityID),
		State:      string(t.state),
		Running:    false,
		Closed:     true,
		UpdatedAt:  time.Now(),
	})
	t.streamTerminalFramed = true
}

// notifyStreamChangeLocked broadcasts one coalesced stream-state change. The
// caller must hold t.mu so an Await caller can atomically check the current
// cursor and capture the channel without losing a wakeup.
func (t *subagentTask) notifyStreamChangeLocked() {
	if t == nil {
		return
	}
	if t.streamChanged != nil {
		close(t.streamChanged)
	}
	t.streamChanged = make(chan struct{})
}

func (t *subagentTask) streamChangeWaiterLocked(cursor stream.Cursor) (<-chan struct{}, bool) {
	if t == nil {
		return nil, true
	}
	current := stream.Cursor{
		Output: t.streamOutputCursor,
		Events: t.streamEventBase + int64(len(t.streamFrames)),
	}
	if current.Output > cursor.Output || current.Events > cursor.Events {
		return nil, true
	}
	state := string(t.state)
	if !t.running && stream.IsTerminalState(state) {
		return nil, true
	}
	if t.streamChanged == nil {
		t.streamChanged = make(chan struct{})
	}
	return t.streamChanged, false
}

func subagentStreamFrameSize(frame stream.Frame) int {
	data, err := json.Marshal(frame)
	if err != nil {
		return len(frame.Text)
	}
	return len(data)
}
