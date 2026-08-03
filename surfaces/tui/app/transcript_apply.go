package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/caelis-labs/caelis/agent-sdk/display"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/surfaces/promptview"
	"github.com/caelis-labs/caelis/surfaces/transcript"
	"github.com/caelis-labs/caelis/surfaces/tui/tuikit"
)

func (m *Model) handleTranscriptEventsMsg(msg TranscriptEventsMsg) (tea.Model, tea.Cmd) {
	// Preserve projection order within replay batches: a Spawn owner must be
	// observed before a later SendMessage resolves that owner's public handle.
	// Decorating the whole batch first would permanently erase the structured
	// target before the earlier Spawn event had mounted its view.
	subagentOutputChanged := false
	for index := range msg.Events {
		one := msg.Events[index : index+1]
		subagentOutputChanged = m.observeSubagentOutputEvents(one) || subagentOutputChanged
		m.decorateAgentMessageDisplayTargets(one)
	}
	// Mount/update transcript owners before applying the correlated repairs;
	// exact owner resolution intentionally fails closed without a BlockID.
	model, transcriptCmd := m.applyTranscriptEvents(msg.Events)
	if next, ok := model.(*Model); ok {
		m = next
	}
	observedSpawnCmd := m.applyObservedSpawnResults(msg.OwnerRepairs.Spawns)
	m.applyObservedCommandResults(msg.OwnerRepairs.Commands)
	// Spawn views, including terminal owner repairs projected from Task
	// observations, drive child subscription lifetime. The Task invocation does
	// not own or redirect that stream.
	m.reconcileSubagentOutputTaskStreams()
	var subagentOutputCmd tea.Cmd
	if subagentOutputChanged {
		subagentOutputCmd = m.requestSubagentOutputRender()
	}
	return m, tea.Batch(transcriptCmd, observedSpawnCmd, subagentOutputCmd, m.resumeRunningAnimationIfNeeded())
}

func (m *Model) decorateAgentMessageDisplayTargets(events []TranscriptEvent) []TranscriptEvent {
	for index := range events {
		event := &events[index]
		if event.Kind != TranscriptEventTool ||
			names.CanonicalOrSelf(toolSemanticName(event.ToolName, event.ToolKind)) != names.SendMessage {
			continue
		}
		target := event.ToolMessageTarget
		label := m.agentMessageTargetDisplayLabel(target)
		if label == "" {
			continue
		}
		event.ToolArgs = replaceAgentMessageDisplayTarget(event.ToolArgs, target, label)
		event.ToolFullArgs = replaceAgentMessageDisplayTarget(event.ToolFullArgs, target, label)
		if m.subagentOutputCallIDForHandle(target) == "" {
			// Keep the semantic label, but do not manufacture a dead overlay
			// affordance for parent or an unresolved/failed destination.
			event.ToolMessageTarget = ""
		}
	}
	return events
}

func (m *Model) agentMessageTargetDisplayLabel(rawTarget string) string {
	target := normalizeTaskStreamHandle(rawTarget)
	if target == "" {
		return ""
	}
	for _, view := range m.subagentOutputViews {
		if view == nil || normalizeTaskStreamHandle(view.taskHandle) != target {
			continue
		}
		label := strings.TrimSpace(view.title)
		if before, _, ok := strings.Cut(label, ":"); ok {
			label = strings.TrimSpace(before)
		}
		if label != "" {
			return label
		}
	}
	return target
}

func replaceAgentMessageDisplayTarget(args, rawTarget, label string) string {
	args = strings.TrimSpace(args)
	label = strings.TrimSpace(label)
	if args == "" || label == "" {
		return args
	}
	target := display.AgentMessageTarget(rawTarget)
	if target == "" {
		target = "@" + normalizeTaskStreamHandle(rawTarget)
	}
	prefix := "to " + target
	if !strings.HasPrefix(args, prefix) {
		return args
	}
	return label + strings.TrimPrefix(args, prefix)
}

