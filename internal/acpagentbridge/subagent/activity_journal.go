package subagent

import (
	"encoding/json"
	"reflect"
	"strings"
	"time"

	agent "github.com/caelis-labs/caelis/agent-sdk"
	"github.com/caelis-labs/caelis/agent-sdk/model"
	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/agent-sdk/task/stream"
)

const (
	childActivityMergedMaxFrames = 64
	childActivityMergedMaxBytes  = 32 << 10
)

// mergePendingActivityEventLocked collapses consecutive, not-yet-selected ACP
// assistant deltas for one logical message. Each item has fixed work bounds so
// a long stream cannot repeatedly copy and encode an ever-growing accumulator.
// The newest cursor acknowledges the complete merged delta, so observer
// replacement cannot split the batch.
func (s *childSlot) mergePendingActivityEventLocked(event agent.ChildActivityEvent) *childActivityJournalItem {
	if s == nil || len(s.journal) <= s.deliveringCount {
		return nil
	}
	item := s.journal[len(s.journal)-1]
	if item == nil || item.frameCount >= childActivityMergedMaxFrames ||
		item.preserveLiveBoundary ||
		!mergeChildAssistantActivityEvent(&item.event, event) {
		return nil
	}
	previousSize := item.size
	item.size = childActivityEventSize(item.event)
	item.frameCount++
	s.journalBytes += item.size - previousSize
	return item
}

func mergeChildAssistantActivityEvent(current *agent.ChildActivityEvent, next agent.ChildActivityEvent) bool {
	if current == nil || current.Frame == nil || next.Frame == nil ||
		current.Result != nil || next.Result != nil || current.Gap || next.Gap ||
		current.ActivityID != next.ActivityID || current.Initial != next.Initial {
		return false
	}
	leftText, leftUpdate, ok := mergeableChildAssistantEvent(current.Frame.Event)
	if !ok || len(leftText) > childActivityMergedMaxBytes {
		return false
	}
	rightText, rightUpdate, ok := mergeableChildAssistantEvent(next.Frame.Event)
	if !ok || len(rightText) > childActivityMergedMaxBytes-len(leftText) ||
		strings.TrimSpace(leftUpdate.MessageID) != strings.TrimSpace(rightUpdate.MessageID) {
		return false
	}
	leftFrame := stream.CloneFrame(*current.Frame)
	rightFrame := stream.CloneFrame(*next.Frame)
	leftEvent := session.CloneEvent(leftFrame.Event)
	rightEvent := session.CloneEvent(rightFrame.Event)
	leftFrame.Event, rightFrame.Event = nil, nil
	leftFrame.Text, rightFrame.Text = "", ""
	leftFrame.UpdatedAt, rightFrame.UpdatedAt = time.Time{}, time.Time{}
	stripChildAssistantEventText(leftEvent)
	stripChildAssistantEventText(rightEvent)
	if !reflect.DeepEqual(leftFrame, rightFrame) || !reflect.DeepEqual(leftEvent, rightEvent) {
		return false
	}

	combined := leftText + rightText
	mergedContent, ok := mergeChildAssistantProtocolContent(leftUpdate.Content, rightUpdate.Content, combined)
	if !ok {
		return false
	}
	merged := stream.CloneFrame(*current.Frame)
	merged.Event.Time = next.Frame.Event.Time
	merged.Event.Text = combined
	if merged.Event.Message != nil {
		message := model.NewTextMessage(merged.Event.Message.Role, combined)
		message.Origin = merged.Event.Message.Origin
		merged.Event.Message = &message
	}
	if merged.Event.Protocol != nil && merged.Event.Protocol.Update != nil {
		merged.Event.Protocol.Update.Content = mergedContent
	}
	if current.Frame.Text != "" || next.Frame.Text != "" {
		merged.Text = combined
	}
	merged.UpdatedAt = next.Frame.UpdatedAt
	current.Frame = &merged
	current.Cursor = next.Cursor
	current.Target = agent.NormalizeChildEndpointRef(next.Target)
	return true
}

