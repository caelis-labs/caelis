package tuiapp

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const (
	defaultRendererFPS     = 60
	maximumRendererFPS     = 120
	submissionRenderFrames = 2
)

type submissionDispatchMsg struct {
	submission     Submission
	turnGeneration uint64
}

type scheduledSubmissionDispatch struct {
	turnGeneration uint64
	mode           SubmissionMode
}

func (m *Model) scheduleSubmissionDispatch(submission Submission) tea.Cmd {
	if m == nil {
		return nil
	}
	turnGeneration := m.liveTurn.generation
	m.registerSubmissionDispatch(submission, turnGeneration)
	delay := submissionRenderDelay(m.cfg.RenderFPS)
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return submissionDispatchMsg{
			submission:     cloneSubmission(submission),
			turnGeneration: turnGeneration,
		}
	})
}

func (m *Model) registerSubmissionDispatch(submission Submission, turnGeneration uint64) {
	if m == nil || submission.localID == 0 {
		return
	}
	if m.submissionDispatches == nil {
		m.submissionDispatches = make(map[uint64]scheduledSubmissionDispatch)
	}
	m.submissionDispatches[submission.localID] = scheduledSubmissionDispatch{
		turnGeneration: turnGeneration,
		mode:           submission.Mode,
	}
}

func (m *Model) takeSubmissionDispatch(msg submissionDispatchMsg) bool {
	if m == nil || msg.submission.localID == 0 {
		return false
	}
	scheduled, ok := m.submissionDispatches[msg.submission.localID]
	if !ok || scheduled.turnGeneration != msg.turnGeneration || scheduled.mode != msg.submission.Mode {
		return false
	}
	delete(m.submissionDispatches, msg.submission.localID)
	return true
}

func (m *Model) revokeSubmissionDispatchesForTurn(turnGeneration uint64) {
	if m == nil || turnGeneration == 0 {
		return
	}
	for localID, scheduled := range m.submissionDispatches {
		if scheduled.turnGeneration != turnGeneration {
			continue
		}
		delete(m.submissionDispatches, localID)
		m.pendingQueue.removeLocalID(localID)
	}
}

func (m *Model) revokeProvisionalTurnDispatch() bool {
	if m == nil || !m.turnRunning() {
		return false
	}
	turnGeneration := m.liveTurn.generation
	for _, scheduled := range m.submissionDispatches {
		if scheduled.turnGeneration == turnGeneration && scheduled.mode == SubmissionModeDefault {
			m.revokeSubmissionDispatchesForTurn(turnGeneration)
			return true
		}
	}
	return false
}

func (m *Model) revokeOverlaySubmissionDispatches() {
	if m == nil {
		return
	}
	for localID, scheduled := range m.submissionDispatches {
		if scheduled.mode == SubmissionModeOverlay {
			delete(m.submissionDispatches, localID)
		}
	}
}

func submissionRenderDelay(renderFPS int) time.Duration {
	if renderFPS <= 0 {
		renderFPS = defaultRendererFPS
	}
	if renderFPS > maximumRendererFPS {
		renderFPS = maximumRendererFPS
	}
	return time.Duration(submissionRenderFrames) * time.Second / time.Duration(renderFPS)
}

func (m *Model) handleSubmissionDispatch(msg submissionDispatchMsg) (tea.Model, tea.Cmd) {
	if m == nil || !m.takeSubmissionDispatch(msg) || m.cfg.ExecuteLine == nil {
		return m, nil
	}
	submission := cloneSubmission(msg.submission)
	if submission.Mode != SubmissionModeActiveTurn {
		return m, m.executeLineCmd(submission)
	}
	if m.turnRunning() && m.liveTurn.generation == msg.turnGeneration {
		if !m.pendingQueue.markDispatched(
			submission.localID,
			m.deferLocalUserDisplayLine(submission.Text),
			submission.Mode,
		) {
			return m, nil
		}
		return m, m.executeLineCmd(submission)
	}

	// A successful terminal may overtake the render yield. Recover only the
	// exact still-undispatched prompt and submit it as ordinary idle work. An
	// abort clears the pending entry, making this message a no-op. If another
	// Turn has started meanwhile, forceIdle queues the prompt behind that Turn
	// rather than steering a different exact target.
	pending, ok := m.pendingQueue.takeScheduled(submission.localID)
	if !ok {
		return m, nil
	}
	return m.submitPendingPromptAsIdle(pending)
}

func cloneSubmission(submission Submission) Submission {
	submission.Attachments = cloneAttachments(submission.Attachments)
	return submission
}
