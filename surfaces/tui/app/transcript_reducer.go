package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/surfaces/transcript"
)

type transcriptToolMutation struct {
	callID string
	name   string
	args   string
	stream string
	output string
	final  bool
	err    bool
	meta   ToolUpdateMeta
}

func transcriptToolMutationFromEvent(event TranscriptEvent) transcriptToolMutation {
	return transcriptToolMutation{
		callID: event.ToolCallID,
		name:   event.ToolName,
		args:   event.ToolArgs,
		stream: firstNonEmpty(strings.TrimSpace(event.ToolStream), "stdout"),
		output: event.ToolOutput,
		final:  event.Final,
		err:    event.ToolError,
		meta:   transcriptToolUpdateMeta(event),
	}
}

func (m *Model) applyTranscriptToolToParticipant(event TranscriptEvent, mutation transcriptToolMutation) (tea.Model, tea.Cmd) {
	if action, hidden := hiddenTaskControlAction(event); hidden {
		mutation.meta.TaskAction = action
		var changed bool
		if action == "wait" || action == "read" {
			if owner := m.absorbParticipantCommandTaskObservation(event, &mutation); owner != nil {
				m.markViewportBlockDirty(owner.BlockID())
				changed = true
			}
		}
		block := m.findParticipantTurnBlock(transcriptParticipantTurnKey(event))
		if block != nil {
			bindParticipantTurnBlock(block, event)
			block.sealNarrativeSegmentWithGap()
			m.markViewportBlockDirty(block.BlockID())
			changed = true
		}
		if !changed {
			// No participant narrative exists yet, so there is no segment to
			// seal and no empty presentation block should be manufactured.
			return m, nil
		}
		return m, m.requestStreamViewportSync()
	}
	block := m.ensureParticipantTurnBlock(transcriptParticipantTurnKey(event), participantTranscriptActor(event))
	if block == nil {
		return m, nil
	}
	bindParticipantTurnBlock(block, event)
	if !event.OccurredAt.IsZero() && (block.StartedAt.IsZero() || event.OccurredAt.Before(block.StartedAt)) {
		block.StartedAt = event.OccurredAt
	}
	if state := strings.ToLower(strings.TrimSpace(block.Status)); state == "initializing" || state == "prompting" {
		block.Status = "running"
	}
	block.UpdateToolWithMeta(mutation.callID, mutation.name, mutation.args, mutation.output, mutation.final, mutation.err, mutation.meta)
	m.markViewportBlockDirty(block.BlockID())
	return m, m.requestStreamViewportSync()
}

func (m *Model) applyTranscriptToolToSubagent(event TranscriptEvent, mutation transcriptToolMutation) (tea.Model, tea.Cmd) {
	if eventTargetsParentToolPanel(event) {
		m.markAnchoredSubagentNarrativeBoundary(event)
		return m, nil
	}
	return m.applyTranscriptToolToParticipant(event, mutation)
}

func (m *Model) markAnchoredSubagentNarrativeBoundary(event TranscriptEvent) {
	if m == nil || m.doc == nil {
		return
	}
	callID := strings.TrimSpace(event.AnchorToolCallID)
	taskID := strings.TrimSpace(event.ScopeID)
	if block := m.activeMainTaskWriteBlock(taskID); block != nil {
		if block.markSubagentNarrativeBoundary(callID, taskID) {
			return
		}
	}
	for _, docBlock := range m.doc.Blocks() {
		block, ok := docBlock.(*MainACPTurnBlock)
		if !ok || !mainACPBlockHasToolCall(block, callID) {
			continue
		}
		if block.markSubagentNarrativeBoundary(callID, taskID) {
			return
		}
	}
}

