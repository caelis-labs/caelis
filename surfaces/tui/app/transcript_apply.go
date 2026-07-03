package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/OnslaughtSnail/caelis/protocol/acp/eventstream"
	"github.com/OnslaughtSnail/caelis/surfaces/transcript"
	"github.com/OnslaughtSnail/caelis/surfaces/tui/tuikit"
)

func (m *Model) handleTranscriptEventsMsg(msg TranscriptEventsMsg) (tea.Model, tea.Cmd) {
	return m.applyTranscriptEvents(msg.Events)
}

func (m *Model) applyTranscriptEvents(events []TranscriptEvent) (tea.Model, tea.Cmd) {
	if len(events) == 0 {
		return m, nil
	}
	var cmds []tea.Cmd
	for _, event := range events {
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
		return m, nil
	default:
		return m, nil
	}
}

func (m *Model) applyTranscriptNotice(event TranscriptEvent) (tea.Model, tea.Cmd) {
	text := formatTranscriptNoticeText(event.Text)
	if text == "" {
		return m, nil
	}
	if shouldAnchorMainNotice(event) {
		m.prepareForTranscriptScope(event.Scope)
		block := m.ensureMainACPTurnBlock(transcriptMainTurnKey(event))
		if block != nil {
			block.AddNotice(text, event.OccurredAt, event.NoticeKind)
			m.markViewportBlockDirty(block.BlockID())
			return m, m.requestStreamViewportSync()
		}
	}
	return m.appendEventStreamTranscriptText(text)
}

func shouldAnchorMainNotice(event TranscriptEvent) bool {
	return event.Scope == ACPProjectionMain && transcriptMainTurnKey(event) != ""
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
		return m.handleParticipantTurnStream(transcriptParticipantTurnKey(event), transcriptNarrativeStreamKind(event.NarrativeKind), event.Actor, event.Text, event.Final, event.OccurredAt)
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

func transcriptNarrativeStreamKind(kind TranscriptNarrativeKind) string {
	if kind == TranscriptNarrativeReasoning {
		return "reasoning"
	}
	return "answer"
}

func (m *Model) applyTranscriptMainNarrative(event TranscriptEvent) (tea.Model, tea.Cmd) {
	block := m.ensureMainACPTurnBlock(transcriptMainTurnKey(event))
	if block == nil {
		return m, nil
	}
	if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
		block.StartedAt = event.OccurredAt
	}
	text := tuikit.SanitizeLogText(event.Text)
	if event.NarrativeKind == TranscriptNarrativeReasoning {
		if event.Final {
			block.ReplaceFinalStreamChunk(SEReasoning, text, event.OccurredAt)
		} else if text != "" {
			block.AppendStreamChunk(SEReasoning, text, event.OccurredAt)
		}
	} else {
		if event.Final {
			closeLatestReasoningTiming(block.Events, event.OccurredAt)
		}
		if event.Final {
			block.ReplaceFinalStreamChunk(SEAssistant, text, event.OccurredAt)
			m.releaseReplayedMainACPTurn(block, event.OccurredAt, "completed")
		} else if text != "" {
			block.AppendStreamChunk(SEAssistant, text, event.OccurredAt)
		}
	}
	m.markViewportBlockDirty(block.BlockID())
	return m, m.requestStreamViewportSync()
}

