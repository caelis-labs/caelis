package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"
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
		return m.appendPlainTranscriptBlock(event.Text)
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
		return m.handleUserMessageMsg(UserMessageMsg{Text: event.Text}), nil
	case TranscriptNarrativeSystem, TranscriptNarrativeNotice:
		return m.appendPlainTranscriptBlock(event.Text)
	}

	m.prepareForTranscriptScope(event.Scope)
	switch event.Scope {
	case ACPProjectionParticipant:
		return m.handleParticipantTurnStream(event.ScopeID, transcriptNarrativeStreamKind(event.NarrativeKind), event.Actor, event.Text, event.Final, event.OccurredAt)
	case ACPProjectionSubagent:
		return m.applyTranscriptSubagentNarrative(event)
	default:
		return m.applyTranscriptMainNarrative(event)
	}
}

func transcriptNarrativeStreamKind(kind TranscriptNarrativeKind) string {
	if kind == TranscriptNarrativeReasoning {
		return "reasoning"
	}
	return "answer"
}

func (m *Model) applyTranscriptMainNarrative(event TranscriptEvent) (tea.Model, tea.Cmd) {
	block := m.ensureMainACPTurnBlock(strings.TrimSpace(event.ScopeID))
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
		block := m.ensureParticipantTurnBlock(event.ScopeID, event.Actor)
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
		if !m.shouldRenderSubagentPanelEvent(event) {
			return m, nil
		}
		sessionKey, state := m.ensureSubagentSessionState(event.ScopeID, "", "")
		panel := m.ensureSubagentPanelBlock(event.ScopeID, "", "", "", "", false)
		if state == nil || panel == nil {
			return m, nil
		}
		if !event.OccurredAt.IsZero() && (state.StartedAt.IsZero() || event.OccurredAt.Before(state.StartedAt)) {
			state.StartedAt = event.OccurredAt
		}
		if strings.EqualFold(state.Status, "waiting_approval") {
			state.Status = "running"
		} else if isTerminalSubagentState(state.Status) {
			state.ReviveFromTerminal()
		}
		panel.bindSession(state)
		state.UpdatePlan(entries)
		m.reviveSubagentPanel(panel, false)
		m.syncSubagentSessionPanels(sessionKey)
		m.markViewportBlockDirty(panel.BlockID())
		return m, m.requestStreamViewportSync()
	default:
		block := m.ensureMainACPTurnBlock(strings.TrimSpace(event.ScopeID))
		if block == nil {
			return m, nil
		}
		m.activeParticipantTurnSessionID = strings.TrimSpace(block.SessionID)
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
			SessionID:       event.ScopeID,
			State:           firstNonEmpty(strings.TrimSpace(event.State), "waiting_approval"),
			ApprovalTool:    event.ApprovalTool,
			ApprovalCommand: event.ApprovalCommand,
			OccurredAt:      event.OccurredAt,
		})
	case ACPProjectionSubagent:
		if !m.shouldRenderSubagentPanelEvent(event) {
			return m, nil
		}
		return m.handleSubagentStatus(SubagentStatusMsg{
			SpawnID:         event.ScopeID,
			State:           firstNonEmpty(strings.TrimSpace(event.State), "waiting_approval"),
			ApprovalTool:    event.ApprovalTool,
			ApprovalCommand: event.ApprovalCommand,
			OccurredAt:      event.OccurredAt,
		})
	default:
		block := m.ensureMainACPTurnBlock(strings.TrimSpace(event.ScopeID))
		if block == nil {
			return m, nil
		}
		block.SetStatus(firstNonEmpty(strings.TrimSpace(event.State), "waiting_approval"), event.ApprovalTool, event.ApprovalCommand, event.OccurredAt)
		m.markViewportBlockDirty(block.BlockID())
		return m, m.requestStreamViewportSync()
	}
}

