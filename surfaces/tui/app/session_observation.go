package tuiapp

import (
	"context"
	"errors"
	"strings"

	tea "charm.land/bubbletea/v2"

	appserver "github.com/caelis-labs/caelis/control/appserver"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/internal/controlprompt"
)

// sessionViewMessage fences all asynchronous presentation output, including
// messages already queued in Bubble Tea when another Session is selected.
type sessionViewMessage struct {
	generation uint64
	message    tea.Msg
}

type sessionViewStartMsg struct {
	generation  uint64
	state       appserver.SessionState
	automatic   bool
	replacement bool
	recovery    bool
}

type sessionObservationErrorMsg struct{ err error }

func (s *ProgramSender) sessionView() (string, uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.viewSessionID, s.viewGeneration
}

func (s *ProgramSender) sessionSend(generation uint64) func(tea.Msg) {
	return func(msg tea.Msg) {
		if msg != nil {
			s.SendMsg(sessionViewMessage{generation: generation, message: msg})
		}
	}
}

func (s *ProgramSender) replaceSessionView(ctx context.Context, sessionID string) (context.Context, uint64) {
	viewCtx, cancel := context.WithCancel(s.observationContext(ctx))
	s.mu.Lock()
	previous := s.viewCancel
	s.viewCancel = cancel
	s.viewSessionID = sessionID
	s.viewGeneration++
	generation := s.viewGeneration
	s.mu.Unlock()
	if previous != nil {
		previous()
	}
	return viewCtx, generation
}

func (s *ProgramSender) releaseSessionView(generation uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.viewGeneration == generation {
		s.viewSessionID = ""
		if s.viewCancel != nil {
			s.viewCancel()
			s.viewCancel = nil
		}
	}
}

// observeSelectedSession owns one Session feed until the view is replaced or
// the terminal exits. A Turn terminal updates the view but does not detach it.
func observeSelectedSession(ctx context.Context, sender *ProgramSender, reconnect controlprompt.SessionReconnect, automatic bool) executeLineResult {
	state := reconnect.State()
	viewCtx, generation := sender.replaceSessionView(ctx, state.SessionID)
	sender.SendMsg(sessionViewStartMsg{generation: generation, state: state, automatic: automatic})
	observer := &sessionObserver{sender: sender, ctx: viewCtx, generation: generation, reconnect: reconnect, sessionID: state.SessionID, automatic: automatic}
	err := observer.backfill(nil)
	if !sender.startForwarder(func() { observer.run(err) }) {
		_ = reconnect.Close()
		sender.releaseSessionView(generation)
	}
	return executeLineResult{queued: true}
}

// attachAdmittedSession changes only observation. The Turn's admission feed
// is released after its receipt; selected Session output has a single owner.
func attachAdmittedSession(ctx context.Context, service ControlServices, sender *ProgramSender, turn controlprompt.Turn) (executeLineResult, bool) {
	addressed, ok := service.(interface{ SessionID() string })
	if !ok || sender == nil {
		return executeLineResult{}, false
	}
	defer turn.Close()
	sessionID := addressed.SessionID()
	selected, _ := sender.sessionView()
	if sessionID == selected {
		return executeLineResult{queued: true}, true
	}
	snapshot, err := service.ResumeSession(ctx, sessionID)
	if err != nil {
		return executeLineResult{completion: TaskResultMsg{Err: err, ContinueRunning: true}}, true
	}
	if snapshot.Reconnect == nil {
		return executeLineResult{completion: TaskResultMsg{Err: errors.New("session attach returned no observation"), ContinueRunning: true}}, true
	}
	return observeSelectedSession(ctx, sender, snapshot.Reconnect, true), true
}

func (m *Model) dismissApproval(requestID string) {
	if requestID == "" {
		return
	}
	pending := m.pendingPrompt[:0]
	for _, req := range m.pendingPrompt {
		if req.ApprovalRequestID != requestID {
			pending = append(pending, req)
		} else if req.dismiss != nil {
			req.dismiss()
		}
	}
	clear(m.pendingPrompt[len(pending):])
	m.pendingPrompt = pending
	if m.activePrompt != nil && m.activePrompt.approvalRequestID == requestID {
		if m.activePrompt.dismiss != nil {
			m.activePrompt.dismiss()
		}
		m.activePrompt = nil
		if len(m.pendingPrompt) > 0 {
			m.activePrompt = newPromptState(m.pendingPrompt[0])
			m.pendingPrompt = m.pendingPrompt[1:]
		}
		m.ensureViewportLayout()
		m.syncViewportContent()
	}
}

func (m *Model) hasApprovalPrompt(requestID string) bool {
	if m.activePrompt != nil && m.activePrompt.approvalRequestID == requestID {
		return true
	}
	for _, req := range m.pendingPrompt {
		if req.ApprovalRequestID == requestID {
			return true
		}
	}
	return false
}

func approvalSettled(env eventstream.Envelope) bool {
	return strings.TrimSpace(string(env.ApprovalRequestID)) != "" &&
		env.Kind == eventstream.KindLifecycle && env.Lifecycle != nil &&
		eventstream.IsTerminalLifecycleState(env.Lifecycle.State)
}
