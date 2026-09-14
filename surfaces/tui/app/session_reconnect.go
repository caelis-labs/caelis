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
	m.saveSessionDraft()
	// Discard old-Session prompts without responding: completing them would
	// submit an implicit rejection to the Session that was just left.
	if m.activePrompt != nil && m.activePrompt.dismiss != nil {
		m.activePrompt.dismiss()
	}
	for _, prompt := range m.pendingPrompt {
		if prompt.dismiss != nil {
			prompt.dismiss()
		}
	}
	m.activePrompt = nil
	// A local theme preview belongs to the TUI and survives Session changes.
	if m.themePicker != nil {
		m.activePrompt = m.themePicker.prompt
	}
	m.pendingPrompt = nil
	m.closeTaskStreamSubscriptions()
	m.currentSessionID = strings.TrimSpace(state.SessionID)
	m.resetSlashSkillCatalog()
	m.runningHintTracker.resetSession()
	m.resetConversationView()
	m.restoreSessionDraft()
	m.statusRefreshInFlight = false
	m.clearInputOverlays()
	if state.Run.Active || state.Approval.Active != nil {
		m.beginLiveTurn(SubmissionModeDefault, true, state.Run.StartedAt)
		m.liveTurn.observed = m.viewGeneration != 0
		return m.resumeRunningAnimationIfNeeded()
	}
	return nil
}

func streamReconnectBackfill(
	ctx context.Context,
	reconnect controlprompt.SessionReconnect,
	send func(tea.Msg),
) error {
	return streamReconnectBackfillFrom(ctx, reconnect, send, nil)
}

func streamReconnectBackfillFrom(ctx context.Context, reconnect interface {
	Deliveries() <-chan appserver.FeedDelivery
	Err() error
}, send func(tea.Msg), first *appserver.FeedDelivery) error {
	if reconnect == nil {
		return nil
	}
	const batchSize = resumeReplayTranscriptBatchSize
	batch := make([]TranscriptEvent, 0, batchSize)
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
		batch = batch[:0]
	}
	for {
		var delivery appserver.FeedDelivery
		open := true
		if first != nil {
			delivery = *first
			first = nil
		} else {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case delivery, open = <-reconnect.Deliveries():
			}
		}
		if !open {
			if err := reconnect.Err(); err != nil {
				return err
			}
			return errorcode.New(errorcode.Unavailable, "Session history ended before sync")
		}
		if delivery.Kind == appserver.FeedDeliveryReplaceBegin {
			batch = batch[:0]
			if send != nil {
				send(sessionHistoryResetMsg{})
			}
		}
		events, _, err := assembler.AcceptPage(delivery)
		if err != nil {
			return err
		}

		for _, envelope := range events {
			if eventstream.IsSessionNotice(envelope) {
				// The exact spool can contain notices published during first
				// admission. Present them in order without making them replayable
				// canonical history or advancing the live Turn target.
				flush()
				if send != nil {
					send(envelope)
				}
				continue
			}
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
			if send != nil && delivery.HistoryBefore != "" {
				send(sessionHistoryPositionMsg{before: delivery.HistoryBefore})
			}
			return nil
		}
	}
}