func (m *Model) applyTranscriptToolToMain(event TranscriptEvent, mutation transcriptToolMutation) (tea.Model, tea.Cmd) {
	if action, hidden := hiddenTaskControlAction(event); hidden {
		mutation.meta.TaskAction = action
		if m.doc != nil {
			if block, _ := m.doc.Find(strings.TrimSpace(m.mainTimelineTailID)).(*MainACPTurnBlock); block != nil {
				block.sealNarrativeSegmentWithGap()
				m.markViewportBlockDirty(block.BlockID())
			}
		}
		if action == "wait" || action == "read" {
			if owner := m.absorbCommandTaskObservation(event, &mutation); owner != nil {
				m.markViewportBlockDirty(owner.BlockID())
			}
		}
		return m, m.requestStreamViewportSync()
	}
	block := m.mainBlockForStreamOwner(event, mutation)
	if block == nil {
		block = m.mainBlockForAnchor(event, mainToolAnchor(mutation.callID))
	}
	if block == nil {
		return m, nil
	}
	block.UpdateToolWithMeta(mutation.callID, mutation.name, mutation.args, mutation.output, mutation.final, mutation.err, mutation.meta)
	m.observeToolPresentationOwner(block, event)
	m.markViewportBlockDirty(block.BlockID())
	return m, m.requestStreamViewportSync()
}

// TASK wait/read/cancel calls remain canonical model-visible observations, but they
// are control mechanics rather than transcript panels. The TUI reports their
// live action and elapsed time in the running hint and consumes the physical
// row here. A Task observation may still repair its owning Spawn or command
// panel before being consumed. Failed terminal controls remain visible so their
// error is not lost. Main and participant lanes repair their own command owner
// before consuming the control row.
func hiddenTaskControlAction(event TranscriptEvent) (string, bool) {
	if !strings.EqualFold(toolSemanticName(event.ToolName, event.ToolKind), "TASK") {
		return "", false
	}
	action := strings.ToLower(strings.TrimSpace(event.ToolTaskAction))
	switch action {
	case "wait", "read", "cancel":
		if event.Final && event.ToolError {
			return action, false
		}
		return action, true
	default:
		return "", false
	}
}

// absorbCommandTaskObservation folds a durable TASK read/wait observation into
// the original async command panel. Cursors make this order-independent with
// transient exact stream delivery; legacy compact observations remain
// recovery-only and never append onto already rendered bytes.
func (m *Model) absorbCommandTaskObservation(event TranscriptEvent, mutation *transcriptToolMutation) *MainACPTurnBlock {
	if m == nil || m.doc == nil || mutation == nil || mutation.err ||
		!strings.EqualFold(toolSemanticName(mutation.name, mutation.meta.ToolKind), "TASK") ||
		!commandTaskTargetKind(mutation.meta.TaskTargetKind) ||
		!taskObservationHasOutput(*mutation) {
		return nil
	}
	action := strings.ToLower(strings.TrimSpace(mutation.meta.TaskAction))
	if action != "read" && action != "wait" {
		return nil
	}
	owner, ok := m.runningActivityTracker.presentationOwner(
		mutation.meta.TaskHandle,
		event.AnchorToolCallID,
		runningTargetShell,
	)
	if !ok {
		return nil
	}
	block, _ := m.doc.Find(owner.BlockID).(*MainACPTurnBlock)
	if block == nil || !absorbCommandTaskObservationIntoEvents(block.Events, mutation) {
		return nil
	}
	return block
}

func absorbCommandTaskObservationIntoEvents(events []SubagentEvent, mutation *transcriptToolMutation) bool {
	if mutation == nil || mutation.err || !taskObservationHasOutput(*mutation) {
		return false
	}
	taskHandle := strings.TrimSpace(mutation.meta.TaskHandle)
	if taskHandle == "" {
		return false
	}
	for i := len(events) - 1; i >= 0; i-- {
		owner := &events[i]
		ownerName := toolSemanticName(owner.Name, owner.ToolKind)
		if owner.Kind != SEToolCall || !sameTaskHandle(owner.TaskHandle, taskHandle) ||
			!isTerminalPanelToolEvent(*owner) || strings.EqualFold(ownerName, "SPAWN") || strings.EqualFold(ownerName, "TASK") {
			continue
		}
		switch {
		case mutation.meta.OutputTerminal:
			mergeTerminalOutputByCursor(owner, mutation.output, mutation.meta)
		case owner.OutputSynthetic || !renderableTextHasContent(owner.Output):
			// Older durable observations only carry compact latest_output.
			// Present that snapshot when no exact bytes exist, but mark it as
			// replaceable so a later exact stream frame can recover fidelity.
			owner.Output = mutation.output
			owner.OutputSynthetic = true
		}
		mutation.output = ""
		mutation.meta.OutputSynthetic = false
		mutation.meta.OutputTerminal = false
		return true
	}
	return false
}

