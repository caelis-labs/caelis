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
		if strings.TrimSpace(line) != "" {
			m.finalizeAssistantBlock()
			m.finalizeReasoningBlock()
		}
		m.commitLine(line)
	}

	cmds = append(cmds, m.requestStreamViewportSync())
	return m, tea.Batch(cmds...)
}

func (m *Model) finalizeAssistantBlock() {
	m.activeAssistantID = ""
	m.activeAssistantActor = ""
}

func (m *Model) discardActiveAssistantStream() {
	m.streamLine = ""
	m.logStreamBuffer.Reset()
	// Remove active assistant block from doc.
	if m.activeAssistantID != "" {
		m.doc.Remove(m.activeAssistantID)
		m.activeAssistantID = ""
		m.activeAssistantActor = ""
	}
	// Remove active reasoning block from doc.
	if m.activeReasoningID != "" {
		m.doc.Remove(m.activeReasoningID)
		m.activeReasoningID = ""
		m.activeReasoningActor = ""
	}
	m.syncViewportContent()
}

func normalizeStreamKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "reasoning", "thinking":
		return "reasoning"
	default:
		return "answer"
	}
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

func (m *Model) enqueueMainDelta(kind string, actor string, text string, final bool) (tea.Model, tea.Cmd) {
	streamKind := normalizeStreamKind(kind)
	m.flushMainStreamSmoothingExcept(streamKind)
	if final {
		m.dropPendingStreamSmoothing(streamSmoothingKey("main", "", streamKind, actor))
		return m.applyStreamBlockImmediate(streamKind, actor, text, true)
	}
	if !m.enqueueStreamDelta("main", "", streamKind, actor, text, false) {
		return m, nil
	}
	if m.shouldDeferStreamViewportSync() {
		return m, m.requestStreamViewportSync()
	}
	return m, m.ensurePendingStreamSmoothingTick()
}

func (m *Model) handleStreamBlock(kind string, actor string, text string, final bool) (tea.Model, tea.Cmd) {
	streamKind := normalizeStreamKind(kind)
	if final {
		m.dropPendingStreamSmoothing(streamSmoothingKey("main", "", streamKind, actor))
	}
	return m.applyStreamBlockImmediate(streamKind, actor, text, final)
}

func (m *Model) applyStreamBlockImmediate(streamKind string, actor string, text string, final bool) (tea.Model, tea.Cmd) {
	if text == "" && !final {
		return m, nil
	}
	if text == "" && final && streamKind != "reasoning" && m.activeAssistantID == "" {
		return m, nil
	}
	if streamKind == "reasoning" {
		return m.handleReasoningStream(actor, text, final)
	}
	return m.handleAnswerStream(actor, text, final)
}

func (m *Model) handleAnswerStream(actor string, text string, final bool) (tea.Model, tea.Cmd) {
	actor = strings.TrimSpace(actor)
	if m.activeAssistantID != "" && strings.TrimSpace(m.activeAssistantActor) != actor {
		m.finalizeAssistantBlock()
	}
	if final && m.activeAssistantID == "" && m.shouldSuppressDuplicateFinalAnswer(actor, text) {
		return m, nil
	}

	if m.activeAssistantID == "" {
		block := NewAssistantBlock(actor)
		block.Streaming = !final
		if final {
			block.Raw = text
			block.LastFinal = text
		} else {
			block.appendActiveDelta(text)
		}
		m.doc.Append(block)
		m.activeAssistantID = block.BlockID()
		m.activeAssistantActor = actor
		m.hasCommittedLine = true
		m.lastCommittedStyle = tuikit.LineStyleAssistant
		m.lastCommittedRaw = "· "
		if final {
			m.activeAssistantID = ""
			m.activeAssistantActor = ""
		}
		m.markViewportStructureDirty()
		return m, m.requestStreamViewportSync()
	}

	block := m.doc.Find(m.activeAssistantID)
	if block == nil {
		m.activeAssistantID = ""
		m.activeAssistantActor = ""
		return m, nil
	}
	ab := block.(*AssistantBlock)
	ab.Actor = actor
	if final {
		ab.Raw = ab.finalizeActiveText(text)
		ab.Streaming = false
		ab.LastFinal = ab.Raw
	} else {
		ab.appendActiveDelta(text)
	}
	if final {
		m.activeAssistantID = ""
		m.activeAssistantActor = ""
	}
	m.lastCommittedStyle = tuikit.LineStyleAssistant
	m.lastCommittedRaw = "· "
	m.markViewportBlockDirty(ab.BlockID())
	return m, m.requestStreamViewportSync()
}

func (m *Model) shouldSuppressDuplicateFinalAnswer(actor string, text string) bool {
	if m == nil {
		return false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return false
	}
	blocks := m.doc.Blocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		switch block := blocks[i].(type) {
		case *TranscriptBlock:
			if strings.TrimSpace(block.Raw) == "" {
				continue
			}
			return false
		case *AssistantBlock:
			if block.Streaming {
				return false
			}
			lastFinal := strings.TrimSpace(block.LastFinal)
			if lastFinal == "" {
				lastFinal = strings.TrimSpace(block.Raw)
			}
			return strings.TrimSpace(block.Actor) == actor && lastFinal == text
		default:
			return false
		}
	}
	return false
}

