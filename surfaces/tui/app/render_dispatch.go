package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type renderEventLane string

const (
	renderLaneLog         renderEventLane = "log"
	renderLaneMainStream  renderEventLane = "main_stream"
	renderLaneToolStream  renderEventLane = "tool_stream"
	renderLaneParticipant renderEventLane = "participant"
	renderLaneSubagent    renderEventLane = "subagent"
	renderLaneUIState     renderEventLane = "ui_state"
	renderLaneLifecycle   renderEventLane = "lifecycle"
	renderLaneOverlay     renderEventLane = "overlay"
	renderLanePrompt      renderEventLane = "prompt"
	renderLaneTick        renderEventLane = "tick"
)

type renderEventPolicy struct {
	lane              renderEventLane
	flushSmoothing    bool
	flushLogChunks    bool
	dismissHints      bool
	flushDeferredOnly bool
}

func renderEventPolicyFor(msg tea.Msg) (renderEventPolicy, bool) {
	switch typed := msg.(type) {
	case TranscriptEventsMsg:
		return renderEventPolicyForTranscriptEvents(typed), true
	case LogChunkMsg:
		return renderEventPolicy{lane: renderLaneLog, flushSmoothing: true, dismissHints: true}, true
	case CommandPanelMsg:
		return renderEventPolicy{lane: renderLaneLog, flushSmoothing: true, flushLogChunks: true, dismissHints: true}, true
	case ParticipantStatusMsg:
		return renderEventPolicy{lane: renderLaneParticipant, flushSmoothing: true, flushLogChunks: true}, true
	case SubagentStartMsg:
		return renderEventPolicy{lane: renderLaneSubagent, flushSmoothing: true, flushLogChunks: true, dismissHints: true}, true
	case SubagentStatusMsg, SubagentDoneMsg:
		return renderEventPolicy{lane: renderLaneSubagent, flushSmoothing: true, flushLogChunks: true}, true
	case PlanUpdateMsg, SetHintMsg, ApprovalReviewHintMsg, SetRunningMsg,
		SetStatusMsg, StatusRefreshResultMsg, SetCommandsMsg, AttachmentCountMsg,
		RunningInterruptResultMsg, SandboxProgressMsg:
		return renderEventPolicy{lane: renderLaneUIState}, true
	case ClearHistoryMsg, UserMessageMsg, TaskResultMsg:
		return renderEventPolicy{lane: renderLaneLifecycle, flushSmoothing: true, flushLogChunks: true, dismissHints: true}, true
	case BTWOverlayMsg:
		return renderEventPolicy{lane: renderLaneOverlay}, true
	case BTWErrorMsg:
		return renderEventPolicy{lane: renderLaneOverlay}, true
	case PromptRequestMsg:
		return renderEventPolicy{lane: renderLanePrompt, flushSmoothing: true, flushLogChunks: true}, true
	case frameTickMsg:
		return renderEventPolicyForFrameTick(typed), true
	case TickStatusMsg:
		return renderEventPolicy{lane: renderLaneTick, flushDeferredOnly: true}, true
	default:
		return renderEventPolicy{}, false
	}
}

func renderEventPolicyForTranscriptEvents(msg TranscriptEventsMsg) renderEventPolicy {
	if len(msg.Events) == 0 {
		return renderEventPolicy{lane: renderLaneLifecycle}
	}
	hasParticipant := false
	hasSubagent := false
	hasTool := false
	for _, event := range msg.Events {
		switch event.Scope {
		case ACPProjectionSubagent:
			hasSubagent = true
		case ACPProjectionParticipant:
			hasParticipant = true
		}
		if event.Kind == TranscriptEventTool || event.Kind == TranscriptEventApproval {
			hasTool = true
		}
	}
	flushSmoothing := transcriptEventsNeedSmoothingFlush(msg.Events)
	switch {
	case hasSubagent:
		return renderEventPolicy{lane: renderLaneSubagent, flushSmoothing: flushSmoothing, flushLogChunks: true, dismissHints: true}
	case hasParticipant:
		return renderEventPolicy{lane: renderLaneParticipant, flushSmoothing: flushSmoothing, flushLogChunks: true, dismissHints: true}
	case hasTool:
		return renderEventPolicy{lane: renderLaneToolStream, flushSmoothing: true, flushLogChunks: true, dismissHints: true}
	default:
		return renderEventPolicy{lane: renderLaneMainStream, flushSmoothing: flushSmoothing, flushLogChunks: true, dismissHints: true}
	}
}

