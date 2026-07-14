package tuiapp

import (
	"strings"

	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
	"github.com/charmbracelet/x/ansi"
)

func (m *Model) resetConversationView() {
	m.flushStream()
	m.statusContext = ""
	m.statusView.Tokens = ""
	m.activeAssistantID = ""
	m.activeReasoningID = ""
	m.transientBlockID = ""
	m.mainTimelineTailID = ""
	m.mainAnchorBlockIDs = nil
	m.participantTurnIDs = nil
	m.activeParticipantTurnSessionID = ""
	m.doc.Clear()
	m.viewportStyledLines = m.viewportStyledLines[:0]
	m.viewportPlainLines = m.viewportPlainLines[:0]
	m.hasCommittedLine = false
	m.lastCommittedStyle = tuikit.LineStyleDefault
	m.lastCommittedRaw = ""
	m.transientIsRetry = false
	m.pendingQueue = nil
	m.hintEntries = nil
	m.hint = ""
	m.liveTurn = liveTurnState{}
	m.clearSelection()
	m.clearInputSelection()
	m.setViewportFollowState(viewportFollowTail)
	if m.cfg.ShowWelcomeCard {
		if m.viewport.Width() > 0 {
			m.appendWelcomeCard()
			m.welcomeCardPending = false
		} else {
			m.welcomeCardPending = true
		}
	}
	m.ensureViewportLayout()
	m.syncViewportContent()
}

func (m *Model) refreshHistoryTailState() {
	m.lastCommittedStyle = tuikit.LineStyleDefault
	m.lastCommittedRaw = ""
	m.hasCommittedLine = false
	blocks := m.doc.Blocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		if ub, ok := blocks[i].(*UserNarrativeBlock); ok {
			raw := "▌ " + strings.TrimSpace(ub.Raw)
			if strings.TrimSpace(raw) == "▌" {
				continue
			}
			m.lastCommittedRaw = raw
			m.lastCommittedStyle = tuikit.LineStyleUser
			m.hasCommittedLine = true
			return
		}
		tb, ok := blocks[i].(*TranscriptBlock)
		if !ok {
			// Non-transcript blocks (assistant, diff, etc.) count as committed content.
			m.hasCommittedLine = true
			continue
		}
		raw := tb.Raw
		if strings.TrimSpace(raw) == "" {
			continue
		}
		m.lastCommittedRaw = raw
		m.lastCommittedStyle = tuikit.DetectLineStyle(raw)
		m.hasCommittedLine = true
		return
	}
}

// commitLine colorizes one complete line and appends it to the document.
func (m *Model) commitLine(line string) {
	if strings.TrimSpace(line) == "" && !m.hasCommittedLine {
		return
	}

	style := tuikit.DetectLineStyleWithContext(line, m.lastCommittedStyle)
	isEphemeralWarn := isTransientWarningLine(line)
	isRetry := tuikit.IsRetryLine(line) && !isEphemeralWarn
	isWarn := !isRetry && style == tuikit.LineStyleWarn

	// --- Transient log replacement ---
	if isRetry && m.transientBlockID != "" && m.transientIsRetry {
		if tb := m.findTranscriptBlock(m.transientBlockID); tb != nil {
			tb.Raw = line
			tb.Style = style
			m.lastCommittedStyle = style
			m.lastCommittedRaw = line
			m.transientRemove = false
			return
		}
	}
	if isWarn && m.transientBlockID != "" && !m.transientIsRetry {
		if tb := m.findTranscriptBlock(m.transientBlockID); tb != nil {
			tb.Raw = line
			tb.Style = style
			m.lastCommittedStyle = style
			m.lastCommittedRaw = line
			m.transientRemove = isEphemeralWarn
			return
		}
	}

	if m.transientBlockID != "" && m.transientRemove {
		m.removeTransientLogLine()
	}

	m.transientBlockID = ""
	m.transientRemove = false

	if m.hasCommittedLine {
		m.insertSpacing(style, line)
	}

	block := NewTranscriptBlock(line, style)
	m.doc.Append(block)

	if isRetry {
		m.transientBlockID = block.BlockID()
		m.transientIsRetry = true
		m.transientRemove = false
	} else if isWarn {
		m.transientBlockID = block.BlockID()
		m.transientIsRetry = false
		m.transientRemove = isEphemeralWarn
	}

	m.lastCommittedStyle = style
	m.lastCommittedRaw = line
	m.hasCommittedLine = true
}

func (m *Model) findTranscriptBlock(id string) *TranscriptBlock {
	b := m.doc.Find(id)
	if b == nil {
		return nil
	}
	tb, ok := b.(*TranscriptBlock)
	if !ok {
		return nil
	}
	return tb
}

func isTransientWarningLine(line string) bool {
	normalized := strings.ToLower(strings.TrimSpace(ansi.Strip(line)))
	if normalized == "" {
		return false
	}
	if strings.Contains(normalized, "rate limit") || strings.Contains(normalized, "too many requests") {
		return true
	}
	if strings.Contains(normalized, "retrying in") && strings.Contains(normalized, "waiting longer before retrying") {
		return true
	}
	return false
}

func (m *Model) removeTransientLogLine() {
	if m.transientBlockID == "" {
		return
	}
	m.doc.Remove(m.transientBlockID)
	m.transientBlockID = ""
	m.transientRemove = false
	m.refreshHistoryTailState()
}

func (m *Model) insertSpacing(style tuikit.LineStyle, line string) {
	if m.doc.Len() == 0 {
		return
	}
	if strings.TrimSpace(line) == "" {
		return
	}
	if strings.TrimSpace(m.lastCommittedRaw) == "" {
		return
	}
	// Check if last block already produces empty content.
	last := m.doc.Last()
	if last != nil {
		if tb, ok := last.(*TranscriptBlock); ok && strings.TrimSpace(tb.Raw) == "" {
			return
		}
	}
	if shouldInsertBlockGap(m.lastCommittedStyle, style) {
		m.doc.Append(NewSpacerBlock())
	}
}

func shouldInsertBlockGap(prev tuikit.LineStyle, current tuikit.LineStyle) bool {
	if prev == tuikit.LineStyleDefault || current == tuikit.LineStyleDefault {
		return false
	}
	if current == tuikit.LineStyleUser {
		return true
	}
	if prev == tuikit.LineStyleUser && current == tuikit.LineStyleSection {
		return true
	}
	return false
}

// flushStream commits any remaining partial line in the stream buffer.
func (m *Model) flushStream() {
	if strings.TrimSpace(m.streamLine) == "" {
		m.streamLine = ""
		m.logStreamBuffer.Reset()
		return
	}
	m.commitLine(m.streamLine)
	m.streamLine = ""
	m.logStreamBuffer.Reset()
}
