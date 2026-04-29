package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/OnslaughtSnail/caelis/tui/tuikit"
)

func (m *Model) ensureSubagentSessionState(spawnID, attachID, agent string) (string, *SubagentSessionState) {
	if m.subagentSessions == nil {
		m.subagentSessions = map[string]*SubagentSessionState{}
	}
	sessionKey := subagentPanelSessionKey(spawnID, attachID)
	if sessionKey == "" {
		return "", nil
	}
	state := m.subagentSessions[sessionKey]
	if state == nil {
		state = NewSubagentSessionState(spawnID, attachID, agent)
		m.subagentSessions[sessionKey] = state
	} else {
		if state.SpawnID == "" && strings.TrimSpace(spawnID) != "" {
			state.SpawnID = strings.TrimSpace(spawnID)
		}
		if state.AttachID == "" && strings.TrimSpace(attachID) != "" {
			state.AttachID = strings.TrimSpace(attachID)
		}
		if nextAgent := strings.TrimSpace(agent); nextAgent != "" && nextAgent != state.Agent {
			state.Agent = nextAgent
		}
		if state.Status == "" {
			state.Status = "running"
		}
	}
	return sessionKey, state
}

func (m *Model) migrateSubagentSession(callID, spawnID, attachID, agent string) string {
	if m == nil {
		return subagentPanelSessionKey(spawnID, attachID)
	}
	fromKey := subagentPanelSessionKey(callID, callID)
	toKey := subagentPanelSessionKey(spawnID, attachID)
	if fromKey == "" || toKey == "" || fromKey == toKey {
		return toKey
	}
	if m.subagentSessions == nil {
		m.subagentSessions = map[string]*SubagentSessionState{}
	}
	fromPanels := append([]*SubagentPanelBlock(nil), m.subagentPanelsForSession(fromKey)...)
	toPanels := append([]*SubagentPanelBlock(nil), m.subagentPanelsForSession(toKey)...)
	fromState := m.subagentSessions[fromKey]
	toState := m.subagentSessions[toKey]
	state := toState
	if state == nil {
		if fromState != nil {
			state = fromState
		} else {
			state = NewSubagentSessionState(spawnID, attachID, agent)
		}
	}
	if fromState != nil && fromState != state {
		if len(fromState.Events) > len(state.Events) {
			state.Events = append(state.Events[:0], fromState.Events...)
			state.eventsGen++
		}
		if state.StartedAt.IsZero() || (!fromState.StartedAt.IsZero() && fromState.StartedAt.Before(state.StartedAt)) {
			state.StartedAt = fromState.StartedAt
		}
		if strings.TrimSpace(state.Status) == "" || strings.EqualFold(state.Status, "running") {
			if status := strings.TrimSpace(fromState.Status); status != "" {
				state.Status = status
			}
		}
	}
	if nextSpawnID := strings.TrimSpace(spawnID); nextSpawnID != "" {
		state.SpawnID = nextSpawnID
	}
	if nextAttachID := strings.TrimSpace(attachID); nextAttachID != "" {
		state.AttachID = nextAttachID
	}
	if nextAgent := strings.TrimSpace(agent); nextAgent != "" {
		state.Agent = nextAgent
	}
	if strings.TrimSpace(state.Status) == "" {
		state.Status = "running"
	}
	m.subagentSessions[toKey] = state
	delete(m.subagentSessions, fromKey)

	for _, panel := range append(fromPanels, toPanels...) {
		if panel == nil {
			continue
		}
		panel.bindSession(state)
		if strings.TrimSpace(panel.CallID) == "" && strings.TrimSpace(callID) != "" {
			panel.CallID = strings.TrimSpace(callID)
		}
		m.rememberSubagentPanelRef(toKey, panel.BlockID())
		m.syncInlineSubagentAnchorState(panel)
	}
	delete(m.subagentSessionRefs, fromKey)
	delete(m.subagentBlockIDs, fromKey)
	return toKey
}