func transcriptEventsNeedSmoothingFlush(events []TranscriptEvent) bool {
	if len(events) == 0 {
		return false
	}
	for _, event := range events {
		switch event.Kind {
		case TranscriptEventNarrative:
			if event.NarrativeKind == TranscriptNarrativeAssistant || event.NarrativeKind == TranscriptNarrativeReasoning {
				if event.Final {
					return true
				}
				continue
			}
			return true
		case TranscriptEventUsage:
			continue
		default:
			return true
		}
	}
	return false
}

func renderEventPolicyForFrameTick(msg frameTickMsg) renderEventPolicy {
	if msg.kind == frameTickDeferredBatch {
		return renderEventPolicy{lane: renderLaneTick, flushDeferredOnly: true}
	}
	return renderEventPolicy{lane: renderLaneTick}
}

func (m *Model) applyRenderEventPolicy(policy renderEventPolicy) tea.Cmd {
	if m == nil {
		return nil
	}
	var cmds []tea.Cmd
	if policy.flushDeferredOnly {
		return m.flushPendingDeferredBatches()
	}
	if policy.flushSmoothing {
		m.flushAllPendingStreamSmoothingWithReason("policy_" + string(policy.lane))
	}
	if policy.dismissHints {
		m.dismissMessageHints()
	}
	if policy.flushLogChunks {
		cmds = append(cmds, m.flushPendingLogChunks())
	}
	return tea.Batch(cmds...)
}

func (m *Model) deferredBatchingEnabled() bool {
	return m != nil && m.cfg.StreamTickInterval > 0
}

