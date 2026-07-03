package tuiapp

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
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
	case eventstream.Envelope:
		return eventStreamEnvelopeShouldEnqueueForRenderDrain(typed)
	default:
		return false
	}
}

func eventStreamEnvelopeShouldEnqueueForRenderDrain(env eventstream.Envelope) bool {
	if env.Err != nil {
		return false
	}
	if _, ok := eventStreamNarrativeBatchKey(env); ok {
		return true
	}
	if _, ok := eventStreamTerminalBatchKey(env); ok {
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
	case eventstream.Envelope:
		if _, ok := eventStreamTerminalBatchKey(typed); ok {
			return cloneEventStreamTerminalEnvelope(typed)
		}
		if _, ok := eventStreamNarrativeBatchKey(typed); ok {
			return cloneEventStreamNarrativeEnvelope(typed)
		}
		return typed
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
	case eventstream.Envelope:
		right, ok := src.msg.(eventstream.Envelope)
		if !ok {
			return false
		}
		if leftKey, ok := eventStreamTerminalBatchKey(left); ok {
			if rightKey, rightOK := eventStreamTerminalBatchKey(right); rightOK && rightKey == leftKey {
				mergeEventStreamTerminalEnvelope(&left, right)
				dst.msg = left
				return true
			}
		}
		if leftKey, ok := eventStreamNarrativeBatchKey(left); ok {
			if rightKey, rightOK := eventStreamNarrativeBatchKey(right); rightOK && rightKey == leftKey {
				mergeEventStreamNarrativeEnvelope(&left, right)
				dst.msg = left
				return true
			}
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
	case eventstream.Envelope:
		return !eventStreamEnvelopeShouldEnqueueForRenderDrain(typed)
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
		case eventstream.Envelope:
			model, cmd := m.handleACPEventEnvelope(typed)
			if next, ok := model.(*Model); ok {
				m = next
			}
			cmds = append(cmds, cmd, m.flushImmediateViewportSyncForMsg(typed))
		}
	}
	return tea.Batch(cmds...)
}