func (m *Model) applyTranscriptEvents(events []TranscriptEvent) (tea.Model, tea.Cmd) {
	if len(events) == 0 {
		return m, nil
	}
	m.observeRunningActivityTargets(events)
	var cmds []tea.Cmd
	for _, event := range events {
		if eventTargetsSubagentOutputView(event) {
			continue
		}
		model, cmd := m.applyTranscriptEvent(event)
		if next, ok := model.(*Model); ok {
			m = next
		}
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

func (m *Model) applyTranscriptEvent(event TranscriptEvent) (tea.Model, tea.Cmd) {
	switch event.Kind {
	case TranscriptEventNarrative:
		return m.applyTranscriptNarrative(event)
	case TranscriptEventNotice:
		return m.applyTranscriptNotice(event)
	case TranscriptEventPlan:
		return m.applyTranscriptPlan(event)
	case TranscriptEventTool:
		return m.applyTranscriptTool(event)
	case TranscriptEventApproval:
		return m.applyTranscriptApproval(event)
	case TranscriptEventParticipant:
		return m.applyTranscriptParticipant(event)
	case TranscriptEventLifecycle:
		return m.applyTranscriptLifecycle(event)
	case TranscriptEventUsage:
		return m.applyTranscriptUsage(event), nil
	case transcript.EventError:
		return m.applyTranscriptError(event)
	default:
		return m, nil
	}
}

func (m *Model) applyTranscriptError(event TranscriptEvent) (tea.Model, tea.Cmd) {
	if m == nil || event.Scope != ACPProjectionParticipant {
		return m, nil
	}
	event.Text = strings.TrimSpace(tuikit.SanitizeLogText(event.Text))
	if event.Text == "" {
		return m, nil
	}
	turnKey := transcriptParticipantTurnKey(event)
	block := m.findParticipantTurnBlock(turnKey)
	if block == nil {
		block = m.ensureParticipantTurnBlock(turnKey, participantTurnTranscriptActor(event))
	}
	if block == nil {
		return m, nil
	}
	m.activeParticipantTurnSessionID = strings.TrimSpace(block.SessionID)
	result := applyTranscriptEventToParticipantTurn(block, event, participantTurnTranscriptPolicy{})
	if !result.changed {
		return m, nil
	}
	m.markViewportBlockDirty(block.BlockID())
	m.hasCommittedLine = true
	return m, m.requestStreamViewportSync()
}

func (m *Model) applyTranscriptUsage(event TranscriptEvent) tea.Model {
	if m == nil || event.Scope != ACPProjectionMain || event.Usage == nil {
		return m
	}
	contextUsage := promptview.FormatContextUsage(event.Usage.TotalTokens, event.Usage.ContextWindowTokens)
	if strings.TrimSpace(contextUsage) != "" {
		m.statusContext = contextUsage
		m.statusView.Tokens = contextUsage
	}
	return m
}

func (m *Model) applyTranscriptNotice(event TranscriptEvent) (tea.Model, tea.Cmd) {
	text := formatTranscriptNoticeText(event.Text)
	if text == "" {
		return m, nil
	}
	if eventTargetsParentToolPanel(event) {
		return m, nil
	}
	if m.shouldAnchorMainNotice(event) {
		m.prepareForTranscriptScope(event.Scope)
		block := m.ensureMainTimelineBlock(event)
		if block != nil {
			block.AddNotice(text, event.OccurredAt, event.NoticeKind)
			m.markViewportBlockDirty(block.BlockID())
			return m, m.requestStreamViewportSync()
		}
	}
	return m.appendEventStreamTranscriptText(text)
}

func (m *Model) shouldAnchorMainNotice(event TranscriptEvent) bool {
	return m != nil && event.Scope == ACPProjectionMain
}

func formatTranscriptNoticeText(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	if text == transcript.CompactNoticeLabel {
		return "• " + transcript.CompactNoticeLabel
	}
	return text
}

func (m *Model) prepareForTranscriptScope(scope ACPProjectionScope) {
	switch scope {
	case ACPProjectionMain, ACPProjectionParticipant, ACPProjectionSubagent:
		m.finalizeAssistantBlock()
		m.finalizeReasoningBlock()
	}
}

func (m *Model) applyTranscriptNarrative(event TranscriptEvent) (tea.Model, tea.Cmd) {
	switch event.NarrativeKind {
	case TranscriptNarrativeUser:
		if event.Scope == ACPProjectionParticipant {
			return m.handleDirectedParticipantUserMessage(event), nil
		}
		return m.handleUserMessageMsg(UserMessageMsg{Text: event.Text}), nil
	case TranscriptNarrativeSystem, TranscriptNarrativeNotice:
		return m.appendEventStreamTranscriptText(event.Text)
	}

	m.prepareForTranscriptScope(event.Scope)
	switch event.Scope {
	case ACPProjectionParticipant:
		return m.applyTranscriptNarrativeToParticipantTurn(event)
	case ACPProjectionSubagent:
		return m.applyTranscriptSubagentNarrative(event)
	default:
		return m.applyTranscriptMainNarrative(event)
	}
}

func (m *Model) handleDirectedParticipantUserMessage(event TranscriptEvent) tea.Model {
	text := directedParticipantUserDisplay(event)
	if text == "" {
		return m
	}
	// Participant echoes are directed side-agent prompts; they must not close
	// or finalize any active main ACP turn.
	return m.applyGatewayUserEcho(gatewayUserEchoOptions{
		displayLine:        text,
		dequeueNeedles:     []string{directedParticipantUserDequeueText(event)},
		participantTurnKey: transcriptParticipantTurnKey(event),
	})
}

func (m *Model) applyTranscriptMainNarrative(event TranscriptEvent) (tea.Model, tea.Cmd) {
	block := m.ensureMainTimelineBlock(event)
	if block == nil {
		return m, nil
	}
	if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
		block.StartedAt = event.OccurredAt
	}
	text := tuikit.SanitizeLogText(transcriptNarrativeText(event))
	source := narrativeSourceIdentityFromTranscriptEvent(event)
	if event.NarrativeKind == TranscriptNarrativeReasoning {
		if event.Final {
			block.ReplaceFinalStreamEvent(SEReasoning, text, source, event.OccurredAt)
		} else if text != "" {
			block.AppendStreamEvent(SEReasoning, text, source, event.OccurredAt)
		}
	} else {
		if event.Final {
			closeLatestReasoningTiming(block.Events, event.OccurredAt)
		}
		if event.Final {
			block.ReplaceFinalStreamEvent(SEAssistant, text, source, event.OccurredAt)
			if !m.turnRunning() {
				m.closeMainTimelineTailWithState(block, event.OccurredAt, "completed")
			}
		} else if text != "" {
			block.AppendStreamEvent(SEAssistant, text, source, event.OccurredAt)
		}
	}
	m.markViewportBlockDirty(block.BlockID())
	return m, m.requestStreamViewportSync()
}

func (m *Model) applyTranscriptPlan(event TranscriptEvent) (tea.Model, tea.Cmd) {
	m.prepareForTranscriptScope(event.Scope)
	switch event.Scope {
	case ACPProjectionParticipant:
		return m.applyTranscriptPlanToParticipantTurn(event, false)
	case ACPProjectionSubagent:
		if eventTargetsParentToolPanel(event) {
			return m, nil
		}
		return m.applyTranscriptPlanToParticipantTurn(event, true)
	default:
		block := m.ensureMainTimelineBlock(event)
		if block == nil {
			return m, nil
		}
		if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
			block.StartedAt = event.OccurredAt
		}
		block.UpdatePlan(transcriptPlanEntries(event))
		m.markViewportBlockDirty(block.BlockID())
		return m, m.requestStreamViewportSync()
	}
}