func (m *Model) dispatchRenderEvent(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	policy, ok := renderEventPolicyFor(msg)
	if !ok {
		return m, nil, false
	}
	m.observeRenderMessage(msg, policy)
	if shouldInvalidateUserDisplayDedup(msg) {
		m.invalidateUserDisplayDedup()
	}
	if m.shouldEnqueueRenderEvent(msg, policy) {
		if policy.dismissHints {
			m.dismissMessageHints()
		}
		return m, m.enqueueRenderEvent(msg, policy.lane), true
	}
	preCmd := tea.Cmd(nil)
	if m.shouldFlushPendingRenderEventsBefore(msg, policy) {
		preCmd = m.drainPendingRenderEvents(time.Now())
	}
	policyCmd := tea.Batch(preCmd, m.applyRenderEventPolicy(policy))

	switch typed := msg.(type) {
	case TranscriptEventsMsg:
		model, cmd := m.handleTranscriptEventsMsg(typed)
		return model, tea.Batch(policyCmd, cmd, m.flushImmediateViewportSyncForTranscriptEvents(typed)), true
	case LogChunkMsg:
		if !m.deferredBatchingEnabled() {
			model, cmd := m.handleLogChunk(typed.Chunk)
			return model, tea.Batch(policyCmd, cmd), true
		}
		if !m.queueLogChunk(typed.Chunk) {
			return m, policyCmd, true
		}
		return m, tea.Batch(policyCmd, m.ensureDeferredBatchTick()), true
	case CommandPanelMsg:
		model, cmd := m.handleCommandPanelMsg(typed)
		return model, tea.Batch(policyCmd, cmd), true

	case ParticipantStatusMsg:
		model, cmd := m.handleParticipantStatusMsg(typed)
		return model, tea.Batch(policyCmd, cmd), true

	case SubagentStartMsg:
		model, cmd := m.handleSubagentStart(typed)
		return model, tea.Batch(policyCmd, cmd), true
	case SubagentStatusMsg:
		model, cmd := m.handleSubagentStatus(typed)
		return model, tea.Batch(policyCmd, cmd), true
	case SubagentDoneMsg:
		model, cmd := m.handleSubagentDone(typed)
		return model, tea.Batch(policyCmd, cmd), true

	case PlanUpdateMsg:
		return m.handlePlanUpdateMsg(typed), policyCmd, true
	case SetHintMsg:
		model, cmd := m.handleSetHintMsg(typed)
		return model, tea.Batch(policyCmd, cmd), true
	case ApprovalReviewHintMsg:
		model, cmd := m.handleApprovalReviewHintMsg(typed)
		return model, tea.Batch(policyCmd, cmd), true
	case SetRunningMsg:
		return m.handleSetRunningMsg(typed), policyCmd, true
	case SetStatusMsg:
		return m.handleSetStatusMsg(typed), policyCmd, true
	case StatusRefreshResultMsg:
		return m.handleStatusRefreshResultMsg(typed), policyCmd, true
	case SetCommandsMsg:
		return m.handleSetCommandsMsg(typed), policyCmd, true
	case AttachmentCountMsg:
		return m.handleAttachmentCountMsg(typed), policyCmd, true
	case RunningInterruptResultMsg:
		model, cmd := m.handleRunningInterruptResultMsg(typed)
		return model, tea.Batch(policyCmd, cmd), true
	case SandboxProgressMsg:
		model, cmd := m.handleSandboxProgressMsg(typed)
		return model, tea.Batch(policyCmd, cmd), true

	case ClearHistoryMsg:
		m.resetConversationView()
		return m, policyCmd, true
	case UserMessageMsg:
		return m.handleUserMessageMsg(typed), policyCmd, true
	case TaskResultMsg:
		model, cmd := m.handleTaskResultMsg(typed)
		return model, tea.Batch(policyCmd, cmd), true

	case BTWOverlayMsg:
		model, cmd := m.handleBTWDelta(typed.Text, typed.Final)
		return model, tea.Batch(policyCmd, cmd), true
	case BTWErrorMsg:
		return m.handleBTWErrorMsg(typed), policyCmd, true

	case PromptRequestMsg:
		m.enqueuePrompt(typed)
		m.ensureViewportLayout()
		return m, policyCmd, true

	case frameTickMsg:
		legacyBroadcast := typed.kind == ""
		var cmds []tea.Cmd
		if legacyBroadcast || typed.kind == frameTickDeferredBatch {
			m.deferredBatchTickScheduled = false
		}
		if legacyBroadcast || typed.kind == frameTickOffscreen {
			hadOffscreenTick := m.offscreenViewportTickScheduled
			m.offscreenViewportTickScheduled = false
			if hadOffscreenTick {
				cmds = append(cmds, m.flushPendingOffscreenViewportSync(typed.at))
			}
		}
		if legacyBroadcast || typed.kind == frameTickViewportSync {
			hadViewportSyncTick := m.viewportSyncTickScheduled
			m.viewportSyncTickScheduled = false
			if hadViewportSyncTick {
				cmds = append(cmds, m.flushPendingViewportSync())
			}
		}
		if legacyBroadcast || typed.kind == frameTickStreamSmoothing {
			cmds = append(cmds, m.drainPendingStreamSmoothing(typed.at))
		}
		if legacyBroadcast || typed.kind == frameTickRenderDrain {
			cmds = append(cmds, m.drainPendingRenderEvents(typed.at))
		}
		if legacyBroadcast || typed.kind == frameTickPanelAnimation {
			cmds = append(cmds, m.advancePanelAnimations(typed.at))
		}
		if legacyBroadcast || typed.kind == frameTickScrollbarVisible {
			cmds = append(cmds, m.advanceScrollbarVisibility(typed.at))
		}
		return m, tea.Batch(append(cmds, policyCmd)...), true
	case TickStatusMsg:
		model, cmd := m.handleStatusTickMsg()
		return model, tea.Batch(policyCmd, cmd), true
	default:
		return m, nil, false
	}
}

