package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type pendingRenderEvent struct {
	lane renderEventLane
	msg  tea.Msg
}

type pendingRenderEvents struct {
	items []pendingRenderEvent
}

func (p *pendingRenderEvents) hasAny() bool {
	return p != nil && len(p.items) > 0
}

func (p *pendingRenderEvents) reset() {
	if p == nil {
		return
	}
	p.items = p.items[:0]
}

func (m *Model) renderSchedulerEnabled() bool {
	return m != nil && m.cfg.StreamTickInterval > 0
}

func (m *Model) shouldEnqueueRenderEvent(msg tea.Msg, policy renderEventPolicy) bool {
	if !m.renderSchedulerEnabled() {
		return false
	}
	switch typed := msg.(type) {
	case LogChunkMsg:
		return typed.Chunk != ""
	case TranscriptEventsMsg:
		return transcriptEventsShouldEnqueueForRenderDrain(typed)
	default:
		return false
	}
}

func transcriptEventsShouldEnqueueForRenderDrain(msg TranscriptEventsMsg) bool {
	if _, ok := pendingTranscriptNarrativeBatchKey(msg); ok {
		return true
	}
	if _, ok := pendingTranscriptTerminalBatchKey(msg); ok {
		return true
	}
	return false
}

func (m *Model) enqueueRenderEvent(msg tea.Msg, lane renderEventLane) tea.Cmd {
	if m == nil {
		return nil
	}
	event := pendingRenderEvent{lane: lane, msg: clonePendingRenderMsg(msg)}
	if len(m.pendingRenderEvents.items) > 0 {
		last := &m.pendingRenderEvents.items[len(m.pendingRenderEvents.items)-1]
		if mergePendingRenderEvent(last, event) {
			return m.ensureRenderDrainTick()
		}
	}
	m.pendingRenderEvents.items = append(m.pendingRenderEvents.items, event)
	return m.ensureRenderDrainTick()
}

func clonePendingRenderMsg(msg tea.Msg) tea.Msg {
	switch typed := msg.(type) {
	case TranscriptEventsMsg:
		return cloneTranscriptEventsMsg(typed)
	default:
		return msg
	}
}

func mergePendingRenderEvent(dst *pendingRenderEvent, src pendingRenderEvent) bool {
	if dst == nil || dst.lane != src.lane {
		return false
	}
	switch left := dst.msg.(type) {
	case LogChunkMsg:
		right, ok := src.msg.(LogChunkMsg)
		if !ok {
			return false
		}
		left.Chunk += right.Chunk
		dst.msg = left
		return true
	case TranscriptEventsMsg:
		right, ok := src.msg.(TranscriptEventsMsg)
		if !ok {
			return false
		}
		if mergeTranscriptEventsMsg(&left, right) {
			dst.msg = left
			return true
		}
	}
	return false
}

func (m *Model) ensureRenderDrainTick() tea.Cmd {
	if m == nil || m.renderDrainTickScheduled || !m.pendingRenderEvents.hasAny() {
		return nil
	}
	m.renderDrainTickScheduled = true
	return frameTickCmd(frameTickRenderDrain, m.streamTickInterval())
}

func (m *Model) shouldFlushPendingRenderEventsBefore(msg tea.Msg, policy renderEventPolicy) bool {
	if m == nil || !m.pendingRenderEvents.hasAny() {
		return false
	}
	switch typed := msg.(type) {
	case frameTickMsg:
		return typed.kind != frameTickRenderDrain
	case TranscriptEventsMsg:
		return !transcriptEventsShouldEnqueueForRenderDrain(typed)
	case LogChunkMsg:
		return false
	default:
		return policy.flushSmoothing || policy.flushLogChunks
	}
}

func (m *Model) drainPendingRenderEvents(time.Time) tea.Cmd {
	if m == nil {
		return nil
	}
	m.renderDrainTickScheduled = false
	if !m.pendingRenderEvents.hasAny() {
		return nil
	}
	items := append([]pendingRenderEvent(nil), m.pendingRenderEvents.items...)
	m.pendingRenderEvents.reset()
	m.beginDeferredViewportSync()
	defer m.endDeferredViewportSync()
	var cmds []tea.Cmd
	for _, item := range items {
		switch typed := item.msg.(type) {
		case LogChunkMsg:
			model, cmd := m.handleLogChunk(typed.Chunk)
			if next, ok := model.(*Model); ok {
				m = next
			}
			cmds = append(cmds, cmd)
		case TranscriptEventsMsg:
			model, cmd := m.handleTranscriptEventsMsg(typed)
			if next, ok := model.(*Model); ok {
				m = next
			}
			cmds = append(cmds, cmd)
		}
	}
	return tea.Batch(cmds...)
}

func cloneTranscriptEventsMsg(msg TranscriptEventsMsg) TranscriptEventsMsg {
	out := msg
	out.Events = append([]TranscriptEvent(nil), msg.Events...)
	return out
}

func mergeTranscriptEventsMsg(dst *TranscriptEventsMsg, src TranscriptEventsMsg) bool {
	if dst == nil {
		return false
	}
	if leftKey, ok := pendingTranscriptNarrativeBatchKey(*dst); ok {
		if rightKey, rightOK := pendingTranscriptNarrativeBatchKey(src); rightOK && rightKey == leftKey {
			dst.Events[0].Text += src.Events[0].Text
			dst.Events[0].OccurredAt = src.Events[0].OccurredAt
			return true
		}
	}
	if leftKey, ok := pendingTranscriptTerminalBatchKey(*dst); ok {
		if rightKey, rightOK := pendingTranscriptTerminalBatchKey(src); rightOK && rightKey == leftKey {
			existing := dst.Events[0].ToolOutput
			incoming := src.Events[0].ToolOutput
			if strings.EqualFold(strings.TrimSpace(dst.Events[0].ToolName), "RUN_COMMAND") {
				dst.Events[0].ToolOutput = appendDeltaStreamChunk(existing, incoming)
			} else {
				dst.Events[0].ToolOutput = mergeSubagentStreamChunk(existing, incoming)
			}
			dst.Events[0].OccurredAt = src.Events[0].OccurredAt
			return true
		}
	}
	return false
}

func pendingTranscriptNarrativeBatchKey(msg TranscriptEventsMsg) (string, bool) {
	if len(msg.Events) != 1 {
		return "", false
	}
	return appTranscriptNarrativeBatchKey(msg.Events[0])
}

func pendingTranscriptTerminalBatchKey(msg TranscriptEventsMsg) (string, bool) {
	if len(msg.Events) != 1 {
		return "", false
	}
	event := msg.Events[0]
	if event.Kind != TranscriptEventTool || !strings.EqualFold(strings.TrimSpace(event.ToolStatus), transcriptToolStatusRunning) || strings.TrimSpace(event.ToolOutput) == "" {
		return "", false
	}
	return strings.Join([]string{
		strings.TrimSpace(string(event.Scope)),
		strings.TrimSpace(event.ScopeID),
		strings.TrimSpace(event.ToolCallID),
		strings.TrimSpace(event.ToolName),
		strings.TrimSpace(event.ToolStream),
	}, "\x00"), true
}
