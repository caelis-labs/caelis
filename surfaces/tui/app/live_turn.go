package tuiapp

import (
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/display"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

func (m *Model) turnRunning() bool {
	return m != nil && m.liveTurn.Active
}

func (m *Model) beginLiveTurn(mode SubmissionMode, divider bool, startedAt time.Time) {
	if m == nil {
		return
	}
	if startedAt.IsZero() {
		startedAt = time.Now()
	}
	m.liveTurn.Active = true
	m.liveTurn.Mode = mode
	m.liveTurn.Divider = divider
	m.liveTurn.StartedAt = startedAt
	m.liveTurn.LastDuration = 0
	m.liveTurn.HasLastDuration = false
	m.startRunningAnimation()
}

func (m *Model) stopLiveTurn() {
	if m == nil {
		return
	}
	m.liveTurn.Active = false
	m.stopRunningAnimation()
}

func (m *Model) finishLiveTurnFromEnvelope(env eventstream.Envelope) (tea.Cmd, bool) {
	if m == nil || !eventstream.IsTerminalLifecycle(env) {
		return nil, false
	}
	err := errorFromTerminalLifecycle(env)
	interrupted := liveTurnLifecycleInterrupted(env)
	cmd := m.finishLiveTurn(liveTurnEndedAt(env), interrupted, err)
	return cmd, true
}

func terminalLifecycleForTaskResult(msg TaskResultMsg, occurredAt time.Time) eventstream.Envelope {
	switch {
	case msg.Interrupted:
		reason := "interrupted"
		if msg.Err != nil {
			reason = display.UserVisibleError(msg.Err)
		}
		return eventstream.TurnCancelled("", "", "", reason, occurredAt)
	case msg.Err != nil:
		return eventstream.TurnFailed("", "", "", display.UserVisibleError(msg.Err), occurredAt)
	default:
		return eventstream.TurnCompleted("", "", "", occurredAt)
	}
}

func (m *Model) finishLiveTurn(endedAt time.Time, interrupted bool, err error) tea.Cmd {
	if m == nil {
		return nil
	}
	if !m.turnRunning() {
		return nil
	}
	if interrupted {
		m.discardActiveAssistantStream()
	} else {
		m.flushStream()
		m.finalizeAssistantBlock()
		m.finalizeReasoningBlock()
	}
	m.finalizeMainTimelineTail(interrupted, err)
	participantFooterAlreadyRendered := m.finalizeActiveParticipantTurn(interrupted, err)
	m.captureLiveTurnDuration(endedAt)
	nextPending, hasNextPending := m.pendingQueue.onTurnEnd(err == nil && !interrupted, err != nil || interrupted)
	m.runningInterruptRequested = false
	m.sandboxProgress = nil
	m.stopLiveTurn()
	m.planEntries = m.planEntries[:0]
	m.clearInputAttachments()
	m.syncTextareaChrome()
	m.clearInputOverlays()
	if err != nil && !interrupted {
		errText := strings.TrimSpace(err.Error())
		isPromptCancel := errText == "cli: input interrupted" ||
			errText == "cli: input eof" ||
			errText == PromptErrInterrupt ||
			errText == PromptErrEOF
		if !isPromptCancel {
			m.commitLine(terminalErrorLine(err))
		}
	}
	if m.liveTurn.Divider && !participantFooterAlreadyRendered {
		m.appendUserTurnDividerIfNeeded(false)
	}
	m.liveTurn.Divider = false
	m.ensureViewportLayout()
	m.syncViewportContent()
	if hasNextPending {
		_, cmd := m.submitPendingPrompt(nextPending)
		return tea.Batch(tea.ClearScreen, cmd)
	}
	return tea.ClearScreen
}

func (m *Model) captureLiveTurnDuration(endedAt time.Time) {
	if m == nil || m.liveTurn.StartedAt.IsZero() {
		return
	}
	if endedAt.IsZero() || endedAt.Before(m.liveTurn.StartedAt) {
		endedAt = time.Now()
	}
	m.liveTurn.LastDuration = endedAt.Sub(m.liveTurn.StartedAt)
	m.liveTurn.HasLastDuration = true
	m.liveTurn.StartedAt = time.Time{}
}

func (m *Model) captureLiveTurnDurationFromMainBlock(block *MainACPTurnBlock) {
	if m == nil || block == nil || m.liveTurn.HasLastDuration {
		return
	}
	if block.StartedAt.IsZero() || block.EndedAt.IsZero() || !block.EndedAt.After(block.StartedAt) {
		return
	}
	m.liveTurn.LastDuration = block.EndedAt.Sub(block.StartedAt)
	m.liveTurn.HasLastDuration = true
}

func liveTurnEndedAt(env eventstream.Envelope) time.Time {
	if !env.OccurredAt.IsZero() {
		return env.OccurredAt
	}
	return time.Now()
}

func errorFromTerminalLifecycle(env eventstream.Envelope) error {
	if env.Err != nil {
		return env.Err
	}
	if env.Kind == eventstream.KindError && strings.TrimSpace(env.Error) != "" {
		return errors.New(env.Error)
	}
	if env.Lifecycle == nil {
		return nil
	}
	state := strings.ToLower(strings.TrimSpace(env.Lifecycle.State))
	switch state {
	case eventstream.LifecycleStateFailed:
		if reason := strings.TrimSpace(env.Lifecycle.Reason); reason != "" {
			return errors.New(reason)
		}
		return errors.New("turn failed")
	default:
		return nil
	}
}

func liveTurnLifecycleInterrupted(env eventstream.Envelope) bool {
	if env.Lifecycle == nil {
		return false
	}
	state := strings.ToLower(strings.TrimSpace(env.Lifecycle.State))
	return state == eventstream.LifecycleStateInterrupted ||
		state == eventstream.LifecycleStateCancelled ||
		state == "canceled"
}
