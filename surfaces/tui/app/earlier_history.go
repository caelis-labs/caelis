package tuiapp

import (
	"context"
	"errors"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

type sessionHistoryLoader interface {
	LoadSessionHistory(context.Context, string, string) (appserver.FeedSubscription, error)
}

type earlierHistoryBuild struct {
	generation uint64
	before     string
	cancel     context.CancelFunc
	model      *Model
	child      *subagentOutputView
	next       string
}

type earlierHistoryMsg struct {
	callID  string
	build   *earlierHistoryBuild
	message tea.Msg
	done    bool
	err     error
}

// Called by transcript scroll handlers, never by composer or picker navigation.
func (m *Model) demandEarlierHistory(callID string) {
	if m.sessionHistory != nil || m.sessionSwitchPending || m.sessionHistoryFailed || m.cfg.ProgramSender == nil {
		return
	}
	if callID != "" {
		pane := m.subagentOutputOverlay
		if pane == nil || pane.callID != callID || pane.offset > max(1, len(pane.geometry.rowTokens)) {
			return
		}
		if view := m.subagentOutputViews[callID]; view != nil && view.history == nil && view.historyBefore != "" {
			m.startEarlierHistory(callID, view.historyBefore)
		}
		return
	}
	if m.viewportVisibleOffset() <= max(1, m.viewport.Height()) && m.sessionHistoryBefore != "" {
		m.setViewportFollowState(viewportPinnedHistory)
		m.startEarlierHistory("", m.sessionHistoryBefore)
	}
}

func (m *Model) cancelEarlierHistory(callID string) {
	for key, build := range m.earlierHistory {
		if callID == "" || key == callID {
			build.cancel()
			delete(m.earlierHistory, key)
		}
	}
}

func (m *Model) startEarlierHistory(callID, before string) {
	if m.earlierHistory[callID] != nil {
		return
	}
	cfg := m.cfg
	if cfg.ProgramSender == nil {
		return
	}
	loader, ok := cfg.ControlService.(sessionHistoryLoader)
	if callID == "" && !ok || callID != "" && cfg.TaskStreams == nil {
		return
	}
	ctx, cancel := context.WithTimeout(cfg.ProgramSender.observationContext(cfg.Context), 30*time.Second)
	build := &earlierHistoryBuild{generation: m.viewGeneration, before: before, cancel: cancel}
	taskID := ""
	if callID == "" {
		cfg.ProgramSender, cfg.TaskStreams, cfg.ControlService = nil, nil, nil
		cfg.NoAnimation, cfg.ShowWelcomeCard = true, false
		build.model = newModelWithTheme(cfg, m.theme)
		build.model.historyBuilding = true
		build.model.beginDeferredViewportSync()
	} else {
		view := m.subagentOutputViews[callID]
		if view == nil {
			cancel()
			return
		}
		child := *view
		child.history, child.pane = nil, nil
		child.resetForReplacement()
		build.child = &child
		taskID = m.taskStreamIDsByCallID[callID]
		if taskID == "" {
			cancel()
			return
		}
	}
	if m.earlierHistory == nil {
		m.earlierHistory = map[string]*earlierHistoryBuild{}
	}
	m.earlierHistory[callID] = build
	sender := m.cfg.ProgramSender
	client := m.cfg.TaskStreams
	sessionID := m.currentSessionID
	send := func(message tea.Msg) {
		sender.SendMsg(earlierHistoryMsg{callID: callID, build: build, message: message})
	}
	if !sender.startForwarder(func() {
		defer cancel()
		var err error
		if callID == "" {
			var sub appserver.FeedSubscription
			sub, err = loader.LoadSessionHistory(ctx, sessionID, before)
			if err == nil {
				if sub == nil {
					err = errors.New("session history subscription unavailable")
				} else {
					defer sub.Close()
					err = streamReconnectBackfillFrom(ctx, sub, send, nil)
				}
			}
		} else {
			var result taskstream.SubscribeResult
			result, err = client.Subscribe(ctx, taskstream.SubscribeRequest{SessionID: sessionID, TaskID: taskID, HistoryBefore: before, HistoryTurns: 16, HistorySnapshot: true})
			if err == nil {
				if result.Subscription == nil {
					err = errors.New("child history subscription unavailable")
				} else {
					defer result.Subscription.Close()
					err = readEarlierChildHistory(ctx, result.Subscription, send)
				}
			}
		}
		sender.SendMsg(earlierHistoryMsg{callID: callID, build: build, done: true, err: err})
	}) {
		cancel()
		delete(m.earlierHistory, callID)
	}
}

func readEarlierChildHistory(ctx context.Context, sub taskstream.Subscription, send func(tea.Msg)) error {
	assembler := &taskstream.DeliveryAssembler{}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case delivery, open := <-sub.Deliveries():
			if !open {
				if err := sub.Err(); err != nil {
					return err
				}
				return errors.New("child history ended before commit")
			}
			events, _, err := assembler.AcceptPage(delivery)
			if err != nil {
				return err
			}
			if delivery.Kind == taskstream.DeliveryAppendPage || delivery.Kind == taskstream.DeliveryStatus {
				return errors.New("child history did not provide an atomic window")
			}
			send(taskStreamBatchMsg{events: events, phase: delivery.Kind, before: delivery.HistoryBefore})
			if delivery.Kind == taskstream.DeliveryReplaceEnd {
				return nil
			}
		}
	}
}

func (m *Model) handleEarlierHistory(msg earlierHistoryMsg) tea.Cmd {
	build := m.earlierHistory[msg.callID]
	if build == nil || build != msg.build || build.generation != m.viewGeneration {
		return nil
	}
	if msg.done {
		delete(m.earlierHistory, msg.callID)
		build.cancel()
		if msg.err != nil {
			return m.showHint("Earlier history: "+msg.err.Error(), hintOptions{priority: HintPriorityHigh})
		}
		if msg.callID == "" {
			if m.sessionHistoryBefore != build.before {
				return nil
			}
			m.prependSessionHistory(build.model)
			m.sessionHistoryBefore = build.next
		} else if view := m.subagentOutputViews[msg.callID]; view != nil && view.historyBefore == build.before {
			m.prependChildHistory(view, build.child)
			view.historyBefore = build.next
		}
		return nil
	}
	switch event := msg.message.(type) {
	case sessionHistoryPositionMsg:
		build.next = event.before
	case TranscriptEventsMsg:
		if build.model != nil {
			build.model.handleTranscriptEventsMsg(event)
		}
	case eventstream.Envelope:
		if build.model != nil {
			build.model.handleACPEventEnvelope(event)
		}
	case sessionHistoryResetMsg: // The private build is empty at the initial boundary.
	case taskStreamBatchMsg:
		build.next = event.before
		for _, envelope := range event.events {
			for _, projection := range m.expandCollaborationMessages(m.projectACPEventToTranscriptEvents(envelope)) {
				if m.subagentOutputEventKey(projection) == msg.callID {
					build.child.observeChildEvent(projection)
				}
			}
		}
	}
	return nil
}
