package tuiapp

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	controlclient "github.com/caelis-labs/caelis/control/client"
	"github.com/caelis-labs/caelis/protocol/acp/control"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	acpprojector "github.com/caelis-labs/caelis/protocol/acp/projector"
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
	m.closeTaskStreamSubscriptions()
	m.currentSessionID = strings.TrimSpace(state.SessionID)
	m.runningActivityTracker.resetSession()
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
	ownerRepairs := acpprojector.TaskOwnerRepairs{}
	flush := func() {
		if (len(batch) == 0 && ownerRepairs.Empty()) || send == nil {
			batch = batch[:0]
			ownerRepairs = acpprojector.TaskOwnerRepairs{}
			return
		}
		send(TranscriptEventsMsg{
			Events:       append([]TranscriptEvent(nil), batch...),
			OwnerRepairs: ownerRepairs.Clone(),
		})
		batch = batch[:0]
		ownerRepairs = acpprojector.TaskOwnerRepairs{}
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
			presentation := transcriptEventsMsgForEnvelope(
				projectResumeReplayEvents([]eventstream.Envelope{envelope}),
				envelope,
			)
			batch = append(batch, presentation.Events...)
			ownerRepairs.Append(presentation.OwnerRepairs)
			// Preserve live ordering when a producer reuses a parent tool call
			// ID in a later turn: apply each terminal observation before
			// replaying any subsequent owner with that ID.
			if !ownerRepairs.Empty() || len(batch) >= batchSize {
				flush()
			}
		}
	}
}