func (m *Model) rememberSubagentPanelRef(sessionKey, blockID string) {
	if m == nil || sessionKey == "" || blockID == "" {
		return
	}
	if m.subagentBlockIDs == nil {
		m.subagentBlockIDs = map[string]string{}
	}
	if m.subagentSessionRefs == nil {
		m.subagentSessionRefs = map[string][]string{}
	}
	refs := m.subagentSessionRefs[sessionKey][:0]
	seen := false
	for _, refID := range m.subagentSessionRefs[sessionKey] {
		if strings.TrimSpace(refID) == "" || m.doc.Find(refID) == nil {
			continue
		}
		refs = append(refs, refID)
		if refID == blockID {
			seen = true
		}
	}
	if !seen {
		refs = append(refs, blockID)
	}
	m.subagentSessionRefs[sessionKey] = refs
	m.subagentBlockIDs[sessionKey] = blockID
}

func (m *Model) subagentPanelsForSession(sessionKey string) []*SubagentPanelBlock {
	if m == nil || sessionKey == "" {
		return nil
	}
	ids := m.subagentSessionRefs[sessionKey]
	if len(ids) == 0 {
		if blockID := strings.TrimSpace(m.subagentBlockIDs[sessionKey]); blockID != "" {
			ids = []string{blockID}
		}
	}
	out := make([]*SubagentPanelBlock, 0, len(ids))
	liveIDs := make([]string, 0, len(ids))
	for _, blockID := range ids {
		panel, _ := m.doc.Find(blockID).(*SubagentPanelBlock)
		if panel == nil {
			continue
		}
		out = append(out, panel)
		liveIDs = append(liveIDs, blockID)
	}
	if len(liveIDs) > 0 {
		if m.subagentSessionRefs == nil {
			m.subagentSessionRefs = map[string][]string{}
		}
		m.subagentSessionRefs[sessionKey] = liveIDs
	}
	return out
}

func (m *Model) latestSubagentPanelForSession(sessionKey string) *SubagentPanelBlock {
	if m == nil || sessionKey == "" {
		return nil
	}
	blockID := strings.TrimSpace(m.subagentBlockIDs[sessionKey])
	if blockID != "" {
		panel, _ := m.doc.Find(blockID).(*SubagentPanelBlock)
		if panel != nil {
			return panel
		}
	}
	panels := m.subagentPanelsForSession(sessionKey)
	if len(panels) == 0 {
		return nil
	}
	panel := panels[len(panels)-1]
	m.subagentBlockIDs[sessionKey] = panel.BlockID()
	return panel
}

func (m *Model) syncSubagentSessionPanels(sessionKey string) {
	if m == nil || sessionKey == "" {
		return
	}
	state := m.subagentSessions[sessionKey]
	if state == nil {
		return
	}
	for _, panel := range m.subagentPanelsForSession(sessionKey) {
		panel.bindSession(state)
		m.markViewportBlockDirty(panel.BlockID())
	}
}

func (m *Model) ensureSubagentPanelBlock(spawnID, attachID, agent, callID, anchorTool string, claimAnchor bool) *SubagentPanelBlock {
	sessionKey, state := m.ensureSubagentSessionState(spawnID, attachID, agent)
	if sessionKey == "" || state == nil {
		return nil
	}
	callID = strings.TrimSpace(callID)
	for _, panel := range m.subagentPanelsForSession(sessionKey) {
		if callID != "" && strings.TrimSpace(panel.CallID) == callID {
			panel.bindSession(state)
			m.attachSubagentPanelToCall(panel, callID, anchorTool, claimAnchor)
			m.rememberSubagentPanelRef(sessionKey, panel.BlockID())
			m.syncInlineSubagentAnchorState(panel)
			m.markViewportBlockDirty(panel.BlockID())
			return panel
		}
	}
	if panel := m.latestSubagentPanelForSession(sessionKey); panel != nil {
		prevCallID := strings.TrimSpace(panel.CallID)
		canReuse := callID == "" || callID == prevCallID || prevCallID == ""
		if canReuse {
			panel.bindSession(state)
			m.attachSubagentPanelToCall(panel, callID, anchorTool, claimAnchor)
			m.rememberSubagentPanelRef(sessionKey, panel.BlockID())
			m.syncInlineSubagentAnchorState(panel)
			m.markViewportBlockDirty(panel.BlockID())
			return panel
		}
	}

	sp := NewSubagentPanelBlock(spawnID, attachID, agent, callID)
	sp.bindSession(state)
	m.doc.Append(sp)
	m.attachSubagentPanelToCall(sp, callID, anchorTool, claimAnchor)
	m.rememberSubagentPanelRef(sessionKey, sp.BlockID())
	m.syncInlineSubagentAnchorState(sp)
	m.markViewportStructureDirty()
	return sp
}