func (m *Model) handleReasoningStream(actor string, text string, final bool) (tea.Model, tea.Cmd) {
	actor = strings.TrimSpace(actor)
	if m.activeReasoningID != "" && strings.TrimSpace(m.activeReasoningActor) != actor {
		m.finalizeReasoningBlock()
	}
	if final {
		if m.activeReasoningID == "" {
			if strings.TrimSpace(text) == "" {
				return m, nil
			}
			block := NewReasoningBlock(actor)
			block.Raw = text
			block.Streaming = false
			m.doc.Append(block)
			m.hasCommittedLine = true
			m.lastCommittedStyle = tuikit.LineStyleReasoning
			m.lastCommittedRaw = "› "
			m.markViewportStructureDirty()
			return m, m.requestStreamViewportSync()
		}
		block := m.doc.Find(m.activeReasoningID)
		if block == nil {
			m.activeReasoningID = ""
			m.activeReasoningActor = ""
			return m, nil
		}
		rb := block.(*ReasoningBlock)
		rb.Actor = actor
		rb.Raw = rb.finalizeActiveText(text)
		rb.Streaming = false
		m.activeReasoningID = ""
		m.activeReasoningActor = ""
		m.lastCommittedStyle = tuikit.LineStyleReasoning
		m.lastCommittedRaw = "› "
		m.markViewportBlockDirty(rb.BlockID())
		return m, m.requestStreamViewportSync()
	}

	if m.activeReasoningID == "" {
		block := NewReasoningBlock(actor)
		block.appendActiveDelta(text)
		m.doc.Append(block)
		m.activeReasoningID = block.BlockID()
		m.activeReasoningActor = actor
		m.hasCommittedLine = true
		m.lastCommittedStyle = tuikit.LineStyleReasoning
		m.lastCommittedRaw = "› "
		m.markViewportStructureDirty()
		return m, m.requestStreamViewportSync()
	}

	block := m.doc.Find(m.activeReasoningID)
	if block == nil {
		m.activeReasoningID = ""
		m.activeReasoningActor = ""
		return m, nil
	}
	rb := block.(*ReasoningBlock)
	rb.Actor = actor
	rb.appendActiveDelta(text)
	m.lastCommittedStyle = tuikit.LineStyleReasoning
	m.lastCommittedRaw = "› "
	m.markViewportBlockDirty(rb.BlockID())
	return m, m.requestStreamViewportSync()
}

const minReplayLen = 16

func mergeStreamChunk(existing string, incoming string, final bool) string {
	if final {
		if incoming == "" {
			return existing
		}
		return incoming
	}
	if incoming == "" {
		return existing
	}
	if existing == "" {
		return incoming
	}
	if len(incoming) >= minReplayLen && strings.HasPrefix(existing, incoming) {
		return existing
	}
	return existing + incoming
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
		if m.shouldDeferMainStreamSmoothing(state) {
			continue
		}
		return true
	}
	return false
}

