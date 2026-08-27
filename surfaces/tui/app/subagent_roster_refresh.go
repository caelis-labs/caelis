package tuiapp

import (
	"context"
	"errors"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/control/appserver/taskstream"
)

type subagentDirectoryOpenedMsg struct {
	sessionID    string
	generation   uint64
	subscription taskstream.DirectorySubscription
}

type subagentDirectorySnapshotMsg struct {
	sessionID  string
	generation uint64
	snapshot   taskstream.DirectorySnapshot
}

type subagentDirectoryClosedMsg struct {
	sessionID  string
	generation uint64
	err        error
}

type subagentDirectoryRetryMsg struct {
	sessionID  string
	generation uint64
}

// ensureSubagentDirectoryWatch attaches one lightweight Session status
// observer for this TUI. It never subscribes to child content or advances Task
// lifecycle; visible overlays reconcile their own independent content demand
// from the resulting snapshots.
func (m *Model) ensureSubagentDirectoryWatch() tea.Cmd {
	if m == nil || m.cfg.TaskStreams == nil || m.cfg.ProgramSender == nil ||
		m.subagentRosterCount() == 0 || m.subagentDirectoryStarting ||
		m.subagentDirectorySubscription != nil || m.subagentDirectoryRetryScheduled {
		return nil
	}
	directory, ok := m.cfg.TaskStreams.(taskstream.DirectoryClient)
	if !ok {
		return nil
	}
	sessionID := strings.TrimSpace(m.currentSessionID)
	if sessionID == "" {
		return nil
	}

	m.subagentDirectoryGeneration++
	generation := m.subagentDirectoryGeneration
	ctx, cancel := context.WithCancel(m.cfg.ProgramSender.observationContext(m.cfg.Context))
	m.subagentDirectoryCancel = cancel
	m.subagentDirectoryStarting = true
	cfg := m.cfg
	if !cfg.ProgramSender.startForwarder(func() {
		result, err := directory.WatchDirectory(ctx, taskstream.DirectoryWatchRequest{SessionID: sessionID})
		if err != nil {
			cfg.ProgramSender.SendMsg(subagentDirectoryClosedMsg{sessionID: sessionID, generation: generation, err: err})
			return
		}
		subscription := result.Subscription
		cfg.ProgramSender.SendMsg(subagentDirectoryOpenedMsg{
			sessionID: sessionID, generation: generation, subscription: subscription,
		})
		if subscription == nil {
			cfg.ProgramSender.SendMsg(subagentDirectoryClosedMsg{
				sessionID: sessionID, generation: generation,
				err: errors.New("task directory subscription is unavailable"),
			})
			return
		}
		defer subscription.Close()
		for {
			snapshot, open := latestSubagentDirectorySnapshot(ctx, subscription.Snapshots())
			if !open {
				cfg.ProgramSender.SendMsg(subagentDirectoryClosedMsg{
					sessionID: sessionID, generation: generation, err: subscription.Err(),
				})
				return
			}
			cfg.ProgramSender.SendMsg(subagentDirectorySnapshotMsg{
				sessionID: sessionID, generation: generation, snapshot: snapshot,
			})
		}
	}) {
		cancel()
		m.subagentDirectoryCancel = nil
		m.subagentDirectoryStarting = false
	}
	return nil
}

func latestSubagentDirectorySnapshot(
	ctx context.Context,
	snapshots <-chan taskstream.DirectorySnapshot,
) (taskstream.DirectorySnapshot, bool) {
	select {
	case <-ctx.Done():
		return taskstream.DirectorySnapshot{}, false
	case snapshot, open := <-snapshots:
		if !open {
			return taskstream.DirectorySnapshot{}, false
		}
		for {
			select {
			case newer, newerOpen := <-snapshots:
				if !newerOpen {
					return snapshot, true
				}
				snapshot = newer
			default:
				return snapshot, true
			}
		}
	}
}

func (m *Model) handleSubagentDirectoryOpened(msg subagentDirectoryOpenedMsg) tea.Cmd {
	if m == nil || msg.subscription == nil {
		return nil
	}
	if msg.sessionID != m.currentSessionID || msg.generation != m.subagentDirectoryGeneration {
		_ = msg.subscription.Close()
		return nil
	}
	if previous := m.subagentDirectorySubscription; previous != nil && previous != msg.subscription {
		_ = previous.Close()
	}
	m.subagentDirectoryStarting = false
	m.subagentDirectorySubscription = msg.subscription
	return nil
}

