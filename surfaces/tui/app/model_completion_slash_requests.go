package tuiapp

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Ordinary slash-argument completion uses one cancellable request per current
// command/query identity. Async catalog-backed wizard steps remain in
// model_completion_slash_args.go because they own setup and authentication.
func (m *Model) cancelSlashArgRequest() {
	if m.slashArgRequestCancel != nil {
		m.slashArgRequestCancel()
	}
	m.slashArgRequestCancel = nil
	m.slashArgRequestPending = false
	m.slashArgRequestCommand = ""
	m.slashArgRequestQuery = ""
	m.slashArgRequestSeq++
}

func (m *Model) requestCurrentSlashArgCompletion() tea.Cmd {
	if m == nil {
		return nil
	}
	command := m.currentSlashArgCompletionCommand()
	if isAsyncSlashArgCommand(command) {
		return m.beginSlashArgLoad()
	}
	return m.requestSlashArgCompletion()
}

func (m *Model) requestSlashArgCompletion() tea.Cmd {
	if m == nil || !m.slashArgActive || !m.hasSlashArgCompleter() || m.turnRunning() {
		return nil
	}
	command, query, ok := m.currentSlashArgCompletionTarget()
	if !ok || isAsyncSlashArgCommand(command) {
		return nil
	}
	if m.slashArgRequestPending && command == m.slashArgRequestCommand && query == m.slashArgRequestQuery {
		return nil
	}
	m.cancelSlashArgRequest()
	if command != m.slashArgCandidateCommand || query != m.slashArgQuery {
		m.slashArgCandidates = nil
		m.slashArgCandidateCommand = ""
		m.slashArgCompletionSettled = false
		m.slashArgQuery = query
		m.slashArgIndex = 0
	}
	requestCtx, cancel := context.WithCancel(contextOrBackground(m.cfg.Context))
	m.slashArgRequestSeq++
	seq := m.slashArgRequestSeq
	m.slashArgRequestCommand = command
	m.slashArgRequestQuery = query
	m.slashArgRequestPending = true
	m.slashArgRequestCancel = cancel
	complete := m.cfg.SlashArgComplete
	return func() tea.Msg {
		started := time.Now()
		candidates, err := complete(requestCtx, command, query, 200)
		return slashArgCompletionResultMsg{
			seq: seq, command: command, query: query,
			candidates: append([]SlashArgCandidate(nil), candidates...),
			err:        err, latency: time.Since(started),
		}
	}
}

func (m *Model) handleSlashArgCompletionResultMsg(msg slashArgCompletionResultMsg) (tea.Model, tea.Cmd) {
	if m == nil || msg.seq != m.slashArgRequestSeq || !m.slashArgRequestPending {
		return m, nil
	}
	cancel := m.slashArgRequestCancel
	m.slashArgRequestCancel = nil
	m.slashArgRequestPending = false
	if cancel != nil {
		cancel()
	}
	m.diag.LastSlashArgLatency = msg.latency
	command, query, ok := m.currentSlashArgCompletionTarget()
	if !ok || command != msg.command || query != msg.query || m.turnRunning() {
		return m, nil
	}
	m.applySlashArgCandidates(msg.command, msg.query, msg.candidates, msg.err)
	if msg.err == nil && !m.isWizardActive() && msg.command == "model use" {
		if nextCommand := exactModelUseReasoningCommandForQuery(msg.query, m.slashArgCandidates); nextCommand != "" {
			m.slashArgCommand = nextCommand
			m.slashArgCandidates = nil
			m.slashArgCandidateCommand = ""
			m.slashArgCompletionSettled = false
			m.slashArgQuery = ""
			m.slashArgIndex = 0
			return m, m.requestSlashArgCompletion()
		}
	}
	return m, nil
}
