package tuiapp

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/errorcode"
	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

func (m *Model) applySessionReconnectState(state appserver.SessionState) tea.Cmd {
	if m == nil {
		return nil
	}
	// Discard old-Session prompts without responding: completing them would
	// submit an implicit rejection to the Session that was just left.
	m.activePrompt = nil
	m.pendingPrompt = nil
	m.closeTaskStreamSubscriptions()
	m.currentSessionID = strings.TrimSpace(state.SessionID)
	m.resetSlashSkillCatalog()
	m.runningHintTracker.resetSession()
	m.resetConversationView()
	if state.Run.Active || state.Approval.Active != nil {
		m.beginLiveTurn(SubmissionModeDefault, false, state.Run.StartedAt)
		return m.resumeRunningAnimationIfNeeded()
	}
	return nil
}

func streamReconnectBackfill(
	ctx context.Context,
	reconnect controlprompt.SessionReconnect,
	send func(tea.Msg),
) error {
	if reconnect == nil {
		return nil
	}
	const batchSize = resumeReplayTranscriptBatchSize
	batch := make([]TranscriptEvent, 0, batchSize)
	published := false
	assembler := &appserver.FeedDeliveryAssembler{}
	flush := func() {
		if len(batch) == 0 || send == nil {
			batch = batch[:0]
			return
		}
		send(TranscriptEventsMsg{
			Events:          append([]TranscriptEvent(nil), batch...),
			ReconnectReplay: true,
		})
		published = true
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, open := <-reconnect.Deliveries():
			if !open {
				flush()
				return reconnect.Err()
			}
			events, replacement, err := assembler.Accept(delivery)
			if err != nil {
				return err
			}
			if replacement {
				if published {
					return errorcode.New(errorcode.Conflict, "Session replacement crossed visible reconnect output")
				}
				batch = batch[:0]
			}
			for _, envelope := range events {
				presentation := transcriptEventsMsg(projectResumeReplayEvents([]eventstream.Envelope{envelope}))
				batch = append(batch, presentation.Events...)
				if len(batch) >= batchSize {
					flush()
				}
			}
			if delivery.Kind == appserver.FeedDeliverySync {
				if assembler.Pending() {
					return errorcode.New(errorcode.Unavailable, "Session replacement ended before sync")
				}
				flush()
				return nil
			}
		}
	}
}