func (m *Model) flushImmediateViewportSyncForTranscriptEvents(msg TranscriptEventsMsg) tea.Cmd {
	if m == nil || !m.viewportSyncPending || m.shouldDeferStreamViewportSync() {
		return nil
	}
	for _, event := range msg.Events {
		switch event.Kind {
		case TranscriptEventTool, TranscriptEventApproval, TranscriptEventPlan:
			return m.flushPendingViewportSync()
		}
	}
	return nil
}

func (m *Model) invalidateUserDisplayDedup() {
	if m == nil {
		return
	}
	m.userDisplayDedupOK = false
}

func shouldInvalidateUserDisplayDedup(msg tea.Msg) bool {
	switch msg.(type) {
	case TranscriptEventsMsg,
		ParticipantStatusMsg,
		SubagentStatusMsg,
		SubagentDoneMsg:
		return true
	default:
		return false
	}
}

func (m *Model) handlePlanUpdateMsg(msg PlanUpdateMsg) tea.Model {
	m.planEntries = m.planEntries[:0]
	hasIncomplete := false
	for _, item := range msg.Entries {
		content := strings.TrimSpace(item.Content)
		status := strings.TrimSpace(item.Status)
		if content == "" || status == "" {
			continue
		}
		if status != "completed" {
			hasIncomplete = true
		}
		m.planEntries = append(m.planEntries, planEntryState{Content: content, Status: status})
	}
	if !hasIncomplete {
		m.planEntries = m.planEntries[:0]
	}
	m.ensureViewportLayout()
	return m
}

func (m *Model) handleSetHintMsg(msg SetHintMsg) (tea.Model, tea.Cmd) {
	after := msg.ClearAfter
	if after <= 0 {
		after = systemHintDuration
	}
	return m, m.showHint(msg.Hint, hintOptions{
		priority:       msg.Priority,
		clearOnMessage: msg.ClearOnMessage,
		clearAfter:     after,
	})
}

func (m *Model) handleApprovalReviewHintMsg(msg ApprovalReviewHintMsg) (tea.Model, tea.Cmd) {
	if msg.Pending {
		m.approvalReviewHint = strings.TrimSpace(msg.Text)
	} else {
		m.approvalReviewHint = ""
	}
	m.ensureViewportLayout()
	return m, m.resumeRunningAnimationIfNeeded()
}

func (m *Model) handleSetRunningMsg(msg SetRunningMsg) tea.Model {
	wasRunning := m.running
	m.running = msg.Running
	if msg.Running && !wasRunning {
		m.startRunningAnimation()
	}
	if !msg.Running {
		m.stopRunningAnimation()
		m.runStartedAt = time.Time{}
	}
	return m
}

func (m *Model) handleSetStatusMsg(msg SetStatusMsg) tea.Model {
	welcomeMayChange := false
	if workspace := strings.TrimSpace(msg.Workspace); workspace != "" {
		if workspace != m.cfg.Workspace {
			m.cfg.Workspace = workspace
			welcomeMayChange = true
		}
	}
	nextModel := normalizeStatusModel(msg.Model)
	if nextModel != m.statusModel {
		m.statusModel = nextModel
		welcomeMayChange = true
	}
	m.statusContext = strings.TrimSpace(msg.Context)
	m.statusModeLabel = strings.TrimSpace(msg.ModeLabel)
	m.statusView = msg.Status
	if welcomeMayChange && m.syncWelcomeCardBlock() {
		m.syncViewportContent()
	}
	return m
}