func (m *Model) applyTranscriptTool(event TranscriptEvent) (tea.Model, tea.Cmd) {
	m.prepareForTranscriptScope(event.Scope)
	mutation := transcriptToolMutationFromEvent(event)
	switch event.Scope {
	case ACPProjectionParticipant:
		return m.applyTranscriptToolToParticipant(event, mutation)
	case ACPProjectionSubagent:
		return m.applyTranscriptToolToSubagent(event, mutation)
	default:
		return m.applyTranscriptToolToMain(event, mutation)
	}
}

func (m *Model) applyTranscriptApproval(event TranscriptEvent) (tea.Model, tea.Cmd) {
	m.prepareForTranscriptScope(event.Scope)
	if strings.TrimSpace(event.ApprovalText) != "" {
		return m.applyTranscriptApprovalReview(event)
	}
	switch event.Scope {
	case ACPProjectionParticipant:
		return m.applyTranscriptStatusToParticipantTurn(
			event,
			firstNonEmpty(strings.TrimSpace(event.State), "waiting_approval"),
			event.ApprovalTool,
			event.ApprovalCommand,
		)
	case ACPProjectionSubagent:
		if eventTargetsParentToolPanel(event) {
			return m, nil
		}
		return m.applyTranscriptStatusToParticipantTurn(event, firstNonEmpty(strings.TrimSpace(event.State), "waiting_approval"), event.ApprovalTool, event.ApprovalCommand)
	default:
		block := m.mainBlockForAnchor(event, mainApprovalAnchor(event.ToolCallID))
		if block == nil {
			return m, nil
		}
		block.SetStatus(firstNonEmpty(strings.TrimSpace(event.State), "waiting_approval"), event.ApprovalTool, event.ApprovalCommand, event.OccurredAt)
		m.markViewportBlockDirty(block.BlockID())
		return m, m.requestStreamViewportSync()
	}
}