func subagentPanelSessionKey(spawnID, attachID string) string {
	if key := strings.TrimSpace(spawnID); key != "" {
		return key
	}
	return strings.TrimSpace(attachID)
}

func (m *Model) resolveRecentTranscriptAnchor(callID, toolName string) string {
	if m == nil || strings.TrimSpace(callID) == "" || strings.TrimSpace(toolName) == "" {
		return ""
	}
	if m.callAnchorIndex == nil {
		m.callAnchorIndex = map[string]string{}
	}
	claimed := map[string]bool{}
	for _, anchorID := range m.callAnchorIndex {
		if strings.TrimSpace(anchorID) != "" {
			claimed[strings.TrimSpace(anchorID)] = true
		}
	}
	prefix := "▸ " + strings.ToUpper(strings.TrimSpace(toolName))
	blocks := m.doc.Blocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		tb, ok := blocks[i].(*TranscriptBlock)
		if !ok || tb == nil || tb.Style != tuikit.LineStyleTool {
			continue
		}
		if claimed[tb.BlockID()] {
			continue
		}
		if strings.HasPrefix(strings.ToUpper(strings.TrimSpace(tb.Raw)), prefix) {
			m.callAnchorIndex[callID] = tb.BlockID()
			return tb.BlockID()
		}
	}
	return ""
}

func (m *Model) attachSubagentPanelToCall(panel *SubagentPanelBlock, callID, anchorTool string, claimAnchor bool) {
	if m == nil || panel == nil {
		return
	}
	callID = strings.TrimSpace(callID)
	anchorTool = strings.TrimSpace(anchorTool)
	prevCallID := strings.TrimSpace(panel.CallID)
	if callID != "" {
		panel.CallID = callID
	}
	resolvedAnchor := ""
	if m.callAnchorIndex != nil {
		resolvedAnchor = strings.TrimSpace(m.callAnchorIndex[panel.CallID])
	}
	if strings.TrimSpace(panel.CallID) == "" {
		return
	}
	if !claimAnchor && resolvedAnchor == "" {
		pendingSpawnAnchors := 0
		for _, anchor := range m.pendingToolAnchors {
			if strings.EqualFold(strings.TrimSpace(anchor.toolName), "SPAWN") {
				pendingSpawnAnchors++
			}
		}
		if pendingSpawnAnchors != 1 {
			return
		}
		claimAnchor = true
	}
	if prevCallID != "" && prevCallID != panel.CallID {
		m.syncInlineSubagentAnchorLabel(prevCallID, false)
	}
	if anchorTool == "" {
		anchorTool = "SPAWN"
	}
	anchorID := resolvedAnchor
	if anchorID == "" && panelProducingTools[strings.ToUpper(anchorTool)] {
		anchorID = m.resolveCallAnchor(panel.CallID, anchorTool)
	}
	if anchorID == "" && claimAnchor {
		anchorID = m.resolveRecentTranscriptAnchor(panel.CallID, anchorTool)
	}
	if anchorID != "" {
		if _, moved := m.doc.MoveAfter(panel.BlockID(), anchorID); moved {
			m.markViewportStructureDirty()
		}
	}
}

func (m *Model) handleSubagentStart(msg SubagentStartMsg) (tea.Model, tea.Cmd) {
	if !msg.Provisional {
		m.migrateSubagentSession(msg.CallID, msg.SpawnID, msg.AttachTarget, msg.Agent)
	}
	sessionKey, state := m.ensureSubagentSessionState(msg.SpawnID, msg.AttachTarget, msg.Agent)
	panel := m.ensureSubagentPanelBlock(msg.SpawnID, msg.AttachTarget, msg.Agent, msg.CallID, msg.AnchorTool, msg.ClaimAnchor)
	if state != nil && !msg.OccurredAt.IsZero() && (state.StartedAt.IsZero() || msg.OccurredAt.Before(state.StartedAt)) {
		state.StartedAt = msg.OccurredAt
	}
	if state != nil && state.Status == "" {
		state.Status = "running"
	}
	if panel != nil && !isTerminalSubagentState(state.Status) {
		m.reviveSubagentPanel(panel, true)
	} else if panel != nil {
		panel.Terminal = true
		panel.Expanded = false
	}
	m.syncSubagentSessionPanels(sessionKey)
	m.syncInlineSubagentAnchorState(panel)
	return m, m.requestStreamViewportSync()
}

