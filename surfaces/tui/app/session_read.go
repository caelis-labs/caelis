package tuiapp

import (
	"context"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
)

type sessionReadState struct {
	sessionID      string
	sequence       uint64
	blockID        string
	narrativeTurn  string
	narrativeBlock string
	presented      uint64
	confirmed      uint64
	scheduled      bool
	writing        bool
	retryAfter     time.Time
}

type sessionReadCheckMsg struct{ generation uint64 }

type sessionReadResultMsg struct {
	generation uint64
	sequence   uint64
	err        error
}

func (m *Model) trackSessionResult(event TranscriptEvent, blockID string) {
	if m.cfg.AcknowledgeSessionRead == nil || m.currentSessionID == "" {
		return
	}
	state := &m.sessionRead
	if state.sessionID != m.currentSessionID {
		*state = sessionReadState{sessionID: m.currentSessionID}
	}
	state.narrativeTurn, state.narrativeBlock = event.TurnID, blockID
	if event.ResultSequence > state.sequence {
		state.sequence, state.blockID = event.ResultSequence, blockID
	}
}

func (m *Model) trackTerminalSessionResult(env eventstream.Envelope) {
	state := &m.sessionRead
	if env.TurnID == "" || env.TurnID != state.narrativeTurn || state.sessionID != m.currentSessionID {
		return
	}
	if seq := eventstream.TerminalSessionResultSequence(env); seq > state.sequence {
		state.sequence, state.blockID = seq, state.narrativeBlock
	}
}

func (m *Model) sessionResultCanBeRead() bool {
	return m.cfg.AcknowledgeSessionRead != nil && m.ready && m.focused &&
		m.sessionRead.sessionID == m.currentSessionID && m.sessionRead.sequence != 0 &&
		m.sessionPicker == nil && m.sessionHistory == nil && !m.sessionSwitchPending &&
		!m.sessionObservationRecovering && !m.sessionHistoryFailed && m.activePrompt == nil &&
		m.wizardOverlay == nil && m.subagentOverlay == nil && m.btwOverlay == nil &&
		!m.showPalette && !m.workspace.childFocused && m.isViewportFollowTail() &&
		(m.subagentOutputOverlay == nil || m.workspaceLayout().split)
}

func (m *Model) markRenderedSessionResult() {
	state := &m.sessionRead
	if state.sequence <= state.presented || !m.sessionResultCanBeRead() || !m.sessionResultBlockVisible() {
		return
	}
	state.presented = state.sequence
}

func (m *Model) sessionResultBlockVisible() bool {
	from := min(len(m.viewportBlockIDs), max(0, m.viewportVisibleOffset()))
	to := min(len(m.viewportBlockIDs), from+m.viewport.Height())
	return slices.Contains(m.viewportBlockIDs[from:to], m.sessionRead.blockID)
}

func (m *Model) scheduleSessionRead() tea.Cmd {
	state := &m.sessionRead
	if state.sequence <= state.confirmed || state.scheduled || state.writing || !m.sessionResultCanBeRead() ||
		state.presented <= state.confirmed && !m.sessionResultBlockVisible() {
		return nil
	}
	state.scheduled = true
	generation := m.viewGeneration
	wait := max(80*time.Millisecond, time.Until(state.retryAfter))
	return tea.Tick(wait, func(time.Time) tea.Msg { return sessionReadCheckMsg{generation} })
}

func (m *Model) acknowledgeRenderedSessionResult(msg sessionReadCheckMsg) tea.Cmd {
	if msg.generation != m.viewGeneration {
		return nil
	}
	state := &m.sessionRead
	state.scheduled = false
	if !m.sessionResultCanBeRead() || state.presented <= state.confirmed || state.writing {
		return nil
	}
	state.writing = true
	ctx, id, sequence, acknowledge := contextOrBackground(m.cfg.Context), state.sessionID, state.presented, m.cfg.AcknowledgeSessionRead
	return func() tea.Msg {
		request, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		err := acknowledge(request, id, appserver.SessionReadReceipt{ThroughSeq: sequence})
		return sessionReadResultMsg{msg.generation, sequence, err}
	}
}

func (m *Model) finishSessionRead(msg sessionReadResultMsg) {
	if msg.generation != m.viewGeneration {
		return
	}
	state := &m.sessionRead
	state.writing = false
	if msg.err != nil {
		state.retryAfter = time.Now().Add(time.Second)
		return
	}
	state.confirmed = max(state.confirmed, msg.sequence)
	state.retryAfter = time.Time{}
}
