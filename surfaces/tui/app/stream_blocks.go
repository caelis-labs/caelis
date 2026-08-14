package tuiapp

import (
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func (m *Model) queueLogChunk(chunk string) bool {
	if m == nil || chunk == "" {
		return false
	}
	return m.pendingLogBuffer.Append(chunk)
}

func (m *Model) flushPendingLogChunks() tea.Cmd {
	if m == nil || m.pendingLogBuffer.Empty() {
		return nil
	}
	chunk := m.pendingLogBuffer.Drain()
	_, cmd := m.handleLogChunk(chunk)
	return cmd
}

func (m *Model) flushPendingDeferredBatches() tea.Cmd {
	if m == nil {
		return nil
	}
	cmd := m.flushPendingLogChunks()
	if m.pendingLogBuffer.Empty() {
		m.deferredBatchTickScheduled = false
	}
	return cmd
}

func (m *Model) ensureDeferredBatchTick() tea.Cmd {
	if m == nil {
		return nil
	}
	if m.deferredBatchTickScheduled {
		return nil
	}
	if m.pendingLogBuffer.Empty() {
		return nil
	}
	m.deferredBatchTickScheduled = true
	return frameTickCmd(frameTickDeferredBatch, m.streamTickInterval())
}

// ---------------------------------------------------------------------------
// Log chunk handling — inline commit architecture
// ---------------------------------------------------------------------------

func (m *Model) handleLogChunk(chunk string) (tea.Model, tea.Cmd) {
	if chunk == "" {
		return m, nil
	}

	chunk = tuikit.SanitizeLogText(chunk)
	normalized := strings.ReplaceAll(strings.ReplaceAll(chunk, "\r\n", "\n"), "\r", "\n")

	lines := m.logStreamBuffer.Append(normalized)
	m.streamLine = m.logStreamBuffer.Tail()
	var cmds []tea.Cmd

	for _, line := range lines {
		if strings.TrimSpace(line) != "" && m.transientBlockID != "" && m.transientRemove && !isTransientWarningLine(line) {
			m.removeTransientLogLine()
		}
		m.commitLine(line)
	}

	cmds = append(cmds, m.requestStreamViewportSync())
	return m, tea.Batch(cmds...)
}

func (m *Model) discardActiveLogStream() {
	m.streamLine = ""
	m.logStreamBuffer.Reset()
	m.syncViewportContent()
}

func (m *Model) streamTickInterval() time.Duration {
	if m == nil || m.cfg.StreamTickInterval <= 0 {
		return streamSmoothingTickIntervalDefault
	}
	return m.cfg.StreamTickInterval
}

func (m *Model) streamWarmDelay() time.Duration {
	if m == nil || m.cfg.StreamWarmDelay <= 0 {
		return streamSmoothingWarmDelayDefault
	}
	return m.cfg.StreamWarmDelay
}

func (m *Model) streamTargetLag() time.Duration {
	if m == nil || m.cfg.StreamTargetLag <= 0 {
		return streamSmoothingTargetLagDefault
	}
	return m.cfg.StreamTargetLag
}

func (m *Model) streamNormalCPS() float64 {
	if m == nil || m.cfg.StreamNormalCPS <= 0 {
		return streamSmoothingNormalCPSDefault
	}
	return m.cfg.StreamNormalCPS
}

func (m *Model) streamCatchupCPS() float64 {
	if m == nil || m.cfg.StreamCatchupCPS <= 0 {
		return streamSmoothingCatchupCPSDefault
	}
	return m.cfg.StreamCatchupCPS
}

func (m *Model) streamNormalMaxPerTick() int {
	if m == nil || m.cfg.StreamNormalMaxTick <= 0 {
		return streamSmoothingNormalMaxPerFrameDefault
	}
	return m.cfg.StreamNormalMaxTick
}

func (m *Model) streamCatchupMaxPerTick() int {
	if m == nil || m.cfg.StreamCatchupMaxTick <= 0 {
		return streamSmoothingCatchupMaxPerFrameDefault
	}
	return m.cfg.StreamCatchupMaxTick
}

func (m *Model) streamSmoothingState(key string) *streamSmoothingState {
	if m == nil || key == "" {
		return nil
	}
	if m.streamSmoothing == nil {
		m.streamSmoothing = map[string]*streamSmoothingState{}
	}
	state := m.streamSmoothing[key]
	if state == nil {
		parts := strings.SplitN(key, "|", 4)
		now := time.Now()
		state = &streamSmoothingState{
			firstSeen: now,
			lastTick:  now,
		}
		if len(parts) > 0 {
			state.targetKind = parts[0]
		}
		if len(parts) > 1 {
			state.sessionKey = parts[1]
		}
		if len(parts) > 2 {
			state.streamKind = parts[2]
		}
		if len(parts) > 3 {
			state.actor = parts[3]
		}
		m.streamSmoothing[key] = state
	}
	return state
}

func (m *Model) enqueueStreamDelta(targetKind string, sessionKey string, streamKind string, actor string, text string, final bool) bool {
	if m == nil {
		return false
	}
	key := streamSmoothingKey(targetKind, sessionKey, streamKind, actor)
	state := m.streamSmoothingState(key)
	if state == nil {
		return false
	}
	state.actor = strings.TrimSpace(actor)
	if final {
		state.upstreamDone = true
	}
	if text == "" {
		return final
	}
	now := time.Now()
	if state.firstSeen.IsZero() {
		state.firstSeen = now
	}
	if state.lastTick.IsZero() {
		state.lastTick = now
	}
	clusters := splitGraphemeClusters(text)
	if len(clusters) == 0 {
		return false
	}
	if len(state.pending) == 0 {
		state.pendingSince = now
	}
	state.pending = append(state.pending, clusters...)
	backlog := len(state.pending)
	m.streamPlayback.BacklogRunes = backlog
	if backlog > m.streamPlayback.MaxBacklogRunes {
		m.streamPlayback.MaxBacklogRunes = backlog
	}
	return true
}

func (m *Model) ensurePendingStreamSmoothingTick() tea.Cmd {
	if m == nil {
		return nil
	}
	if len(m.streamSmoothing) == 0 || m.streamSmoothingTickScheduled {
		return nil
	}
	if !m.hasImmediateStreamSmoothingWork() {
		return nil
	}
	m.streamSmoothingTickScheduled = true
	return frameTickCmd(frameTickStreamSmoothing, m.streamTickInterval())
}

func (m *Model) hasImmediateStreamSmoothingWork() bool {
	if m == nil {
		return false
	}
	for _, state := range m.streamSmoothing {
		if state == nil || len(state.pending) == 0 {
			continue
		}
		return true
	}
	return false
}

func (m *Model) drainPendingStreamSmoothing(now time.Time) tea.Cmd {
	if m == nil {
		return nil
	}
	m.streamSmoothingTickScheduled = false
	m.streamPlayback.LastFrameAppendRunes = 0
	if now.IsZero() {
		now = time.Now()
	}
	m.streamPlayback.LastFrameAt = now
	if len(m.streamSmoothing) == 0 {
		return nil
	}
	keys := m.pendingStreamSmoothingKeys()
	m.beginDeferredViewportSync()
	defer m.endDeferredViewportSync()
	var cmds []tea.Cmd
	for _, key := range keys {
		state := m.streamSmoothing[key]
		if state == nil || len(state.pending) == 0 {
			delete(m.streamSmoothing, key)
			continue
		}
		backlog := len(state.pending)
		m.streamPlayback.BacklogRunes = backlog
		if backlog > m.streamPlayback.MaxBacklogRunes {
			m.streamPlayback.MaxBacklogRunes = backlog
		}
		chunk, revealed := m.revealPendingSmoothedText(state, now)
		if revealed > 0 {
			state.rendered += revealed
			m.streamPlayback.LastFrameAppendRunes += revealed
			if state.firstPaint.IsZero() {
				state.firstPaint = now
				if !state.firstSeen.IsZero() {
					m.streamPlayback.FirstByteLatency = state.firstPaint.Sub(state.firstSeen)
				}
			}
			if cmd := m.applyPendingSmoothChunk(state, chunk); cmd != nil {
				cmds = append(cmds, cmd)
			}
		}
		if len(state.pending) == 0 {
			delete(m.streamSmoothing, key)
		}
	}
	m.streamPlayback.Frames++
	if !m.hasImmediateStreamSmoothingWork() {
		m.streamSmoothingTickScheduled = false
		if len(m.streamSmoothing) == 0 {
			m.streamPlayback.BacklogRunes = 0
		}
		return tea.Batch(cmds...)
	}
	if len(m.streamSmoothing) == 0 {
		m.streamPlayback.BacklogRunes = 0
		return tea.Batch(cmds...)
	}
	m.streamSmoothingTickScheduled = true
	cmds = append(cmds, frameTickCmd(frameTickStreamSmoothing, m.streamTickInterval()))
	return tea.Batch(cmds...)
}

func (m *Model) pendingStreamSmoothingKeys() []string {
	if m == nil || len(m.streamSmoothing) == 0 {
		return nil
	}
	if len(m.streamSmoothing) == 1 {
		for key, state := range m.streamSmoothing {
			if state == nil || len(state.pending) == 0 {
				delete(m.streamSmoothing, key)
				return nil
			}
			return []string{key}
		}
	}
	keys := make([]string, 0, len(m.streamSmoothing))
	for key, state := range m.streamSmoothing {
		if state == nil || len(state.pending) == 0 {
			delete(m.streamSmoothing, key)
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func (m *Model) revealPendingSmoothedText(state *streamSmoothingState, now time.Time) (string, int) {
	if state == nil || len(state.pending) == 0 {
		return "", 0
	}
	if now.IsZero() {
		now = time.Now()
	}
	if state.lastTick.IsZero() {
		state.lastTick = now
	}
	if state.firstSeen.IsZero() {
		state.firstSeen = now
	}
	if state.pendingSince.IsZero() {
		state.pendingSince = state.firstSeen
	}
	if !state.upstreamDone && now.Sub(state.firstSeen) < m.streamWarmDelay() {
		return "", 0
	}
	dt := now.Sub(state.lastTick)
	if dt < 0 {
		dt = 0
	}
	state.lastTick = now
	cps := m.streamNormalCPS()
	maxPerFrame := m.streamNormalMaxPerTick()
	if state.upstreamDone || now.Sub(state.pendingSince) > m.streamTargetLag() {
		cps = m.streamCatchupCPS()
		maxPerFrame = m.streamCatchupMaxPerTick()
	}
	state.budget += cps * dt.Seconds()
	if state.firstPaint.IsZero() && state.budget < 1 {
		state.budget = 1
	}
	want := int(state.budget)
	if want <= 0 {
		return "", 0
	}
	if want > maxPerFrame {
		want = maxPerFrame
	}
	if want > len(state.pending) {
		want = len(state.pending)
	}
	want = chooseRevealClusterCount(state.pending, want, maxPerFrame)
	if want <= 0 {
		return "", 0
	}
	chunk := joinGraphemeClusters(state.pending[:want])
	state.pending = state.pending[want:]
	state.budget -= float64(want)
	if state.budget < 0 {
		state.budget = 0
	}
	if len(state.pending) == 0 {
		state.pendingSince = time.Time{}
	} else if state.pendingSince.IsZero() {
		state.pendingSince = now
	}
	return chunk, want
}

func (m *Model) applyPendingSmoothChunk(state *streamSmoothingState, chunk string) tea.Cmd {
	if m == nil || state == nil || chunk == "" {
		return nil
	}
	if state.targetKind == "btw" {
		m.applyBTWOverlayImmediate(chunk, false)
	}
	return nil
}

func (m *Model) flushAllPendingStreamSmoothing() {
	m.flushAllPendingStreamSmoothingWithReason("manual")
}

func (m *Model) flushAllPendingStreamSmoothingWithReason(reason string) {
	if m == nil || len(m.streamSmoothing) == 0 {
		return
	}
	m.observeStreamSmoothingFlush(reason)
	keys := make([]string, 0, len(m.streamSmoothing))
	for key := range m.streamSmoothing {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	m.beginDeferredViewportSync()
	defer m.endDeferredViewportSync()
	for _, key := range keys {
		state := m.streamSmoothing[key]
		if state == nil {
			delete(m.streamSmoothing, key)
			continue
		}
		m.applyPendingSmoothChunk(state, joinGraphemeClusters(state.pending))
		delete(m.streamSmoothing, key)
	}
	m.streamSmoothingTickScheduled = false
}

func (m *Model) dropPendingStreamSmoothing(key string) {
	if m == nil || key == "" || len(m.streamSmoothing) == 0 {
		return
	}
	delete(m.streamSmoothing, key)
	if len(m.streamSmoothing) == 0 {
		m.streamSmoothingTickScheduled = false
	}
}

func (m *Model) applyBTWOverlayImmediate(text string, final bool) {
	if m == nil {
		return
	}
	if m.btwOverlay == nil {
		m.btwOverlay = &btwOverlayState{}
	}
	if final {
		m.btwOverlay.Answer = strings.TrimSpace(text)
	} else {
		m.btwOverlay.Answer += text
	}
	m.btwOverlay.Loading = false
	m.clampBTWScroll(len(m.btwContentLines()))
	m.ensureViewportLayout()
}

func (m *Model) handleBTWDelta(text string, final bool) (tea.Model, tea.Cmd) {
	if m == nil {
		return m, nil
	}
	if m.btwOverlay == nil && m.btwDismissed {
		return m, nil
	}
	if final {
		m.dropPendingStreamSmoothing(streamSmoothingKey("btw", "", "answer", ""))
		m.applyBTWOverlayImmediate(text, true)
		return m, nil
	}
	m.applyBTWOverlayImmediate(text, false)
	return m, nil
}

func (m *Model) enqueueBTWDelta(text string, final bool) (tea.Model, tea.Cmd) {
	if m == nil {
		return m, nil
	}
	if m.btwOverlay == nil && m.btwDismissed {
		return m, nil
	}
	if final {
		m.dropPendingStreamSmoothing(streamSmoothingKey("btw", "", "answer", ""))
		m.applyBTWOverlayImmediate(text, true)
		return m, nil
	}
	if !m.enqueueStreamDelta("btw", "", "answer", "", text, false) {
		return m, nil
	}
	return m, m.ensurePendingStreamSmoothingTick()
}

func (m *Model) ensureParticipantTurnBlock(sessionID string, actor string) *ParticipantTurnBlock {
	if m == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	if m.participantTurnIDs == nil {
		m.participantTurnIDs = map[string]string{}
	}
	if blockID := strings.TrimSpace(m.participantTurnIDs[sessionID]); blockID != "" {
		if block, _ := m.doc.Find(blockID).(*ParticipantTurnBlock); block != nil {
			if strings.TrimSpace(actor) != "" {
				block.Actor = participantActorDisplayName(actor)
				m.markViewportBlockDirty(block.BlockID())
			}
			return block
		}
	}
	block := NewParticipantTurnBlock(sessionID, actor)
	m.appendMainTranscriptBlock(block)
	m.participantTurnIDs[sessionID] = block.BlockID()
	m.markViewportStructureDirty()
	return block
}

func (m *Model) handleParticipantStatusMsg(msg ParticipantStatusMsg) (tea.Model, tea.Cmd) {
	block := m.ensureParticipantTurnBlock(msg.SessionID, msg.Actor)
	if block == nil {
		return m, nil
	}
	block.SetStatus(msg.State, msg.ApprovalTool, msg.ApprovalCommand, msg.OccurredAt)
	m.markViewportBlockDirty(block.BlockID())
	if participantTurnIsTerminal(msg.State) {
		m.activeParticipantTurnSessionID = strings.TrimSpace(msg.SessionID)
	}
	return m, m.requestStreamViewportSync()
}

func (m *Model) finalizeActiveParticipantTurn(interrupted bool, err error) bool {
	if m == nil {
		return false
	}
	sessionID := strings.TrimSpace(m.activeParticipantTurnSessionID)
	if sessionID == "" {
		return false
	}
	block := m.findParticipantTurnBlock(sessionID)
	if block == nil {
		m.activeParticipantTurnSessionID = ""
		return false
	}
	if !participantTurnIsTerminal(block.Status) {
		state := "completed"
		switch {
		case interrupted:
			state = "interrupted"
		case err != nil:
			state = "failed"
		}
		block.SetStatus(state, "", "", time.Time{})
	}
	m.activeParticipantTurnSessionID = ""
	return participantTurnHasFooter(block)
}

func (m *Model) findParticipantTurnBlock(sessionID string) *ParticipantTurnBlock {
	if m == nil {
		return nil
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	blockID := ""
	if m.participantTurnIDs != nil {
		blockID = strings.TrimSpace(m.participantTurnIDs[sessionID])
	}
	if blockID == "" {
		return nil
	}
	block, _ := m.doc.Find(blockID).(*ParticipantTurnBlock)
	return block
}
