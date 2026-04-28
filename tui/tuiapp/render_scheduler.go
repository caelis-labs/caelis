package tuiapp

import (
	"time"

	tea "charm.land/bubbletea/v2"

	appgateway "github.com/OnslaughtSnail/caelis/gateway"
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
	case appgateway.EventEnvelope:
		return gatewayEnvelopeShouldEnqueueForRenderDrain(typed)
	default:
		return false
	}
}

func gatewayEnvelopeShouldEnqueueForRenderDrain(env appgateway.EventEnvelope) bool {
	if env.Err != nil {
		return false
	}
	if _, ok := gatewayNarrativeBatchKey(env); ok {
		return true
	}
	if _, ok := gatewayTerminalBatchKey(env); ok {
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
	case appgateway.EventEnvelope:
		if _, ok := gatewayTerminalBatchKey(typed); ok {
			return cloneGatewayTerminalEnvelope(typed)
		}
		if _, ok := gatewayNarrativeBatchKey(typed); ok {
			return cloneGatewayNarrativeEnvelope(typed)
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
	case appgateway.EventEnvelope:
		right, ok := src.msg.(appgateway.EventEnvelope)
		if !ok {
			return false
		}
		if leftKey, ok := gatewayTerminalBatchKey(left); ok {
			if rightKey, rightOK := gatewayTerminalBatchKey(right); rightOK && rightKey == leftKey {
				mergeGatewayTerminalEnvelope(&left, right)
				dst.msg = left
				return true
			}
		}
		if leftKey, ok := gatewayNarrativeBatchKey(left); ok {
			if rightKey, rightOK := gatewayNarrativeBatchKey(right); rightOK && rightKey == leftKey {
				mergeGatewayNarrativeEnvelope(&left, right)
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
	case appgateway.EventEnvelope:
		return !gatewayEnvelopeShouldEnqueueForRenderDrain(typed)
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
		case appgateway.EventEnvelope:
			model, cmd := m.handleGatewayEventEnvelope(typed)
			if next, ok := model.(*Model); ok {
				m = next
			}
			cmds = append(cmds, cmd, m.flushImmediateViewportSyncForMsg(typed))
		}
	}
	return tea.Batch(cmds...)
}