func (m *Model) handleSubagentStatus(msg SubagentStatusMsg) (tea.Model, tea.Cmd) {
	sessionKey, state := m.ensureSubagentSessionState(msg.SpawnID, "", "")
	panel := m.ensureSubagentPanelBlock(msg.SpawnID, "", "", "", "", false)
	var cmd tea.Cmd
	if state != nil && !msg.OccurredAt.IsZero() && (state.StartedAt.IsZero() || msg.OccurredAt.Before(state.StartedAt)) {
		state.StartedAt = msg.OccurredAt
	}
	stateName := strings.TrimSpace(msg.State)
	if stateName != "" && state != nil {
		state.Status = stateName
	}
	if panel != nil {
		if isTerminalSubagentState(stateName) {
			panel.Terminal = true
			if subagentHasInlineAnchor(m, panel) {
				m.scheduleInlineSubagentCollapse(panel)
				cmd = m.ensurePanelAnimationTick()
			}
		} else {
			m.reviveSubagentPanel(panel, false)
		}
	}
	// Create an approval event with context when entering waiting_approval.
	if strings.EqualFold(stateName, "waiting_approval") && state != nil {
		state.AddApprovalEvent(msg.ApprovalTool, msg.ApprovalCommand)
	}
	m.syncSubagentSessionPanels(sessionKey)
	m.syncInlineSubagentAnchorState(panel)
	return m, tea.Batch(cmd, m.requestStreamViewportSync())
}

func (m *Model) enqueueSubagentDelta(spawnID string, stream string, chunk string, final bool) (tea.Model, tea.Cmd) {
	sessionKey, state := m.ensureSubagentSessionState(spawnID, "", "")
	_ = m.ensureSubagentPanelBlock(spawnID, "", "", "", "", false)
	streamKind := strings.TrimSpace(stream)
	chunk = tuikit.SanitizeLogText(chunk)
	m.flushSubagentStreamSmoothingExcept(sessionKey, streamKind)
	if state != nil {
		switch {
		case strings.EqualFold(state.Status, "waiting_approval"):
			state.Status = "running"
		case isTerminalSubagentState(state.Status):
			state.ReviveFromTerminal()
		}
	}
	if chunk == "" && !final {
		m.syncSubagentSessionPanels(sessionKey)
		return m, nil
	}
	if !m.enqueueStreamDelta("subagent", sessionKey, streamKind, "", chunk, final) {
		m.syncSubagentSessionPanels(sessionKey)
		return m, nil
	}
	m.syncSubagentSessionPanels(sessionKey)
	return m, m.ensurePendingStreamSmoothingTick()
}

func (m *Model) applySubagentStreamImmediate(sessionKey string, stream string, chunk string) tea.Cmd {
	if m == nil || strings.TrimSpace(sessionKey) == "" || chunk == "" {
		return nil
	}
	state := m.subagentSessions[sessionKey]
	if state == nil {
		_, state = m.ensureSubagentSessionState(sessionKey, "", "")
	}
	if state == nil {
		return nil
	}
	switch stream {
	case "assistant":
		state.AppendStreamChunk(SEAssistant, chunk)
	case "reasoning":
		state.AppendStreamChunk(SEReasoning, chunk)
	default:
		return nil
	}
	m.syncSubagentSessionPanels(sessionKey)
	return m.requestStreamViewportSync()
}

