package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
)

const subagentOutputRenderInterval = 50 * time.Millisecond

type subagentOutputRenderTickMsg struct {
	callID string
}

// subagentOutputView is a transient, presentation-only projection of one
// anchored child Task. Its block reuses the Side ACP renderer but is not part
// of the main Document and never becomes Session or Task authority. Views and
// their complete child transcript are retained until the Session changes.
type subagentOutputView struct {
	callID     string
	taskHandle string
	actor      string
	title      string
	document   *Document
	turnBlocks map[string]*ParticipantTurnBlock
	turnID     string
	// block is the current transcript block. Historical blocks remain in
	// document and are rendered in stream order; TurnID is internal routing
	// metadata, never overlay identity or lifecycle authority.
	block           *ParticipantTurnBlock
	revision        uint64
	renderReady     bool
	renderScheduled bool
	renderCache     subagentOutputRenderCache
	seenProjections map[string]struct{}
}

type subagentOutputRenderCache struct {
	revision  uint64
	width     int
	height    int
	termWidth int
	themeKey  string
	workspace string
	rows      []RenderedRow
	fixedRows []string
	renders   uint64
}

func (m *Model) observeSubagentOutputEvents(events []TranscriptEvent) bool {
	if m == nil {
		return false
	}
	changed := false
	for _, event := range events {
		if event.Scope == ACPProjectionMain && event.Kind == TranscriptEventTool &&
			names.CanonicalOrSelf(toolSemanticName(event.ToolName, event.ToolKind)) == names.Spawn {
			m.observeSubagentOutputOwner(event)
			changed = true
			continue
		}
		if !eventTargetsSubagentOutputView(event) {
			continue
		}
		view := m.ensureSubagentOutputView(event.AnchorToolCallID)
		if view == nil {
			continue
		}
		before := view.revision
		view.observeChildEvent(event)
		changed = changed || view.revision != before
	}
	return changed
}

func (m *Model) observeSubagentOutputOwner(event TranscriptEvent) {
	view := m.ensureSubagentOutputView(event.ToolCallID)
	if view == nil {
		return
	}
	if handle := strings.TrimSpace(event.ToolTaskHandle); handle != "" {
		view.taskHandle = handle
	}
	if title := sanitizeSpawnHeaderArgs(event.ToolArgs); title != "" {
		view.title = title
	}
	if actor := subagentOutputActor("", view.title, view.taskHandle); actor != "" {
		view.actor = actor
		view.block.Actor = participantActorDisplayName(actor)
	}
	view.touch(true)
}

func (m *Model) ensureSubagentOutputView(callID string) *subagentOutputView {
	if m == nil {
		return nil
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return nil
	}
	if m.subagentOutputViews == nil {
		m.subagentOutputViews = map[string]*subagentOutputView{}
	}
	if existing := m.subagentOutputViews[callID]; existing != nil {
		return existing
	}
	block := NewParticipantTurnBlock(callID, "")
	document := NewDocument()
	document.Append(block)
	view := &subagentOutputView{
		callID:      callID,
		document:    document,
		turnBlocks:  map[string]*ParticipantTurnBlock{},
		block:       block,
		renderReady: true,
	}
	m.subagentOutputViews[callID] = view
	return view
}

func (v *subagentOutputView) observeChildEvent(event TranscriptEvent) {
	if v == nil {
		return
	}
	if projectionID := strings.TrimSpace(event.SourceProjectionID); projectionID != "" {
		if _, seen := v.seenProjections[projectionID]; seen {
			return
		}
		if v.seenProjections == nil {
			v.seenProjections = make(map[string]struct{})
		}
		v.seenProjections[projectionID] = struct{}{}
	}
	if event.Kind == TranscriptEventNarrative && event.NarrativeKind == TranscriptNarrativeUser {
		v.appendInput(event)
		v.touch(false)
		return
	}
	block := v.blockForEvent(event)
	if block == nil {
		return
	}
	actor := strings.TrimSpace(v.actor)
	if actor == "" {
		actor = subagentOutputActor(event.Actor, v.title, v.taskHandle)
		if actor != "" {
			v.actor = actor
		}
	}
	result := applyTranscriptEventToParticipantTurn(block, event, participantTurnTranscriptPolicy{
		actor:                actor,
		appendFinalNarrative: true,
		hideTaskControl:      true,
		monotonicStatus:      true,
		reopenPlan:           true,
	})
	if !result.changed {
		return
	}
	if result.terminal {
		finalizeSubagentOutputNarratives(block)
	}
	v.touch(false)
}

func (v *subagentOutputView) blockForEvent(event TranscriptEvent) *ParticipantTurnBlock {
	if v == nil {
		return nil
	}
	turnID := strings.TrimSpace(event.TurnID)
	if turnID == "" {
		return v.block
	}
	if block := v.turnBlocks[turnID]; block != nil {
		return block
	}
	if v.document == nil {
		v.document = NewDocument()
	}
	if v.turnBlocks == nil {
		v.turnBlocks = map[string]*ParticipantTurnBlock{}
	}
	block := v.block
	if v.turnID != "" || block == nil || len(block.Events) > 0 {
		block = NewParticipantTurnBlock(turnID, v.actor)
		v.document.Append(block)
	}
	block.SessionID = turnID
	block.ParticipantID = v.taskHandle
	block.Actor = participantActorDisplayName(v.actor)
	if v.document.Find(block.BlockID()) == nil {
		v.document.Append(block)
	}
	if !event.OccurredAt.IsZero() {
		block.StartedAt = event.OccurredAt
	}
	v.turnBlocks[turnID] = block
	v.block = block
	v.turnID = turnID
	return block
}