func (m *Model) applyTranscriptApprovalReview(event TranscriptEvent) (tea.Model, tea.Cmd) {
	if strings.TrimSpace(event.AnchorToolCallID) != "" {
		if applied, cmd := m.applyAnchoredApprovalReviewToTool(event); applied {
			return m, cmd
		}
	}
	switch event.Scope {
	case ACPProjectionParticipant:
		return m.applyTranscriptApprovalReviewToParticipantTurn(event)
	case ACPProjectionSubagent:
		if eventTargetsParentToolPanel(event) {
			return m, nil
		}
		return m.applyTranscriptApprovalReviewToParticipantTurn(event)
	default:
		block := m.mainBlockForAnchor(event, mainApprovalAnchor(event.ToolCallID))
		if block == nil {
			return m, nil
		}
		if state := strings.ToLower(strings.TrimSpace(block.Status)); state == "waiting_approval" {
			block.Status = "running"
		}
		block.AddApprovalReviewEvent(event.ToolCallID, event.ApprovalTool, event.ApprovalCommand, event.ApprovalStatus, event.ApprovalRisk, event.ApprovalAuth, event.ApprovalText)
		m.markViewportBlockDirty(block.BlockID())
		return m, m.requestStreamViewportSync()
	}
}

func (m *Model) applyAnchoredApprovalReviewToTool(event TranscriptEvent) (bool, tea.Cmd) {
	if m == nil {
		return false, nil
	}
	callID := strings.TrimSpace(event.AnchorToolCallID)
	if callID == "" {
		return false, nil
	}
	output := approvalReviewTailOutput(event)
	if output == "" {
		return true, nil
	}
	toolName := strings.TrimSpace(event.AnchorToolName)
	for _, docBlock := range m.doc.Blocks() {
		block, ok := docBlock.(*MainACPTurnBlock)
		if !ok || !mainACPBlockHasToolCall(block, callID) {
			continue
		}
		block.UpdateToolWithMeta(callID, toolName, "", output, false, false, ToolUpdateMeta{ToolKind: "execute"})
		m.markViewportBlockDirty(block.BlockID())
		return true, m.requestStreamViewportSync()
	}
	return false, nil
}

func mainACPBlockHasToolCall(block *MainACPTurnBlock, callID string) bool {
	if block == nil {
		return false
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return false
	}
	for _, ev := range block.Events {
		if ev.Kind == SEToolCall && strings.TrimSpace(ev.CallID) == callID {
			return true
		}
	}
	return false
}

func approvalReviewTailOutput(event TranscriptEvent) string {
	return transcript.ApprovalReviewTailOutput(transcript.ApprovalReviewFields{
		Tool:          event.ApprovalTool,
		Command:       event.ApprovalCommand,
		Status:        event.ApprovalStatus,
		Risk:          event.ApprovalRisk,
		Authorization: event.ApprovalAuth,
		Text:          event.ApprovalText,
	})
}

func (m *Model) applyTranscriptParticipant(event TranscriptEvent) (tea.Model, tea.Cmd) {
	m.prepareForTranscriptScope(event.Scope)
	switch event.Scope {
	case ACPProjectionSubagent:
		if eventTargetsParentToolPanel(event) {
			return m, nil
		}
		return m.applyTranscriptStatusToParticipantTurn(event, event.State, "", "")
	default:
		return m.applyTranscriptStatusToParticipantTurn(event, event.State, "", "")
	}
}