func (m *Model) handleStatusRefreshResultMsg(msg StatusRefreshResultMsg) tea.Model {
	m.statusRefreshInFlight = false
	welcomeMayChange := false
	if msg.HasWorkspace {
		if workspace := strings.TrimSpace(msg.Workspace); workspace != "" {
			if workspace != m.cfg.Workspace {
				m.cfg.Workspace = workspace
				welcomeMayChange = true
			}
		}
	}
	if msg.HasStatus {
		nextModel := normalizeStatusModel(msg.Model)
		if nextModel != m.statusModel {
			m.statusModel = nextModel
			welcomeMayChange = true
		}
		m.statusContext = strings.TrimSpace(msg.Context)
	}
	if msg.HasView {
		m.statusView = msg.Status
	}
	if msg.HasModeLabel {
		m.statusModeLabel = strings.TrimSpace(msg.ModeLabel)
	}
	if welcomeMayChange && m.syncWelcomeCardBlock() {
		m.syncViewportContent()
	}
	return m
}

func (m *Model) handleSetCommandsMsg(msg SetCommandsMsg) tea.Model {
	m.setCommands(msg.Commands)
	return m
}

func (m *Model) handleAttachmentCountMsg(msg AttachmentCountMsg) tea.Model {
	if msg.Count <= 0 {
		m.clearInputAttachments()
		m.dismissVisibleHint()
	} else {
		m.syncAttachmentSummary()
	}
	m.syncTextareaChrome()
	m.ensureViewportLayout()
	return m
}

func (m *Model) handleUserMessageMsg(msg UserMessageMsg) tea.Model {
	m.dequeuePendingUserMessage(msg.Text)
	m.finalizeActiveMainACPTurn(false, nil)
	m.commitUserDisplayLine(msg.Text)
	m.ensureViewportLayout()
	m.syncViewportContent()
	return m
}

func (m *Model) handleBTWErrorMsg(msg BTWErrorMsg) tea.Model {
	if m.btwOverlay == nil && m.btwDismissed {
		return m
	}
	m.dropPendingStreamSmoothing(streamSmoothingKey("btw", "", "answer", ""))
	m.applyBTWOverlayImmediate(msg.Text, true)
	return m
}

