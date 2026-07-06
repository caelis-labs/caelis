package tuiapp

import (
	"strings"
	"time"
)

const (
	mainAnchorToolPrefix     = "tool:"
	mainAnchorApprovalPrefix = "approval:"
)

// Main ACP display is intentionally timeline-first: unanchored main events
// append to the current tail only. TurnID is kept as block metadata, not as a
// routing key. Stable entity anchors, such as tool call IDs and approval call
// IDs, are the only paths that may update an older block after a user barrier.
func (m *Model) ensureMainTimelineBlock(event TranscriptEvent) *MainACPTurnBlock {
	if m == nil || m.doc == nil {
		return nil
	}
	if blockID := strings.TrimSpace(m.mainTimelineTailID); blockID != "" {
		if block, _ := m.doc.Find(blockID).(*MainACPTurnBlock); block != nil {
			m.fillMainTimelineBlockMetadata(block, event)
			return block
		}
		m.mainTimelineTailID = ""
	}
	block := NewMainACPTurnBlock(strings.TrimSpace(event.TurnID))
	m.fillMainTimelineBlockMetadata(block, event)
	m.doc.Append(block)
	m.mainTimelineTailID = block.BlockID()
	m.markViewportStructureDirty()
	return block
}

func (m *Model) fillMainTimelineBlockMetadata(block *MainACPTurnBlock, event TranscriptEvent) {
	if block == nil {
		return
	}
	if strings.TrimSpace(block.TurnKey) == "" {
		block.TurnKey = strings.TrimSpace(event.TurnID)
	}
	if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
		block.StartedAt = event.OccurredAt
	}
}

func (m *Model) mainBlockForAnchor(event TranscriptEvent, anchor string) *MainACPTurnBlock {
	anchor = strings.TrimSpace(anchor)
	if anchor == "" {
		return m.ensureMainTimelineBlock(event)
	}
	if m.mainAnchorBlockIDs == nil {
		m.mainAnchorBlockIDs = map[string]string{}
	}
	if blockID := strings.TrimSpace(m.mainAnchorBlockIDs[anchor]); blockID != "" {
		if block, _ := m.doc.Find(blockID).(*MainACPTurnBlock); block != nil {
			if mainAnchorBlockAcceptsEvent(block, event) {
				m.fillMainTimelineBlockMetadata(block, event)
				return block
			}
		}
		delete(m.mainAnchorBlockIDs, anchor)
	}
	block := m.ensureMainTimelineBlock(event)
	if block != nil {
		m.mainAnchorBlockIDs[anchor] = block.BlockID()
	}
	return block
}

func mainAnchorBlockAcceptsEvent(block *MainACPTurnBlock, event TranscriptEvent) bool {
	if block == nil {
		return false
	}
	eventTurnID := strings.TrimSpace(event.TurnID)
	blockTurnKey := strings.TrimSpace(block.TurnKey)
	if eventTurnID != "" && blockTurnKey != "" && eventTurnID != blockTurnKey {
		return false
	}
	if !block.EndedAt.IsZero() {
		return eventTurnID != "" && blockTurnKey != "" && eventTurnID == blockTurnKey
	}
	return true
}

func mainToolAnchor(callID string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return ""
	}
	return mainAnchorToolPrefix + callID
}

func mainApprovalAnchor(callID string) string {
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return ""
	}
	return mainAnchorApprovalPrefix + callID
}

func (m *Model) mainTimelineBarrier() {
	if m == nil {
		return
	}
	blockID := strings.TrimSpace(m.mainTimelineTailID)
	if blockID == "" {
		return
	}
	if m.doc != nil {
		if block, _ := m.doc.Find(blockID).(*MainACPTurnBlock); block != nil {
			block.onNarrativeBarrier()
			m.markViewportBlockDirty(block.BlockID())
		}
	}
	m.mainTimelineTailID = ""
}

func (m *Model) finalizeMainTimelineTail(interrupted bool, err error) {
	if m == nil {
		return
	}
	blockID := strings.TrimSpace(m.mainTimelineTailID)
	if blockID == "" {
		return
	}
	block, _ := m.doc.Find(blockID).(*MainACPTurnBlock)
	if block == nil {
		m.mainTimelineTailID = ""
		return
	}
	state := "completed"
	switch {
	case interrupted:
		state = "interrupted"
	case err != nil:
		state = "failed"
	}
	block.SetStatus(state, "", "", time.Time{})
	m.captureLiveTurnDurationFromMainBlock(block)
	m.mainTimelineTailID = ""
}

func (m *Model) closeMainTimelineTailWithState(block *MainACPTurnBlock, occurredAt time.Time, state string) {
	if m == nil || block == nil {
		return
	}
	state = strings.TrimSpace(state)
	if state == "" {
		state = "completed"
	}
	block.SetStatus(state, "", "", occurredAt)
	if strings.TrimSpace(m.mainTimelineTailID) == strings.TrimSpace(block.BlockID()) {
		m.mainTimelineTailID = ""
	}
}
