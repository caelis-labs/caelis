package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/protocol/acp/taskstream"
)

const (
	subagentRosterRefreshInterval        = time.Second
	subagentRosterAcceptedSendRetryLimit = 4
)

type subagentRosterRefreshResultMsg struct {
	sessionID  string
	generation uint64
	tasks      []taskstream.TaskDescriptor
	err        error
}

type subagentRosterRefreshTickMsg struct {
	sessionID  string
	generation uint64
}

// requestSubagentRosterRefresh reads only the canonical Task directory. It
// does not subscribe to hidden child output and cannot advance Task lifecycle.
func (m *Model) requestSubagentRosterRefresh() tea.Cmd {
	return m.requestSubagentRosterRefreshCommand(false)
}

func (m *Model) requestSubagentRosterRefreshAfterAcceptedSend(callIDs ...string) tea.Cmd {
	return m.requestSubagentRosterRefreshCommand(true, callIDs...)
}

func (m *Model) requestSubagentRosterRefreshCommand(afterAcceptedSend bool, callIDs ...string) tea.Cmd {
	if m == nil || m.cfg.TaskStreams == nil || m.subagentRosterCount() == 0 {
		return nil
	}
	if afterAcceptedSend {
		m.markSubagentRosterRefreshWakeTargets(callIDs...)
		m.subagentRosterRefreshWake = true
		m.subagentRosterRefreshWakeRetries = 0
	}
	if m.subagentRosterRefreshPending {
		m.subagentRosterRefreshQueued = m.subagentRosterRefreshQueued || afterAcceptedSend
		return nil
	}
	if m.subagentRosterRefreshScheduled {
		return nil
	}
	sessionID := strings.TrimSpace(m.currentSessionID)
	if sessionID == "" {
		return nil
	}
	m.subagentRosterRefreshPending = true
	m.subagentRosterRefreshGeneration++
	generation := m.subagentRosterRefreshGeneration
	service := m.cfg.TaskStreams
	ctx := contextOrBackground(m.cfg.Context)
	return func() tea.Msg {
		result, err := service.List(ctx, taskstream.ListRequest{SessionID: sessionID})
		return subagentRosterRefreshResultMsg{
			sessionID: sessionID, generation: generation, tasks: result.Tasks, err: err,
		}
	}
}

func (m *Model) handleSubagentRosterRefreshResult(msg subagentRosterRefreshResultMsg) tea.Cmd {
	if m == nil || msg.sessionID != m.currentSessionID ||
		msg.generation != m.subagentRosterRefreshGeneration {
		return nil
	}
	m.subagentRosterRefreshPending = false
	if msg.err == nil {
		m.subagentRosterTasks = subagentRosterTasksByCallID(msg.tasks)
		// Directory state changes the overlay title and its empty-state copy even
		// though it deliberately does not mutate retained child transcript blocks.
		for callID, view := range m.subagentOutputViews {
			if view != nil {
				view.participantID = strings.TrimSpace(m.subagentRosterTasks[callID].ParticipantID)
				view.touch(true)
			}
		}
		// List itself remains metadata-only. Reconcile after installing the
		// directory snapshot so an already-visible workspace can attach when a
		// later SendMessage starts new activity, or detach after its cached
		// terminal history has resolved. Hidden children never gain demand here.
		m.reconcileSubagentOutputTaskStreams()
	}
	if m.subagentRosterRefreshQueued {
		m.subagentRosterRefreshQueued = false
		m.subagentRosterRefreshScheduled = false
		return m.requestSubagentRosterRefresh()
	}
	if msg.err == nil && m.subagentRosterRefreshWake {
		m.resolveSubagentRosterRefreshWakeTargets()
	}
	if m.subagentRosterRefreshWake && (msg.err == nil || taskStreamRetryable(msg.err)) {
		m.subagentRosterRefreshWakeRetries++
		if m.subagentRosterRefreshWakeRetries <= subagentRosterAcceptedSendRetryLimit {
			m.subagentRosterRefreshScheduled = true
			sessionID := msg.sessionID
			generation := msg.generation
			return tea.Tick(subagentRosterRefreshInterval, func(time.Time) tea.Msg {
				return subagentRosterRefreshTickMsg{sessionID: sessionID, generation: generation}
			})
		}
		m.clearSubagentRosterRefreshWake()
	} else {
		m.clearSubagentRosterRefreshWake()
	}
	if !m.subagentRosterHasRunning() {
		m.subagentRosterRefreshScheduled = false
		return nil
	}
	m.subagentRosterRefreshScheduled = true
	sessionID := msg.sessionID
	generation := msg.generation
	return tea.Tick(subagentRosterRefreshInterval, func(time.Time) tea.Msg {
		return subagentRosterRefreshTickMsg{sessionID: sessionID, generation: generation}
	})
}

func (m *Model) handleSubagentRosterRefreshTick(msg subagentRosterRefreshTickMsg) tea.Cmd {
	if m == nil || msg.sessionID != m.currentSessionID ||
		msg.generation != m.subagentRosterRefreshGeneration {
		return nil
	}
	m.subagentRosterRefreshScheduled = false
	return m.requestSubagentRosterRefresh()
}

func (m *Model) resetSubagentRosterRefresh() {
	if m == nil {
		return
	}
	m.subagentRosterRefreshGeneration++
	m.subagentRosterRefreshPending = false
	m.subagentRosterRefreshQueued = false
	m.subagentRosterRefreshScheduled = false
	m.subagentRosterRefreshWake = false
	m.subagentRosterRefreshWakeRetries = 0
	m.subagentRosterRefreshWakeTargets = nil
	m.subagentRosterTasks = map[string]taskstream.TaskDescriptor{}
}

