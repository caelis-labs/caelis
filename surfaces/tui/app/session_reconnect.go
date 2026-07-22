package tuiapp

import (
	"context"

	tea "charm.land/bubbletea/v2"

	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

const reconnectTransientGapWarning = "Some transient output may be missing; durable history and the live session feed were restored."

func (m *Model) applySessionReconnectState(state controlclient.SessionState) tea.Cmd {
	if m == nil {
		return nil
	}
	// Discard old-Session prompts without responding: completing them would
	// submit an implicit rejection to the Session that was just left.
	m.activePrompt = nil
	m.pendingPrompt = nil
	m.resetConversationView()
	var warning tea.Cmd
	if state.TransientGap {
		warning = m.showHint(reconnectTransientGapWarning, hintOptions{
			priority: HintPriorityHigh, clearOnMessage: true, clearAfter: systemHintDuration,
		})
	}
	if state.Run.Active || state.Approval.Active != nil {
		m.beginLiveTurn(SubmissionModeDefault, false, state.Run.StartedAt)
		return tea.Batch(warning, m.resumeRunningAnimationIfNeeded())
	}
	return warning
}

func streamReconnectBackfill(
	ctx context.Context,
	reconnect control.SessionReconnect,
	send func(tea.Msg),
) error {
	if reconnect == nil {
		return nil
	}
	const batchSize = resumeReplayTranscriptBatchSize
	batch := make([]TranscriptEvent, 0, batchSize)
	flush := func() {
		if len(batch) == 0 || send == nil {
			batch = batch[:0]
			return
		}
		send(TranscriptEventsMsg{Events: append([]TranscriptEvent(nil), batch...)})
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case envelope, open := <-reconnect.Backfill():
			if !open {
				flush()
				return reconnect.Err()
			}
			batch = append(batch, projectResumeReplayEvents([]eventstream.Envelope{envelope})...)
			if len(batch) >= batchSize {
				flush()
			}
		}
	}
}