func (m *Model) absorbParticipantCommandTaskObservation(event TranscriptEvent, mutation *transcriptToolMutation) *ParticipantTurnBlock {
	if m == nil || m.doc == nil || mutation == nil {
		return nil
	}
	participantID := transcriptParticipantLaneID(event)
	turnID := strings.TrimSpace(transcriptParticipantTurnKey(event))
	blocks := m.doc.Blocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		block, ok := blocks[i].(*ParticipantTurnBlock)
		if !ok || !participantTurnBlockMatchesLane(block, participantID, turnID) {
			continue
		}
		if absorbCommandTaskObservationIntoEvents(block.Events, mutation) {
			return block
		}
	}
	return nil
}

func bindParticipantTurnBlock(block *ParticipantTurnBlock, event TranscriptEvent) {
	if block == nil {
		return
	}
	participantID := transcriptParticipantLaneID(event)
	if participantID != "" && (block.ParticipantID == "" || block.ParticipantID == participantID) {
		block.ParticipantID = participantID
	}
}

func transcriptParticipantLaneID(event TranscriptEvent) string {
	return firstNonEmpty(
		strings.TrimSpace(event.ParticipantID),
		strings.TrimSpace(event.ScopeID),
	)
}

func participantTurnBlockMatchesLane(block *ParticipantTurnBlock, participantID string, turnID string) bool {
	if block == nil {
		return false
	}
	if participantID != "" {
		return strings.TrimSpace(block.ParticipantID) == participantID
	}
	return turnID != "" && strings.TrimSpace(block.SessionID) == turnID
}

func taskObservationHasOutput(mutation transcriptToolMutation) bool {
	if mutation.meta.OutputTerminal {
		return mutation.output != ""
	}
	return renderableTextHasContent(mutation.output)
}

func commandTaskTargetKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "command", "terminal":
		return true
	default:
		return false
	}
}

// mainBlockForStreamOwner lets a later TASK observer deliver bytes to the
// original async tool panel. Cross-Turn routing is deliberately limited to
// projector-owned stream frames and requires both physical task and call
// identity, so an ordinary reused CallID still starts in the current Turn.
func (m *Model) mainBlockForStreamOwner(event TranscriptEvent, mutation transcriptToolMutation) *MainACPTurnBlock {
	if m == nil || m.doc == nil {
		return nil
	}
	mode := strings.ToLower(transcript.MetaString(event.Meta, "caelis", "runtime", "stream", "mode"))
	if mode != "append" && mode != "final" {
		return nil
	}
	callID := strings.TrimSpace(mutation.callID)
	taskID := strings.TrimSpace(mutation.meta.TaskHandle)
	if callID == "" || taskID == "" {
		return nil
	}
	blocks := m.doc.Blocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		block, ok := blocks[i].(*MainACPTurnBlock)
		if !ok || !mainACPBlockHasStreamOwner(block, callID, taskID, mutation.name) {
			continue
		}
		return block
	}
	return nil
}

func mainACPBlockHasStreamOwner(block *MainACPTurnBlock, callID string, taskID string, toolName string) bool {
	if block == nil {
		return false
	}
	callID = strings.TrimSpace(callID)
	taskID = strings.TrimSpace(taskID)
	semanticName := toolSemanticName(toolName, "")
	for i := len(block.Events) - 1; i >= 0; i-- {
		event := block.Events[i]
		if event.Kind != SEToolCall || strings.TrimSpace(event.CallID) != callID || !sameTaskHandle(event.TaskHandle, taskID) {
			continue
		}
		eventName := toolSemanticName(event.Name, event.ToolKind)
		return semanticName == "" || eventName == "" || strings.EqualFold(eventName, semanticName)
	}
	return false
}