func (m *Model) applyTranscriptLifecycle(event TranscriptEvent) (tea.Model, tea.Cmd) {
	m.prepareForTranscriptScope(event.Scope)
	switch event.Scope {
	case ACPProjectionParticipant:
		return m.applyTranscriptStatusToParticipantTurn(event, event.State, "", "")
	case ACPProjectionSubagent:
		if eventTargetsParentToolPanel(event) {
			return m, nil
		}
		return m.applyTranscriptStatusToParticipantTurn(event, event.State, "", "")
	default:
		terminal := eventstream.IsTerminalLifecycleState(event.State)
		// A terminal-only main lifecycle can close a participant-owned Side ACP
		// turn without carrying any main transcript content. Do not manufacture
		// an invisible MainACPTurnBlock after the participant footer: it would
		// hide that footer from finishLiveTurn and produce a second duration
		// divider for the same user submission.
		if terminal && strings.TrimSpace(m.mainTimelineTailID) == "" {
			return m, nil
		}
		block := m.ensureMainTimelineBlock(event)
		if block == nil {
			return m, nil
		}
		if event.State == "attempt_reset" {
			block.ClearActiveBuffers()
		} else {
			if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
				block.StartedAt = event.OccurredAt
			}
			if !m.turnRunning() && terminal {
				m.closeMainTimelineTailWithState(block, event.OccurredAt, event.State)
			} else {
				block.SetStatus(event.State, "", "", event.OccurredAt)
			}
			if strings.EqualFold(strings.TrimSpace(event.State), "completed") {
				m.captureLiveTurnDuration(event.OccurredAt)
				m.captureLiveTurnDurationFromMainBlock(block)
			}
		}
		m.markViewportBlockDirty(block.BlockID())
		return m, m.requestStreamViewportSync()
	}
}

func (m *Model) applyTranscriptSubagentNarrative(event TranscriptEvent) (tea.Model, tea.Cmd) {
	if !eventTargetsParentToolPanel(event) {
		return m.applyTranscriptNarrativeToParticipantTurn(event)
	}
	if event.NarrativeKind != TranscriptNarrativeAssistant {
		return m, nil
	}
	return m.applyAnchoredSubagentNarrativeToTool(event)
}

func narrativeSourceIdentityFromTranscriptEvent(event TranscriptEvent) narrativeSourceIdentity {
	return newNarrativeSourceIdentity(event.MessageID, event.SourceEventID, event.SourceProjectionID)
}

func eventTargetsParentToolPanel(event TranscriptEvent) bool {
	return event.Scope == ACPProjectionSubagent &&
		strings.TrimSpace(event.ScopeID) != "" &&
		strings.TrimSpace(event.AnchorToolCallID) != ""
}

func eventTargetsSubagentOutputView(event TranscriptEvent) bool {
	return eventTargetsParentToolPanel(event) &&
		names.CanonicalOrSelf(toolSemanticName(event.AnchorToolName, "")) == names.Spawn
}

func (m *Model) applyAnchoredSubagentNarrativeToTool(event TranscriptEvent) (tea.Model, tea.Cmd) {
	if m == nil {
		return m, nil
	}
	callID := strings.TrimSpace(event.AnchorToolCallID)
	if callID == "" {
		return m, nil
	}
	text := tuikit.SanitizeLogText(transcriptNarrativeText(event))
	if strings.TrimSpace(text) == "" {
		return m, nil
	}
	toolName := strings.TrimSpace(event.AnchorToolName)
	meta := ToolUpdateMeta{
		ToolKind:        "execute",
		TaskHandle:      strings.TrimSpace(event.ScopeID),
		MessageID:       strings.TrimSpace(event.MessageID),
		OutputNarrative: true,
	}
	// A later Turn may observe the same physical child through TASK write. Keep
	// that continuation in the current panel instead of reopening the original
	// completed Spawn panel solely because ParentTool still names its owner.
	if block := m.activeMainTaskWriteBlock(meta.TaskHandle); block != nil {
		block.UpdateToolWithMeta(callID, toolName, "", text, false, false, meta)
		m.markViewportBlockDirty(block.BlockID())
		return m, m.requestStreamViewportSync()
	}
	for _, docBlock := range m.doc.Blocks() {
		block, ok := docBlock.(*MainACPTurnBlock)
		if !ok || !mainACPBlockHasToolCall(block, callID) {
			continue
		}
		// A child narrative finalizes only that ACP message segment. The parent
		// Spawn lifecycle remains open until its own tool status/result arrives.
		block.UpdateToolWithMeta(callID, toolName, "", text, false, false, meta)
		m.markViewportBlockDirty(block.BlockID())
		return m, m.requestStreamViewportSync()
	}
	return m, nil
}

func (m *Model) activeMainTaskWriteBlock(taskID string) *MainACPTurnBlock {
	if m == nil || m.doc == nil {
		return nil
	}
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil
	}
	blockID := strings.TrimSpace(m.mainTimelineTailID)
	if blockID == "" {
		return nil
	}
	block, _ := m.doc.Find(blockID).(*MainACPTurnBlock)
	if block == nil || !block.EndedAt.IsZero() {
		return nil
	}
	for i := len(block.Events) - 1; i >= 0; i-- {
		ev := block.Events[i]
		if ev.Kind != SEToolCall || strings.TrimSpace(ev.TaskHandle) != taskID {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(ev.Name), "TASK") && taskEventAction(ev) == "write" {
			return block
		}
	}
	return nil
}

