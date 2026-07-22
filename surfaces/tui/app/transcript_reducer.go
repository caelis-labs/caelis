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
	block := m.ensureParticipantTurnBlock(transcriptParticipantTurnKey(event), participantTranscriptActor(event))
	if block == nil {
		return m, nil
	}
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
	if owner, consumed := m.absorbSubagentTaskResult(event, &mutation); consumed {
		if owner != nil {
			m.markViewportBlockDirty(owner.BlockID())
		}
		return m, m.requestStreamViewportSync()
	}
	if owner := m.absorbCommandTaskResult(&mutation); owner != nil {
		m.markViewportBlockDirty(owner.BlockID())
	}
	block := m.mainBlockForStreamOwner(event, mutation)
	if block == nil {
		block = m.mainBlockForAnchor(event, mainToolAnchor(mutation.callID))
	}
	if block == nil {
		return m, nil
	}
	block.UpdateToolWithMeta(mutation.callID, mutation.name, mutation.args, mutation.output, mutation.final, mutation.err, mutation.meta)
	m.markViewportBlockDirty(block.BlockID())
	return m, m.requestStreamViewportSync()
}

// absorbSubagentTaskResult routes one canonical TASK wait result to the exact
// Spawn call identified by the typed Envelope parent relation. Successful
// absorption consumes the observer result so the Surface does not create a
// second physical TASK panel.
func (m *Model) absorbSubagentTaskResult(event TranscriptEvent, mutation *transcriptToolMutation) (*MainACPTurnBlock, bool) {
	if m == nil || m.doc == nil || mutation == nil || !mutation.final ||
		!strings.EqualFold(toolSemanticName(mutation.name, mutation.meta.ToolKind), "TASK") ||
		!strings.EqualFold(strings.TrimSpace(mutation.meta.TaskAction), "wait") ||
		!strings.EqualFold(strings.TrimSpace(mutation.meta.TaskTargetKind), "subagent") {
		return nil, false
	}
	parentCall := strings.TrimSpace(event.AnchorToolCallID)
	parentTool := strings.TrimSpace(event.AnchorToolName)
	if parentCall == "" || !strings.EqualFold(toolSemanticName(parentTool, ""), "SPAWN") {
		return nil, false
	}
	blocks := m.doc.Blocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		block, ok := blocks[i].(*MainACPTurnBlock)
		if !ok {
			continue
		}
		for j := len(block.Events) - 1; j >= 0; j-- {
			owner := block.Events[j]
			if owner.Kind != SEToolCall || strings.TrimSpace(owner.CallID) != parentCall ||
				!strings.EqualFold(toolSemanticName(owner.Name, owner.ToolKind), "SPAWN") {
				continue
			}
			ownerMeta := mutation.meta
			ownerMeta.ToolKind = ""
			ownerMeta.TaskAction = ""
			ownerMeta.TaskInput = ""
			ownerMeta.OutputNarrative = false
			ownerMeta.OutputAuthoritative = true
			ownerMeta.OutputTerminal = false
			ownerMeta.OutputGapBefore = false
			block.UpdateToolWithMeta(parentCall, parentTool, "", mutation.output, true, mutation.err, ownerMeta)
			return block, true
		}
	}
	return nil, false
}

// absorbCommandTaskResult uses the durable TASK wait snapshot only as a
// recovery fallback for an async command's transient terminal stream. If the
// owner already has real bytes, the snapshot stays hidden to avoid duplicate
// output; if those bytes were lost across restart/eviction, it fills the
// original panel without pretending the snapshot is an exact byte delta.
func (m *Model) absorbCommandTaskResult(mutation *transcriptToolMutation) *MainACPTurnBlock {
	if m == nil || m.doc == nil || mutation == nil || mutation.err ||
		!strings.EqualFold(toolSemanticName(mutation.name, mutation.meta.ToolKind), "TASK") ||
		!strings.EqualFold(strings.TrimSpace(mutation.meta.TaskAction), "wait") ||
		!commandTaskTargetKind(mutation.meta.TaskTargetKind) ||
		!renderableTextHasContent(mutation.output) {
		return nil
	}
	taskID := strings.TrimSpace(mutation.meta.TaskHandle)
	if taskID == "" {
		return nil
	}
	blocks := m.doc.Blocks()
	for i := len(blocks) - 1; i >= 0; i-- {
		block, ok := blocks[i].(*MainACPTurnBlock)
		if !ok {
			continue
		}
		for j := len(block.Events) - 1; j >= 0; j-- {
			owner := &block.Events[j]
			ownerName := toolSemanticName(owner.Name, owner.ToolKind)
			if owner.Kind != SEToolCall || strings.TrimSpace(owner.TaskHandle) != taskID ||
				!isTerminalPanelToolEvent(*owner) || strings.EqualFold(ownerName, "SPAWN") || strings.EqualFold(ownerName, "TASK") {
				continue
			}
			if owner.OutputSynthetic || !renderableTextHasContent(owner.Output) {
				owner.Output = mutation.output
				owner.OutputSynthetic = false
			}
			mutation.output = ""
			mutation.meta.OutputSynthetic = false
			mutation.meta.OutputTerminal = false
			return block
		}
	}
	return nil
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
		if event.Kind != SEToolCall || strings.TrimSpace(event.CallID) != callID || strings.TrimSpace(event.TaskHandle) != taskID {
			continue
		}
		eventName := toolSemanticName(event.Name, event.ToolKind)
		return semanticName == "" || eventName == "" || strings.EqualFold(eventName, semanticName)
	}
	return false
}
