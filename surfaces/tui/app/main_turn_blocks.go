package tuiapp

import (
	"strings"
	"time"
)

func (m *Model) ensureMainACPTurnBlock(turnKey string) *MainACPTurnBlock {
	if m == nil {
		return nil
	}
	turnKey = strings.TrimSpace(turnKey)
	if turnKey == "" {
		return nil
	}
	if m.mainACPTurnIDs == nil {
		m.mainACPTurnIDs = map[string]string{}
	}
	if blockID := strings.TrimSpace(m.mainACPTurnIDs[turnKey]); blockID != "" {
		if block, _ := m.doc.Find(blockID).(*MainACPTurnBlock); block != nil {
			m.activeMainACPTurnID = block.BlockID()
			return block
		}
		delete(m.mainACPTurnIDs, turnKey)
	}
	block := NewMainACPTurnBlock(turnKey)
	m.doc.Append(block)
	m.mainACPTurnIDs[turnKey] = block.BlockID()
	m.activeMainACPTurnID = block.BlockID()
	m.markViewportStructureDirty()
	return block
}

func (m *Model) releaseReplayedMainACPTurn(block *MainACPTurnBlock, occurredAt time.Time, state string) {
	if m == nil || block == nil || m.turnRunning() {
		return
	}
	state = strings.TrimSpace(state)
	if state == "" {
		state = "completed"
	}
	block.SetStatus(state, "", "", occurredAt)
	if strings.TrimSpace(m.activeMainACPTurnID) == strings.TrimSpace(block.BlockID()) {
		m.activeMainACPTurnID = ""
	}
}

func (m *Model) finalizeActiveMainACPTurn(interrupted bool, err error) {
	if m == nil {
		return
	}
	blockID := strings.TrimSpace(m.activeMainACPTurnID)
	if blockID == "" {
		return
	}
	block, _ := m.doc.Find(blockID).(*MainACPTurnBlock)
	if block == nil {
		m.activeMainACPTurnID = ""
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
	m.activeMainACPTurnID = ""
}