func (m *Model) handleStatusTickMsg() (tea.Model, tea.Cmd) {
	cmds := []tea.Cmd{tickStatusCmd()}
	if !m.statusRefreshInFlight && m.hasStatusRefreshCallbacks() {
		m.statusRefreshInFlight = true
		m.observeDriverStatusCall()
		cmds = append(cmds, m.statusRefreshCmd())
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) hasStatusRefreshCallbacks() bool {
	return m != nil &&
		(m.cfg.RefreshWorkspace != nil ||
			m.cfg.RefreshStatus != nil ||
			m.cfg.RefreshStatusView != nil ||
			m.cfg.ModeLabel != nil)
}

func (m *Model) statusRefreshCmd() tea.Cmd {
	if m == nil {
		return nil
	}
	cfg := m.cfg
	return func() tea.Msg {
		msg := StatusRefreshResultMsg{}
		if cfg.RefreshWorkspace != nil {
			msg.Workspace = strings.TrimSpace(cfg.RefreshWorkspace())
			msg.HasWorkspace = true
		}
		if cfg.RefreshStatus != nil {
			msg.Model, msg.Context = cfg.RefreshStatus()
			msg.HasStatus = true
		}
		if cfg.RefreshStatusView != nil {
			msg.Status = cfg.RefreshStatusView()
			msg.HasView = true
		}
		if cfg.ModeLabel != nil {
			msg.ModeLabel = strings.TrimSpace(cfg.ModeLabel())
			msg.HasModeLabel = true
		}
		return msg
	}
}

func (m *Model) handleTaskResultMsg(msg TaskResultMsg) (tea.Model, tea.Cmd) {
	if msg.ContinueRunning {
		if msg.Err != nil {
			m.pendingQueue = nil
			errLine := "error: " + msg.Err.Error()
			m.commitLine(errLine)
			m.ensureViewportLayout()
			m.syncViewportContent()
		}
		return m, nil
	}
	if msg.Interrupted {
		m.discardActiveAssistantStream()
	} else {
		m.flushStream()
		m.finalizeAssistantBlock()
		m.finalizeReasoningBlock()
	}
	m.finalizeActiveMainACPTurn(msg.Interrupted, msg.Err)
	m.finalizeActiveParticipantTurn(msg.Interrupted, msg.Err)
	if !m.runStartedAt.IsZero() {
		m.lastRunDuration = time.Since(m.runStartedAt)
		m.hasLastRunDuration = true
		m.runStartedAt = time.Time{}
	}
	var nextPending pendingPrompt
	hasNextPending := false
	if msg.Err == nil && !msg.Interrupted && !msg.ExitNow {
		nextPending, hasNextPending = m.takeNextDeferredPendingPrompt()
	}
	m.discardDispatchedPendingPrompts()
	if !hasNextPending && (msg.Err != nil || msg.Interrupted || msg.ExitNow) {
		m.pendingQueue = nil
	}
	m.running = false
	m.runningInterruptRequested = false
	m.sandboxProgress = nil
	m.stopRunningAnimation()
	m.planEntries = m.planEntries[:0]
	m.clearInputAttachments()
	m.syncTextareaChrome()
	m.clearInputOverlays()
	if msg.Err != nil && !msg.Interrupted {
		errText := strings.TrimSpace(msg.Err.Error())
		isPromptCancel := errText == "cli: input interrupted" ||
			errText == "cli: input eof" ||
			errText == PromptErrInterrupt ||
			errText == PromptErrEOF
		if !isPromptCancel {
			errLine := "error: " + msg.Err.Error()
			m.commitLine(errLine)
		}
	}
	if m.showTurnDivider && !msg.SuppressTurnDivider && m.doc.Len() > 0 && !m.lastBlockHasParticipantTurnFooter() {
		last := m.doc.Last()
		hasContent := false
		if last != nil {
			if tb, ok := last.(*TranscriptBlock); ok {
				hasContent = strings.TrimSpace(tb.Raw) != ""
			} else {
				hasContent = true
			}
		}
		if hasContent {
			m.doc.Append(NewDividerBlock(m.userTurnDividerLabel()))
			m.markViewportStructureDirty()
		}
	}
	m.showTurnDivider = false
	m.ensureViewportLayout()
	m.syncViewportContent()
	if msg.ExitNow {
		m.quit = true
		return m, tea.Quit
	}
	if hasNextPending {
		return m.submitPendingPrompt(nextPending)
	}
	return m, nil
}

func (m *Model) handleRunningInterruptResultMsg(msg RunningInterruptResultMsg) (tea.Model, tea.Cmd) {
	if msg.Accepted {
		return m, nil
	}
	m.runningInterruptRequested = false
	if !m.running {
		return m, nil
	}
	return m, m.showHint("interrupt request did not reach the running task", hintOptions{
		priority:       HintPriorityHigh,
		clearOnMessage: true,
		clearAfter:     systemHintDuration,
	})
}

func (m *Model) handleSandboxProgressMsg(msg SandboxProgressMsg) (tea.Model, tea.Cmd) {
	if msg.Clear {
		source := strings.TrimSpace(msg.Source)
		if source != "" && (m.sandboxProgress == nil || m.sandboxProgress.Source != source) {
			return m, nil
		}
		m.sandboxProgress = nil
		m.ensureViewportLayout()
		return m, nil
	}
	title := strings.TrimSpace(msg.Title)
	if title == "" {
		title = "Windows sandbox"
	}
	message := strings.TrimSpace(msg.Message)
	if message == "" {
		message = strings.TrimSpace(msg.Phase)
	}
	m.sandboxProgress = &sandboxProgressState{
		Title:     title,
		Source:    strings.TrimSpace(msg.Source),
		Phase:     strings.TrimSpace(msg.Phase),
		Message:   message,
		Step:      msg.Step,
		Total:     msg.Total,
		Done:      msg.Done,
		UpdatedAt: time.Now(),
	}
	m.ensureViewportLayout()
	return m, nil
}

func (m *Model) lastBlockHasParticipantTurnFooter() bool {
	if m == nil || m.doc == nil {
		return false
	}
	block, _ := m.doc.Last().(*ParticipantTurnBlock)
	if block == nil || !block.Expanded {
		return false
	}
	return participantTurnIsTerminal(block.Status)
}