func (m *Model) handleSubagentDone(msg SubagentDoneMsg) (tea.Model, tea.Cmd) {
	sessionKey, state := m.ensureSubagentSessionState(msg.SpawnID, "", "")
	panel := m.ensureSubagentPanelBlock(msg.SpawnID, "", "", "", "", false)
	var cmd tea.Cmd
	if state != nil && !msg.OccurredAt.IsZero() && (state.StartedAt.IsZero() || msg.OccurredAt.Before(state.StartedAt)) {
		state.StartedAt = msg.OccurredAt
	}
	if state != nil {
		state.Status = msg.State
	}
	if panel != nil {
		panel.Terminal = isTerminalSubagentState(msg.State)
	}
	if subagentHasInlineAnchor(m, panel) {
		m.scheduleInlineSubagentCollapse(panel)
		cmd = m.ensurePanelAnimationTick()
	}
	m.syncSubagentSessionPanels(sessionKey)
	m.syncInlineSubagentAnchorState(panel)
	return m, tea.Batch(cmd, m.requestStreamViewportSync())
}

func (m *Model) scheduleInlineSubagentCollapse(panel *SubagentPanelBlock) {
	if m == nil || panel == nil || !subagentHasInlineAnchor(m, panel) {
		return
	}
	if panel.PinnedOpenByUser {
		cancelInlineCollapse(&panel.CollapseAt, &panel.CollapseFrom, &panel.VisibleLines)
		return
	}
	if m.noAnimation {
		panel.Expanded = false
		panel.CollapseAt = time.Time{}
		panel.CollapseFrom = time.Time{}
		panel.VisibleLines = 0
		m.syncInlineSubagentAnchorState(panel)
		return
	}
	scheduleInlineCollapse(&panel.CollapseAt, &panel.CollapseFrom, &panel.CollapseFor, &panel.VisibleLines, panel.StartedAt, subagentOutputPreviewLines, time.Now())
	m.syncInlineSubagentAnchorState(panel)
}

func (m *Model) toggleInlineSubagentPanel(panel *SubagentPanelBlock) {
	if m == nil || panel == nil {
		return
	}
	cancelInlineCollapse(&panel.CollapseAt, &panel.CollapseFrom, &panel.VisibleLines)
	panel.Expanded = !panel.Expanded
	if panel.Expanded && panel.Terminal {
		panel.PinnedOpenByUser = true
	}
	if !panel.Expanded {
		panel.PinnedOpenByUser = false
	}
	m.syncInlineSubagentAnchorState(panel)
}

func (m *Model) reviveSubagentPanel(panel *SubagentPanelBlock, reopen bool) {
	if panel == nil {
		return
	}
	cancelInlineCollapse(&panel.CollapseAt, &panel.CollapseFrom, &panel.VisibleLines)
	panel.Terminal = false
	panel.PinnedOpenByUser = false
	if reopen {
		panel.Expanded = true
	}
}

func (m *Model) findInlineSubagentPanelByAnchorBlockID(blockID string) *SubagentPanelBlock {
	blockID = strings.TrimSpace(blockID)
	if blockID == "" {
		return nil
	}
	for sessionKey := range m.subagentSessionRefs {
		for _, panel := range m.subagentPanelsForSession(sessionKey) {
			callID := strings.TrimSpace(panel.CallID)
			if callID == "" {
				continue
			}
			if strings.TrimSpace(m.callAnchorIndex[callID]) == blockID {
				return panel
			}
		}
	}
	return nil
}

func (m *Model) syncInlineSubagentAnchorState(panel *SubagentPanelBlock) {
	if m == nil || panel == nil {
		return
	}
	m.syncInlineSubagentAnchorLabel(strings.TrimSpace(panel.CallID), panel.Expanded)
}

func (m *Model) syncInlineSubagentAnchorLabel(callID string, expanded bool) {
	if m == nil {
		return
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return
	}
	anchorID := strings.TrimSpace(m.callAnchorIndex[callID])
	if anchorID == "" {
		return
	}
	tb := m.findTranscriptBlock(anchorID)
	if tb == nil {
		return
	}
	next := inlineBashAnchorLabel(tb.Raw, expanded)
	if next != tb.Raw {
		tb.Raw = next
		m.markViewportBlockDirty(tb.BlockID())
	}
}

func subagentHasInlineAnchor(m *Model, panel *SubagentPanelBlock) bool {
	if m == nil || panel == nil {
		return false
	}
	callID := strings.TrimSpace(panel.CallID)
	if callID == "" {
		return false
	}
	return strings.TrimSpace(m.callAnchorIndex[callID]) != ""
}

func isTerminalSubagentState(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "completed", "failed", "interrupted", "timed_out", "cancelled", "canceled", "terminated":
		return true
	default:
		return false
	}
}