func (m *Model) applyTranscriptPlanToParticipantTurn(event TranscriptEvent, reopenPlan bool) (tea.Model, tea.Cmd) {
	block := m.ensureParticipantTurnBlock(transcriptParticipantTurnKey(event), participantTurnTranscriptActor(event))
	if block == nil {
		return m, nil
	}
	m.activeParticipantTurnSessionID = strings.TrimSpace(block.SessionID)
	result := applyTranscriptEventToParticipantTurn(block, event, participantTurnTranscriptPolicy{
		actor:      participantTurnTranscriptActor(event),
		reopenPlan: reopenPlan,
	})
	if !result.changed {
		return m, nil
	}
	m.markViewportBlockDirty(block.BlockID())
	return m, m.requestStreamViewportSync()
}

func (m *Model) applyTranscriptStatusToParticipantTurn(event TranscriptEvent, stateName, approvalTool, approvalCommand string) (tea.Model, tea.Cmd) {
	block := m.ensureParticipantTurnBlock(transcriptParticipantTurnKey(event), participantTurnTranscriptActor(event))
	if block == nil {
		return m, nil
	}
	statusEvent := event
	statusEvent.State = stateName
	if strings.TrimSpace(approvalTool) != "" || strings.TrimSpace(approvalCommand) != "" {
		statusEvent.Kind = TranscriptEventApproval
		statusEvent.ApprovalTool = approvalTool
		statusEvent.ApprovalCommand = approvalCommand
	} else {
		statusEvent.Kind = TranscriptEventLifecycle
	}
	result := applyTranscriptEventToParticipantTurn(block, statusEvent, participantTurnTranscriptPolicy{
		actor: participantTurnTranscriptActor(event),
	})
	if !result.changed {
		return m, nil
	}
	m.markViewportBlockDirty(block.BlockID())
	return m, m.requestStreamViewportSync()
}

func (m *Model) applyTranscriptApprovalReviewToParticipantTurn(event TranscriptEvent) (tea.Model, tea.Cmd) {
	block := m.ensureParticipantTurnBlock(transcriptParticipantTurnKey(event), participantTurnTranscriptActor(event))
	if block == nil {
		return m, nil
	}
	result := applyTranscriptEventToParticipantTurn(block, event, participantTurnTranscriptPolicy{
		actor: participantTurnTranscriptActor(event),
	})
	if !result.changed {
		return m, nil
	}
	m.markViewportBlockDirty(block.BlockID())
	return m, m.requestStreamViewportSync()
}

func participantTurnTranscriptActor(event TranscriptEvent) string {
	if event.Scope == ACPProjectionSubagent {
		return subagentTranscriptActor(event)
	}
	return participantTranscriptActor(event)
}

func subagentTranscriptActor(event TranscriptEvent) string {
	return firstNonEmpty(strings.TrimSpace(event.Actor), strings.TrimSpace(event.ScopeID), "subagent")
}

func transcriptParticipantTurnKey(event TranscriptEvent) string {
	if event.Scope == ACPProjectionParticipant {
		return firstNonEmpty(strings.TrimSpace(event.TurnID), strings.TrimSpace(event.ScopeID))
	}
	return strings.TrimSpace(event.ScopeID)
}

func transcriptToolUpdateMeta(event TranscriptEvent) ToolUpdateMeta {
	return ToolUpdateMeta{
		TaskHandle:             event.ToolTaskHandle,
		TaskAction:             event.ToolTaskAction,
		TaskInput:              event.ToolTaskInput,
		TaskTargetKind:         event.ToolTaskTargetKind,
		MessageTarget:          event.ToolMessageTarget,
		ToolKind:               event.ToolKind,
		FullArgs:               event.ToolFullArgs,
		ToolStatus:             event.ToolStatus,
		Terminal:               event.ToolTerminal,
		OutputSynthetic:        event.ToolOutputSynthetic,
		OutputTerminal:         event.ToolOutputTerminal,
		OutputGapBefore:        event.ToolOutputGapBefore,
		OutputCursor:           event.ToolOutputCursor,
		OutputCursorKnown:      event.ToolOutputCursorKnown,
		OutputStartCursor:      event.ToolOutputStartCursor,
		OutputStartCursorKnown: event.ToolOutputStartCursorKnown,
	}
}