func mergeChildAssistantProtocolContent(left any, right any, combined string) (any, bool) {
	if leftText, ok := left.(string); ok {
		rightText, rightOK := right.(string)
		return combined, rightOK && leftText != "" && rightText != ""
	}
	leftMap, leftOK := left.(map[string]any)
	rightMap, rightOK := right.(map[string]any)
	if !leftOK || !rightOK {
		return nil, false
	}
	leftCopy := session.CloneState(leftMap)
	rightCopy := session.CloneState(rightMap)
	leftText, leftTextOK := leftCopy["text"].(string)
	rightText, rightTextOK := rightCopy["text"].(string)
	delete(leftCopy, "text")
	delete(rightCopy, "text")
	if !leftTextOK || !rightTextOK || leftText == "" || rightText == "" || !reflect.DeepEqual(leftCopy, rightCopy) {
		return nil, false
	}
	leftCopy["text"] = combined
	return leftCopy, true
}

func mergeableChildAssistantEvent(event *session.Event) (string, *session.ProtocolUpdate, bool) {
	if event == nil || session.EventTypeOf(event) != session.EventTypeAssistant {
		return "", nil, false
	}
	update := session.ProtocolUpdateOf(event)
	if update == nil || strings.TrimSpace(update.SessionUpdate) != string(session.ProtocolUpdateTypeAgentMessage) {
		return "", nil, false
	}
	text := session.EventText(event)
	return text, update, text != ""
}

func stripChildAssistantEventText(event *session.Event) {
	if event == nil {
		return
	}
	event.Time = time.Time{}
	event.Text = ""
	if event.Message != nil {
		event.Message.Parts = nil
	}
	if event.Protocol != nil && event.Protocol.Update != nil {
		event.Protocol.Update.Content = nil
	}
}

// compactActivityJournalLocked bounds observer-only state without changing the
// child lifecycle. Selected callbacks and the fixed-size terminal reference
// are retained; other pending observation frames collapse into one recoverable
// gap cursor.
func (s *childSlot) compactActivityJournalLocked() {
	if s == nil || (len(s.journal) <= childActivityJournalMaxEvents && s.journalBytes <= childActivityJournalMaxBytes) {
		return
	}
	selected := min(s.deliveringCount, len(s.journal))
	prefix := append([]*childActivityJournalItem(nil), s.journal[:selected]...)
	retained := make([]*childActivityJournalItem, 0, len(s.journal)-selected)
	var dropped []*childActivityJournalItem
	var droppedFrames uint64
	var gapCursor uint64
	var gapActivityID string
	var gapInitial bool
	for _, item := range s.journal[selected:] {
		if item != nil && item.terminal != nil {
			retained = append(retained, item)
			continue
		}
		dropped = append(dropped, item)
		if item == nil {
			continue
		}
		gapCursor = max(gapCursor, item.event.Cursor)
		gapActivityID = item.event.ActivityID
		gapInitial = item.event.Initial
		if item.event.Gap {
			droppedFrames += max(item.event.Dropped, 1)
		} else {
			droppedFrames += max(item.frameCount, 1)
		}
	}
	if len(dropped) == 0 {
		// Only a callback already selected within the budget and the fixed-size
		// authoritative terminal reference remain.
		return
	}
	for _, item := range dropped {
		item.acknowledge()
	}
	gap := agent.ChildActivityEvent{
		Target: agent.NormalizeChildEndpointRef(s.target), ActivityID: gapActivityID,
		Cursor: gapCursor, Initial: gapInitial, Gap: true, Dropped: max(droppedFrames, 1),
	}
	gapItem := &childActivityJournalItem{event: gap, size: childActivityEventSize(gap), done: make(chan struct{})}
	s.journal = append(prefix, gapItem)
	s.journal = append(s.journal, retained...)
	s.journalBytes = 0
	for _, item := range s.journal {
		if item != nil {
			s.journalBytes += item.size
		}
	}
}

func childActivityEventSize(event agent.ChildActivityEvent) int {
	encoded, err := json.Marshal(event)
	if err != nil {
		return 1
	}
	return max(len(encoded), 1)
}