func (m *Model) handleSubagentDirectorySnapshot(msg subagentDirectorySnapshotMsg) tea.Cmd {
	if m == nil || msg.sessionID != m.currentSessionID ||
		msg.generation != m.subagentDirectoryGeneration {
		return nil
	}
	if m.subagentDirectoryRevision != 0 && msg.snapshot.Revision <= m.subagentDirectoryRevision {
		return nil
	}
	m.subagentDirectoryRevision = msg.snapshot.Revision
	m.subagentDirectoryRetries = 0
	m.subagentRosterTasks = subagentRosterTasksByCallID(msg.snapshot.Tasks)
	for callID, view := range m.subagentOutputViews {
		if view == nil {
			continue
		}
		descriptor, ok := m.subagentRosterTasks[callID]
		if !ok {
			continue
		}
		activityID := subagentRosterDescriptorActivityID(descriptor)
		newActivity := subagentRosterDescriptorIsNewActivity(descriptor, view)
		view.directoryActivityID = activityID
		if descriptor.Running || newActivity ||
			(view.idleHistorySettled && view.idleHistoryActivityID != activityID) {
			view.idleHistorySettled = false
			view.idleHistoryActivityID = ""
		}
		if newActivity {
			m.cancelTaskStreamHistoryForCallID(callID)
		}
		if participantID := strings.TrimSpace(descriptor.ParticipantID); participantID != "" {
			view.participantID = participantID
		}
		view.touch(true)
	}
	// Directory metadata never mutates transcript content. It only starts or
	// stops the content subscription owned by an already-visible overlay.
	m.reconcileSubagentOutputTaskStreams()
	return m.requestSubagentOutputRender()
}

func (m *Model) handleSubagentDirectoryClosed(msg subagentDirectoryClosedMsg) tea.Cmd {
	if m == nil || msg.sessionID != m.currentSessionID ||
		msg.generation != m.subagentDirectoryGeneration {
		return nil
	}
	m.subagentDirectoryStarting = false
	m.subagentDirectorySubscription = nil
	if m.subagentDirectoryCancel != nil {
		m.subagentDirectoryCancel()
		m.subagentDirectoryCancel = nil
	}
	// Directory revisions belong to one observation lifetime. Control releases
	// the Session index after its final observer leaves, so a reconnect may
	// legitimately restart at revision 1. Invalidate the closed generation as
	// well, preventing already-queued snapshots from that connection from
	// racing the replacement snapshot back into the model.
	m.subagentDirectoryRevision = 0
	m.subagentDirectoryGeneration++
	retryGeneration := m.subagentDirectoryGeneration
	if m.subagentRosterCount() == 0 || errors.Is(msg.err, context.Canceled) {
		return nil
	}
	if msg.err != nil && !taskStreamRetryable(msg.err) {
		return m.showHint(taskStreamUnavailableHint("subagent status", msg.err), hintOptions{
			priority: HintPriorityHigh, clearOnMessage: true, clearAfter: systemHintDuration,
		})
	}
	m.subagentDirectoryRetries++
	m.subagentDirectoryRetryScheduled = true
	delay := taskStreamRetryBackoff(m.subagentDirectoryRetries)
	return tea.Tick(delay, func(time.Time) tea.Msg {
		return subagentDirectoryRetryMsg{sessionID: msg.sessionID, generation: retryGeneration}
	})
}

func (m *Model) handleSubagentDirectoryRetry(msg subagentDirectoryRetryMsg) tea.Cmd {
	if m == nil || msg.sessionID != m.currentSessionID ||
		msg.generation != m.subagentDirectoryGeneration {
		return nil
	}
	m.subagentDirectoryRetryScheduled = false
	return m.ensureSubagentDirectoryWatch()
}

func (m *Model) resetSubagentDirectoryWatch() {
	if m == nil {
		return
	}
	m.subagentDirectoryGeneration++
	if m.subagentDirectoryCancel != nil {
		m.subagentDirectoryCancel()
		m.subagentDirectoryCancel = nil
	}
	if m.subagentDirectorySubscription != nil {
		_ = m.subagentDirectorySubscription.Close()
		m.subagentDirectorySubscription = nil
	}
	m.subagentDirectoryStarting = false
	m.subagentDirectoryRetryScheduled = false
	m.subagentDirectoryRetries = 0
	m.subagentDirectoryRevision = 0
	m.subagentRosterTasks = map[string]taskstream.TaskDescriptor{}
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
	if descriptorStatus == subagentOutputRunning {
		return descriptorStatus, descriptor.UpdatedAt, time.Time{}
	}
	return descriptorStatus, time.Time{}, descriptor.UpdatedAt
}

func subagentRosterDescriptorIsNewActivity(descriptor taskstream.TaskDescriptor, view *subagentOutputView) bool {
	activityID := subagentRosterDescriptorActivityID(descriptor)
	return view != nil && activityID != "" && view.directoryActivityID != "" &&
		activityID != view.directoryActivityID
}

func subagentRosterDescriptorActivityID(descriptor taskstream.TaskDescriptor) string {
	if activityID := taskStreamActivityKey(descriptor.ActivityID); activityID != "" {
		return activityID
	}
	// Older durable Tasks may predate ActivityID persistence. Keep their cache
	// scoped to the Control directory's current child Turn rather than falling
	// back to transcript content or local timestamps.
	if turnID := strings.TrimSpace(descriptor.CurrentTurnID); turnID != "" {
		return "turn:" + turnID
	}
	if taskID := strings.TrimSpace(descriptor.TaskID); taskID != "" {
		return "legacy-task:" + taskID
	}
	return ""
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