func (m *Model) applyTranscriptApprovalReview(event TranscriptEvent) (tea.Model, tea.Cmd) {
	m.approvalReviewHint = ""
	if strings.TrimSpace(event.AnchorToolCallID) != "" {
		if applied, cmd := m.applyAnchoredApprovalReviewToTool(event); applied {
			return m, cmd
		}
	}
	switch event.Scope {
	case ACPProjectionParticipant:
		block := m.ensureParticipantTurnBlock(event.ScopeID, event.Actor)
		if block == nil {
			return m, nil
		}
		block.AddApprovalReviewEvent(event.ToolCallID, event.ApprovalTool, event.ApprovalCommand, event.ApprovalStatus, event.ApprovalRisk, event.ApprovalAuth, event.ApprovalText)
		m.markViewportBlockDirty(block.BlockID())
		return m, m.requestStreamViewportSync()
	case ACPProjectionSubagent:
		if !m.shouldRenderSubagentPanelEvent(event) {
			return m, nil
		}
		sessionKey, state := m.ensureSubagentSessionState(event.ScopeID, "", "")
		panel := m.ensureSubagentPanelBlock(event.ScopeID, "", "", "", "", false)
		if state == nil || panel == nil {
			return m, nil
		}
		switch {
		case strings.EqualFold(state.Status, "waiting_approval"):
			state.Status = "running"
		case isTerminalSubagentState(state.Status):
			state.ReviveFromTerminal()
		}
		panel.bindSession(state)
		state.AddApprovalReviewEvent(event.ToolCallID, event.ApprovalTool, event.ApprovalCommand, event.ApprovalStatus, event.ApprovalRisk, event.ApprovalAuth, event.ApprovalText)
		m.reviveSubagentPanel(panel, false)
		m.syncSubagentSessionPanels(sessionKey)
		m.markViewportBlockDirty(panel.BlockID())
		return m, m.requestStreamViewportSync()
	default:
		block := m.ensureMainACPTurnBlock(strings.TrimSpace(event.ScopeID))
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
	toolName := firstNonEmpty(strings.TrimSpace(event.AnchorToolName), "SPAWN")
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
	review := SubagentEvent{
		ApprovalTool:    strings.TrimSpace(event.ApprovalTool),
		ApprovalCommand: strings.TrimSpace(event.ApprovalCommand),
		ApprovalStatus:  strings.TrimSpace(event.ApprovalStatus),
		ApprovalRisk:    strings.TrimSpace(event.ApprovalRisk),
		ApprovalAuth:    strings.TrimSpace(event.ApprovalAuth),
		ApprovalText:    strings.TrimSpace(event.ApprovalText),
	}
	display := approvalReviewDisplayParts(review)
	status := strings.TrimSpace(display.Status)
	if status == "" {
		status = "reviewed"
	}
	line := "Approval review " + status
	if tool := strings.TrimSpace(event.ApprovalTool); tool != "" {
		line += " " + tool
	}
	if command := strings.TrimSpace(event.ApprovalCommand); command != "" {
		line += " " + command
	}
	meta := make([]string, 0, 2)
	if risk := strings.TrimSpace(display.Risk); risk != "" {
		meta = append(meta, "risk: "+risk)
	}
	if authorization := strings.TrimSpace(display.Authorization); authorization != "" {
		meta = append(meta, "authorization: "+authorization)
	}
	if len(meta) > 0 {
		line += " (" + strings.Join(meta, ", ") + ")"
	}
	if rationale := strings.TrimSpace(display.Rationale); rationale != "" {
		line += "\n" + rationale
	}
	return line + "\n"
}

func (m *Model) applyTranscriptParticipant(event TranscriptEvent) (tea.Model, tea.Cmd) {
	m.prepareForTranscriptScope(event.Scope)
	switch event.Scope {
	case ACPProjectionSubagent:
		if !m.shouldRenderSubagentPanelEvent(event) {
			return m, nil
		}
		return m.handleSubagentStatus(SubagentStatusMsg{
			SpawnID:    event.ScopeID,
			State:      event.State,
			OccurredAt: event.OccurredAt,
		})
	default:
		return m.handleParticipantStatusMsg(ParticipantStatusMsg{
			SessionID:  event.ScopeID,
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
			SessionID:  event.ScopeID,
			State:      event.State,
			OccurredAt: event.OccurredAt,
		})
	case ACPProjectionSubagent:
		if !m.shouldRenderSubagentPanelEvent(event) {
			return m, nil
		}
		return m.handleSubagentStatus(SubagentStatusMsg{
			SpawnID:    event.ScopeID,
			State:      event.State,
			OccurredAt: event.OccurredAt,
		})
	default:
		block := m.ensureMainACPTurnBlock(strings.TrimSpace(event.ScopeID))
		if block == nil {
			return m, nil
		}
		block.SetStatus(event.State, "", "", event.OccurredAt)
		m.markViewportBlockDirty(block.BlockID())
		return m, m.requestStreamViewportSync()
	}
}

func (m *Model) applyTranscriptSubagentNarrative(event TranscriptEvent) (tea.Model, tea.Cmd) {
	if !m.shouldRenderSubagentPanelEvent(event) {
		return m, nil
	}
	sessionKey, state := m.ensureSubagentSessionState(event.ScopeID, "", "")
	panel := m.ensureSubagentPanelBlock(event.ScopeID, "", "", "", "", false)
	if state == nil || panel == nil {
		return m, nil
	}
	if !event.OccurredAt.IsZero() && (state.StartedAt.IsZero() || event.OccurredAt.Before(state.StartedAt)) {
		state.StartedAt = event.OccurredAt
	}
	switch {
	case strings.EqualFold(state.Status, "waiting_approval"):
		state.Status = "running"
	case isTerminalSubagentState(state.Status):
		state.ReviveFromTerminal()
	}
	panel.bindSession(state)
	text := tuikit.SanitizeLogText(event.Text)
	if event.NarrativeKind == TranscriptNarrativeReasoning {
		if event.Final {
			panel.ReplaceFinalStreamChunk(SEReasoning, text, event.OccurredAt)
		} else {
			panel.AppendStreamChunk(SEReasoning, text, event.OccurredAt)
		}
	} else {
		if event.Final {
			closeLatestReasoningTiming(state.Events, event.OccurredAt)
			state.eventsGen++
		}
		if event.Final {
			panel.ReplaceFinalStreamChunk(SEAssistant, text, event.OccurredAt)
		} else {
			panel.AppendStreamChunk(SEAssistant, text, event.OccurredAt)
		}
	}
	m.reviveSubagentPanel(panel, false)
	m.syncSubagentSessionPanels(sessionKey)
	m.markViewportBlockDirty(panel.BlockID())
	return m, m.requestStreamViewportSync()
}

func (m *Model) shouldRenderSubagentPanelEvent(event TranscriptEvent) bool {
	return strings.TrimSpace(event.AnchorToolCallID) == ""
}

func transcriptToolUpdateMeta(event TranscriptEvent) ToolUpdateMeta {
	return ToolUpdateMeta{
		TaskID:          event.ToolTaskID,
		TaskAction:      event.ToolTaskAction,
		TaskInput:       event.ToolTaskInput,
		TaskTargetKind:  event.ToolTaskTargetKind,
		ToolKind:        event.ToolKind,
		FullArgs:        event.ToolFullArgs,
		OutputSynthetic: event.ToolOutputSynthetic,
	}
}