func (m *Model) applyTranscriptPlan(event TranscriptEvent) (tea.Model, tea.Cmd) {
	m.prepareForTranscriptScope(event.Scope)
	entries := make([]planEntryState, 0, len(event.PlanEntries))
	for _, entry := range event.PlanEntries {
		entries = append(entries, planEntryState(entry))
	}
	switch event.Scope {
	case ACPProjectionParticipant:
		block := m.ensureParticipantTurnBlock(transcriptParticipantTurnKey(event), event.Actor)
		if block == nil {
			return m, nil
		}
		m.activeParticipantTurnSessionID = strings.TrimSpace(block.SessionID)
		if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
			block.StartedAt = event.OccurredAt
		}
		if state := strings.ToLower(strings.TrimSpace(block.Status)); state == "initializing" || state == "prompting" {
			block.Status = "running"
		}
		block.UpdatePlan(entries)
		m.markViewportBlockDirty(block.BlockID())
		return m, m.requestStreamViewportSync()
	case ACPProjectionSubagent:
		if eventTargetsParentToolPanel(event) {
			return m, nil
		}
		return m.applyTranscriptPlanToParticipantTurn(event, entries)
	default:
		block := m.ensureMainACPTurnBlock(transcriptMainTurnKey(event))
		if block == nil {
			return m, nil
		}
		if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
			block.StartedAt = event.OccurredAt
		}
		block.UpdatePlan(entries)
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
		return m.handleParticipantStatusMsg(ParticipantStatusMsg{
			SessionID:       transcriptParticipantTurnKey(event),
			State:           firstNonEmpty(strings.TrimSpace(event.State), "waiting_approval"),
			ApprovalTool:    event.ApprovalTool,
			ApprovalCommand: event.ApprovalCommand,
			OccurredAt:      event.OccurredAt,
		})
	case ACPProjectionSubagent:
		if eventTargetsParentToolPanel(event) {
			return m, nil
		}
		return m.applyTranscriptStatusToParticipantTurn(event, firstNonEmpty(strings.TrimSpace(event.State), "waiting_approval"), event.ApprovalTool, event.ApprovalCommand)
	default:
		block := m.ensureMainACPTurnBlock(transcriptMainTurnKey(event))
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
		block := m.ensureParticipantTurnBlock(transcriptParticipantTurnKey(event), event.Actor)
		if block == nil {
			return m, nil
		}
		block.AddApprovalReviewEvent(event.ToolCallID, event.ApprovalTool, event.ApprovalCommand, event.ApprovalStatus, event.ApprovalRisk, event.ApprovalAuth, event.ApprovalText)
		m.markViewportBlockDirty(block.BlockID())
		return m, m.requestStreamViewportSync()
	case ACPProjectionSubagent:
		if eventTargetsParentToolPanel(event) {
			return m, nil
		}
		return m.applyTranscriptApprovalReviewToParticipantTurn(event)
	default:
		block := m.ensureMainACPTurnBlock(transcriptMainTurnKey(event))
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
		return m.handleParticipantStatusMsg(ParticipantStatusMsg{
			SessionID:  transcriptParticipantTurnKey(event),
			State:      event.State,
			OccurredAt: event.OccurredAt,
		})
	}
}