func (v *subagentOutputView) appendInput(event TranscriptEvent) {
	if v == nil {
		return
	}
	text := strings.TrimSpace(transcriptNarrativeText(event))
	if text == "" {
		return
	}
	actor := strings.TrimPrefix(strings.TrimSpace(event.Actor), "@")
	if actor != "" && actor != "user" {
		text = actor + ": " + text
	}
	if v.document == nil {
		v.document = NewDocument()
	}
	if v.block != nil && len(v.block.Events) == 0 && v.turnID == "" {
		v.document.Remove(v.block.BlockID())
	}
	v.document.Append(NewUserNarrativeBlock(text))
}

func (v *subagentOutputView) touch(immediate bool) {
	if v == nil {
		return
	}
	v.revision++
	if immediate {
		v.renderReady = true
	}
}

func (v *subagentOutputView) prepareVisibleRender() {
	if v == nil {
		return
	}
	v.renderReady = true
	v.renderScheduled = false
}

func (m *Model) requestSubagentOutputRender() tea.Cmd {
	if m == nil || m.subagentOutputOverlay == nil {
		return nil
	}
	view := m.subagentOutputViews[m.subagentOutputOverlay.callID]
	if view == nil || view.renderScheduled {
		return nil
	}
	view.renderScheduled = true
	callID := view.callID
	return tea.Tick(subagentOutputRenderInterval, func(time.Time) tea.Msg {
		return subagentOutputRenderTickMsg{callID: callID}
	})
}

func (m *Model) handleSubagentOutputRenderTick(msg subagentOutputRenderTickMsg) (tea.Model, tea.Cmd) {
	if m == nil {
		return m, nil
	}
	view := m.subagentOutputViews[strings.TrimSpace(msg.callID)]
	if view == nil {
		return m, nil
	}
	view.renderScheduled = false
	if m.subagentOutputOverlay == nil || m.subagentOutputOverlay.callID != view.callID {
		return m, nil
	}
	view.renderReady = true
	return m, nil
}

func finalizeSubagentOutputNarratives(block *ParticipantTurnBlock) {
	if block == nil {
		return
	}
	for index := range block.Events {
		event := &block.Events[index]
		if event.Kind != SEAssistant && event.Kind != SEReasoning {
			continue
		}
		event.ActiveBuffer = nil
	}
}

func subagentOutputActor(actor, title, handle string) string {
	if actor = strings.TrimSpace(actor); actor != "" {
		return actor
	}
	title = strings.TrimSpace(title)
	if candidate, _, ok := strings.Cut(title, ":"); ok {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" && !strings.ContainsAny(candidate, " \t\n") {
			return candidate
		}
	}
	return strings.TrimSpace(handle)
}

// observeOwnerIdentity hydrates the stable Spawn-to-Task relation without
// interpreting Spawn tool completion as child Task completion. It is safe to
// call while opening or reopening the overlay.
func (v *subagentOutputView) observeOwnerIdentity(event SubagentEvent) {
	if v == nil || v.block == nil {
		return
	}
	if handle := strings.TrimSpace(event.TaskHandle); handle != "" {
		v.taskHandle = handle
	}
	if title := sanitizeSpawnHeaderArgs(event.Args); title != "" {
		v.title = title
	}
	if actor := subagentOutputActor("", v.title, v.taskHandle); actor != "" {
		v.actor = actor
		v.block.Actor = participantActorDisplayName(actor)
	}
}

func (m *Model) subagentOutputOwner(blockID, callID string) (SubagentEvent, bool) {
	if m == nil || m.doc == nil {
		return SubagentEvent{}, false
	}
	blockID = strings.TrimSpace(blockID)
	callID = strings.TrimSpace(callID)
	var events []SubagentEvent
	switch block := m.doc.Find(blockID).(type) {
	case *MainACPTurnBlock:
		events = block.Events
	case *ParticipantTurnBlock:
		events = block.Events
	default:
		return SubagentEvent{}, false
	}
	var owner SubagentEvent
	found := false
	for _, event := range events {
		if event.Kind != SEToolCall || strings.TrimSpace(event.CallID) != callID ||
			names.CanonicalOrSelf(toolSemanticName(event.Name, event.ToolKind)) != names.Spawn {
			continue
		}
		if !found {
			owner = event
			found = true
			continue
		}
		mergeSubagentOutputOwner(&owner, event)
	}
	return owner, found
}

func mergeSubagentOutputOwner(owner *SubagentEvent, update SubagentEvent) {
	if owner == nil {
		return
	}
	if update.Name != "" {
		owner.Name = update.Name
	}
	if update.Args != "" {
		owner.Args = update.Args
	}
	if update.FullArgs != "" {
		owner.FullArgs = update.FullArgs
	}
	if update.Output != "" {
		owner.Output = update.Output
	}
	if update.OutputMessageID != "" {
		owner.OutputMessageID = update.OutputMessageID
	}
	if update.OutputMessage != "" {
		owner.OutputMessage = update.OutputMessage
	}
	if update.TaskHandle != "" {
		owner.TaskHandle = update.TaskHandle
	}
	if update.Activity != "" {
		owner.Activity = update.Activity
	}
	if update.Done {
		owner.Done = true
		owner.Err = update.Err
		owner.EndedAt = update.EndedAt
	}
}