func (m *Model) shouldDeferMainStreamSmoothing(state *streamSmoothingState) bool {
	if m == nil || state == nil {
		return false
	}
	return state.targetKind == "main" && m.shouldDeferStreamViewportSync()
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
		if m.shouldDeferMainStreamSmoothing(state) {
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

func (m *Model) flushDeferredMainStreamSmoothing() {
	if m == nil || len(m.streamSmoothing) == 0 {
		return
	}
	keys := make([]string, 0, len(m.streamSmoothing))
	for key, state := range m.streamSmoothing {
		if state == nil || len(state.pending) == 0 || state.targetKind != "main" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	m.beginDeferredViewportSync()
	defer m.endDeferredViewportSync()
	for _, key := range keys {
		state := m.streamSmoothing[key]
		if state == nil || len(state.pending) == 0 || state.targetKind != "main" {
			continue
		}
		_ = m.applyPendingSmoothChunk(state, joinGraphemeClusters(state.pending))
		delete(m.streamSmoothing, key)
	}
	if !m.hasImmediateStreamSmoothingWork() {
		m.streamSmoothingTickScheduled = false
	}
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
	want = m.chooseRevealClusterCountForState(state, want, maxPerFrame)
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

func (m *Model) chooseRevealClusterCountForState(state *streamSmoothingState, desired int, maxPerFrame int) int {
	if state == nil {
		return 0
	}
	count := chooseRevealClusterCount(state.pending, desired, maxPerFrame)
	if count <= 0 || state.targetKind != "main" {
		return count
	}
	wrapWidth := m.viewportContentWidth()
	if wrapWidth <= 0 {
		return count
	}
	existing := m.currentMainStreamRaw(state)
	return extendRevealToStableRenderedRows(existing, state.pending, count, maxPerFrame, wrapWidth, state.streamKind, state.actor, state.upstreamDone)
}

func (m *Model) currentMainStreamRaw(state *streamSmoothingState) string {
	if m == nil || state == nil {
		return ""
	}
	switch state.streamKind {
	case "reasoning":
		if m.activeReasoningID == "" {
			return ""
		}
		block, ok := m.doc.Find(m.activeReasoningID).(*ReasoningBlock)
		if !ok || block == nil {
			return ""
		}
		if block.Streaming && block.activeBuffer != nil && !block.activeBuffer.Empty() {
			return block.activeBuffer.Text()
		}
		return block.Raw
	default:
		if m.activeAssistantID == "" {
			return ""
		}
		block, ok := m.doc.Find(m.activeAssistantID).(*AssistantBlock)
		if !ok || block == nil {
			return ""
		}
		if block.Streaming && block.activeBuffer != nil && !block.activeBuffer.Empty() {
			return block.activeBuffer.Text()
		}
		return block.Raw
	}
}

func (m *Model) applyPendingSmoothChunk(state *streamSmoothingState, chunk string) tea.Cmd {
	if m == nil || state == nil || chunk == "" {
		return nil
	}
	switch state.targetKind {
	case "btw":
		m.applyBTWOverlayImmediate(chunk, false)
		return nil
	default:
		_, cmd := m.applyStreamBlockImmediate(state.streamKind, state.actor, chunk, false)
		return cmd
	}
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

func (m *Model) flushMainStreamSmoothingExcept(streamKind string) {
	m.flushMatchingStreamSmoothing(func(state *streamSmoothingState) bool {
		return state != nil && state.targetKind == "main" && state.streamKind != streamKind
	})
}

func (m *Model) flushSubagentStreamSmoothingExcept(sessionKey string, streamKind string) {
	m.flushMatchingStreamSmoothing(func(state *streamSmoothingState) bool {
		return state != nil && state.targetKind == "subagent" && state.sessionKey == sessionKey && state.streamKind != streamKind
	})
}

func (m *Model) flushMatchingStreamSmoothing(match func(*streamSmoothingState) bool) {
	if m == nil || len(m.streamSmoothing) == 0 || match == nil {
		return
	}
	keys := make([]string, 0, len(m.streamSmoothing))
	for key, state := range m.streamSmoothing {
		if match(state) {
			keys = append(keys, key)
		}
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
	if len(m.streamSmoothing) == 0 {
		m.streamSmoothingTickScheduled = false
	}
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

func (m *Model) finalizeReasoningBlock() {
	m.activeReasoningID = ""
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
	m.doc.Append(block)
	m.participantTurnIDs[sessionID] = block.BlockID()
	m.markViewportStructureDirty()
	return block
}

func (m *Model) handleParticipantTurnStreamEvent(sessionID, kind, actor, text string, final bool, source narrativeSourceIdentity, occurredAt ...time.Time) (tea.Model, tea.Cmd) {
	m.finalizeAssistantBlock()
	m.finalizeReasoningBlock()
	text = tuikit.SanitizeLogText(text)
	if text == "" && !final {
		return m, nil
	}
	block := m.ensureParticipantTurnBlock(sessionID, actor)
	if block == nil {
		return m, nil
	}
	m.activeParticipantTurnSessionID = strings.TrimSpace(block.SessionID)
	if !block.EndedAt.IsZero() {
		block.EndedAt = time.Time{}
	}
	streamKind := normalizeStreamKind(kind)
	switch streamKind {
	case "reasoning":
		if final {
			block.ReplaceFinalStreamEvent(SEReasoning, text, source, occurredAt...)
		} else if text != "" {
			block.AppendStreamEvent(SEReasoning, text, source, occurredAt...)
		}
	default:
		if final {
			closeLatestReasoningTiming(block.Events, narrativeEventTime(occurredAt...))
		}
		if final {
			block.ReplaceFinalStreamEvent(SEAssistant, text, source, occurredAt...)
		} else if text != "" {
			block.AppendStreamEvent(SEAssistant, text, source, occurredAt...)
		}
	}
	if final && streamKind != "reasoning" {
		block.SetStatus("completed", "", "", narrativeEventTime(occurredAt...))
		m.activeParticipantTurnSessionID = ""
	} else if final && strings.EqualFold(strings.TrimSpace(block.Status), "waiting_approval") {
		block.Status = "running"
	} else if state := strings.ToLower(strings.TrimSpace(block.Status)); state == "initializing" || state == "prompting" {
		block.Status = "running"
	}
	m.markViewportBlockDirty(block.BlockID())
	m.hasCommittedLine = true
	m.lastCommittedStyle = tuikit.LineStyleAssistant
	m.lastCommittedRaw = strings.TrimSpace(block.Actor) + ":"
	return m, m.requestStreamViewportSync()
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