func (m *Model) markSubagentRosterRefreshWakeTargets(callIDs ...string) {
	if m == nil {
		return
	}
	if len(callIDs) == 0 {
		for callID := range m.subagentOutputViews {
			callIDs = append(callIDs, callID)
		}
	}
	if m.subagentRosterRefreshWakeTargets == nil {
		m.subagentRosterRefreshWakeTargets = map[string]string{}
	}
	for _, callID := range callIDs {
		callID = strings.TrimSpace(callID)
		if callID == "" {
			continue
		}
		baseline := strings.TrimSpace(m.subagentRosterTasks[callID].CurrentTurnID)
		if baseline == "" {
			if view := m.subagentOutputViews[callID]; view != nil && view.block != nil {
				baseline = strings.TrimSpace(view.block.SessionID)
			}
		}
		m.subagentRosterRefreshWakeTargets[callID] = baseline
	}
}

func (m *Model) resolveSubagentRosterRefreshWakeTargets() {
	if m == nil {
		return
	}
	for callID, baseline := range m.subagentRosterRefreshWakeTargets {
		descriptor, ok := m.subagentRosterTasks[callID]
		turnID := strings.TrimSpace(descriptor.CurrentTurnID)
		if ok && (descriptor.Running || (turnID != "" && turnID != strings.TrimSpace(baseline))) {
			delete(m.subagentRosterRefreshWakeTargets, callID)
		}
	}
	if len(m.subagentRosterRefreshWakeTargets) == 0 {
		m.subagentRosterRefreshWake = false
	}
}

func (m *Model) clearSubagentRosterRefreshWake() {
	if m == nil {
		return
	}
	m.subagentRosterRefreshWake = false
	m.subagentRosterRefreshWakeRetries = 0
	m.subagentRosterRefreshWakeTargets = nil
}

func (m *Model) subagentRosterViewState(callID string, view *subagentOutputView) (subagentOutputStatus, time.Time, time.Time) {
	status := subagentOutputRunning
	var startedAt time.Time
	var endedAt time.Time
	if view != nil && view.block != nil {
		status = subagentOutputStatusFromState(view.block.Status)
		startedAt = view.block.StartedAt
		endedAt = view.block.EndedAt
	}
	descriptor, ok := m.subagentRosterTasks[strings.TrimSpace(callID)]
	if !ok {
		return status, startedAt, endedAt
	}
	descriptorStatus := subagentOutputStatusFromState(string(descriptor.State))
	if descriptor.Running {
		descriptorStatus = subagentOutputRunning
	}
	if descriptorStatus == status {
		if !subagentRosterDescriptorIsNewActivity(descriptor, view) {
			return status, startedAt, endedAt
		}
	} else if !subagentRosterDescriptorSupersedesView(descriptor, view) {
		return status, startedAt, endedAt
	}
	if descriptorStatus == subagentOutputRunning {
		return descriptorStatus, descriptor.UpdatedAt, time.Time{}
	}
	return descriptorStatus, time.Time{}, descriptor.UpdatedAt
}

func subagentRosterDescriptorIsNewActivity(descriptor taskstream.TaskDescriptor, view *subagentOutputView) bool {
	if view == nil || view.block == nil {
		return true
	}
	descriptorTurnID := strings.TrimSpace(descriptor.CurrentTurnID)
	viewTurnID := strings.TrimSpace(view.block.SessionID)
	return descriptorTurnID != "" && viewTurnID != "" && descriptorTurnID != viewTurnID &&
		subagentRosterDescriptorSupersedesView(descriptor, view)
}

func subagentRosterDescriptorSupersedesView(descriptor taskstream.TaskDescriptor, view *subagentOutputView) bool {
	if view == nil || view.block == nil {
		return true
	}
	// A replayed Spawn with no child transcript is only a provisional
	// presentation shell. It has no lifecycle authority, so the first canonical
	// Task directory observation must replace it even if the shell was rebuilt
	// with a newer local timestamp after process restart.
	if view.turnID == "" && !subagentOutputViewHasTranscript(view) {
		return true
	}
	viewUpdatedAt := view.block.StartedAt
	if !view.block.EndedAt.IsZero() {
		viewUpdatedAt = view.block.EndedAt
	}
	if !descriptor.UpdatedAt.IsZero() && !viewUpdatedAt.IsZero() {
		if descriptor.UpdatedAt.After(viewUpdatedAt) {
			return true
		}
		if descriptor.UpdatedAt.Before(viewUpdatedAt) {
			return false
		}
	}
	descriptorTurnID := strings.TrimSpace(descriptor.CurrentTurnID)
	viewTurnID := strings.TrimSpace(view.block.SessionID)
	return descriptorTurnID != "" && viewTurnID != "" && descriptorTurnID != viewTurnID
}

func (m *Model) subagentRosterHasRunning() bool {
	return m != nil && m.subagentRosterRunningCount() > 0
}

func subagentRosterTasksByCallID(tasks []taskstream.TaskDescriptor) map[string]taskstream.TaskDescriptor {
	byCallID := make(map[string]taskstream.TaskDescriptor)
	ambiguous := make(map[string]bool)
	for _, descriptor := range tasks {
		callID := strings.TrimSpace(descriptor.ParentTool.ToolCallID)
		if callID == "" || strings.TrimSpace(string(descriptor.Kind)) != "subagent" || ambiguous[callID] {
			continue
		}
		if existing, ok := byCallID[callID]; ok &&
			strings.TrimSpace(existing.TaskID) != strings.TrimSpace(descriptor.TaskID) {
			delete(byCallID, callID)
			ambiguous[callID] = true
			continue
		}
		byCallID[callID] = descriptor
	}
	return byCallID
}