func (m *Model) applyTranscriptLifecycle(event TranscriptEvent) (tea.Model, tea.Cmd) {
	m.prepareForTranscriptScope(event.Scope)
	switch event.Scope {
	case ACPProjectionParticipant:
		return m.handleParticipantStatusMsg(ParticipantStatusMsg{
			SessionID:  transcriptParticipantTurnKey(event),
			State:      event.State,
			OccurredAt: event.OccurredAt,
		})
	case ACPProjectionSubagent:
		if eventTargetsParentToolPanel(event) {
			return m, nil
		}
		return m.applyTranscriptStatusToParticipantTurn(event, event.State, "", "")
	default:
		block := m.ensureMainACPTurnBlock(transcriptMainTurnKey(event))
		if block == nil {
			return m, nil
		}
		if event.State == "attempt_reset" {
			block.ClearActiveBuffers()
		} else {
			if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
				block.StartedAt = event.OccurredAt
			}
			if !m.turnRunning() && eventstream.IsTerminalLifecycleState(event.State) {
				m.releaseReplayedMainACPTurn(block, event.OccurredAt, event.State)
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
		return m.handleParticipantTurnStream(event.ScopeID, transcriptNarrativeStreamKind(event.NarrativeKind), subagentTranscriptActor(event), event.Text, event.Final, event.OccurredAt)
	}
	if event.MirroredToParentTool {
		return m, nil
	}
	if event.NarrativeKind != TranscriptNarrativeAssistant {
		return m, nil
	}
	return m.applyAnchoredSubagentNarrativeToTool(event)
}

func eventTargetsParentToolPanel(event TranscriptEvent) bool {
	return event.Scope == ACPProjectionSubagent &&
		strings.TrimSpace(event.ScopeID) != "" &&
		strings.TrimSpace(event.AnchorToolCallID) != ""
}

func (m *Model) applyAnchoredSubagentNarrativeToTool(event TranscriptEvent) (tea.Model, tea.Cmd) {
	if m == nil {
		return m, nil
	}
	callID := strings.TrimSpace(event.AnchorToolCallID)
	if callID == "" {
		return m, nil
	}
	text := tuikit.SanitizeLogText(event.Text)
	if strings.TrimSpace(text) == "" {
		return m, nil
	}
	toolName := strings.TrimSpace(event.AnchorToolName)
	for _, docBlock := range m.doc.Blocks() {
		block, ok := docBlock.(*MainACPTurnBlock)
		if !ok || !mainACPBlockHasToolCall(block, callID) {
			continue
		}
		block.UpdateToolWithMeta(callID, toolName, "", text, event.Final, false, ToolUpdateMeta{
			ToolKind: "execute",
			TaskID:   strings.TrimSpace(event.ScopeID),
		})
		m.markViewportBlockDirty(block.BlockID())
		return m, m.requestStreamViewportSync()
	}
	return m, nil
}

func (m *Model) applyTranscriptPlanToParticipantTurn(event TranscriptEvent, entries []planEntryState) (tea.Model, tea.Cmd) {
	block := m.ensureParticipantTurnBlock(event.ScopeID, subagentTranscriptActor(event))
	if block == nil {
		return m, nil
	}
	m.activeParticipantTurnSessionID = strings.TrimSpace(block.SessionID)
	if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
		block.StartedAt = event.OccurredAt
	}
	if state := strings.ToLower(strings.TrimSpace(block.Status)); state == "initializing" || state == "prompting" || state == "waiting_approval" || participantTurnIsTerminal(state) {
		block.Status = "running"
	}
	block.UpdatePlan(entries)
	m.markViewportBlockDirty(block.BlockID())
	return m, m.requestStreamViewportSync()
}

func (m *Model) applyTranscriptStatusToParticipantTurn(event TranscriptEvent, stateName, approvalTool, approvalCommand string) (tea.Model, tea.Cmd) {
	block := m.ensureParticipantTurnBlock(event.ScopeID, subagentTranscriptActor(event))
	if block == nil {
		return m, nil
	}
	block.SetStatus(stateName, approvalTool, approvalCommand, event.OccurredAt)
	m.markViewportBlockDirty(block.BlockID())
	return m, m.requestStreamViewportSync()
}

func (m *Model) applyTranscriptApprovalReviewToParticipantTurn(event TranscriptEvent) (tea.Model, tea.Cmd) {
	block := m.ensureParticipantTurnBlock(event.ScopeID, subagentTranscriptActor(event))
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

func transcriptMainTurnKey(event TranscriptEvent) string {
	if event.Scope != ACPProjectionMain {
		return strings.TrimSpace(event.ScopeID)
	}
	return firstNonEmpty(strings.TrimSpace(event.TurnID), strings.TrimSpace(event.ScopeID))
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
		TaskID:          event.ToolTaskID,
		TaskAction:      event.ToolTaskAction,
		TaskInput:       event.ToolTaskInput,
		TaskTargetKind:  event.ToolTaskTargetKind,
		ToolKind:        event.ToolKind,
		FullArgs:        event.ToolFullArgs,
		Terminal:        event.ToolTerminal,
		OutputSynthetic: event.ToolOutputSynthetic,
		OutputTerminal:  event.ToolOutputTerminal,
	}
}
