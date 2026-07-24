package tuiapp

import (
	"strings"

	tea "charm.land/bubbletea/v2"

	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
)

// applyACPRunningActivity derives presentation-only activity from the same
// live Envelope projection used by the transcript. Replay does not call this
// path, and no activity fact becomes durable or model-visible.
func (m *Model) applyACPRunningActivity(
	env eventstream.Envelope,
	events []TranscriptEvent,
) tea.Cmd {
	if m == nil || !m.turnRunning() {
		return nil
	}
	m.applyACPRunningActivityState(env, events)
	return m.resumeRunningAnimationIfNeeded()
}

// applyACPRunningActivityState advances the tracker beneath transient overlays.
// Observed Spawn completion is applied with its presentation owner so the
// transcript block and live activity cannot diverge.
func (m *Model) applyACPRunningActivityState(
	env eventstream.Envelope,
	events []TranscriptEvent,
) {
	if env.Kind == eventstream.KindApprovalReview && env.ApprovalReview != nil {
		key := "approval:" + strings.TrimSpace(env.ApprovalReview.ToolCallID)
		switch strings.ToLower(strings.TrimSpace(env.ApprovalReview.Status)) {
		case "in_progress":
			m.setRunningToolActivity(runningPhaseReview, "", key, env.ApprovalReview.ToolCallID)
			return
		case "approved", "denied", "timed_out", "failed":
			m.completeRunningActivity(key)
			return
		}
	}
	for _, event := range events {
		m.applyTranscriptRunningActivity(event)
	}
}

func (m *Model) applyTranscriptRunningActivity(event TranscriptEvent) {
	if m == nil || event.Scope != ACPProjectionMain {
		return
	}
	switch event.Kind {
	case TranscriptEventNarrative:
		switch event.NarrativeKind {
		case TranscriptNarrativeReasoning:
			m.runningActivityTracker.setFocus(runningPhaseThinking, "", runningNarrativeActivityKey("reasoning", event))
		case TranscriptNarrativeAssistant:
			m.runningActivityTracker.setFocus(runningPhaseResponding, "", runningNarrativeActivityKey("response", event))
		}
		m.refreshRunningActivity()
	case TranscriptEventPlan:
		m.runningActivityTracker.setFocus(runningPhaseThinking, "", "plan")
		m.refreshRunningActivity()
	case TranscriptEventTool:
		m.applyToolRunningActivity(event)
	}
}

func (m *Model) applyToolRunningActivity(event TranscriptEvent) {
	semanticName := names.CanonicalOrSelf(toolSemanticName(event.ToolName, event.ToolKind))
	key := m.runningActivityTracker.toolKey(event.TurnID, event.ToolCallID, event.OccurredAt)
	switch semanticName {
	case names.RunCommand:
		m.updateToolRunningActivity(event.Final, runningPhaseWait, runningTargetShell, key, event.ToolCallID)
	case names.Spawn:
		m.updateToolRunningActivity(event.Final, runningPhaseWait, runningTargetSubagent, key, event.ToolCallID)
	case names.Task:
		action := strings.ToLower(strings.TrimSpace(event.ToolTaskAction))
		target := m.taskControlActivityTarget(event)
		switch action {
		case "wait":
			m.updateToolRunningActivity(event.Final, runningPhaseWait, target, key, event.ToolCallID)
		case "read":
			m.updateToolRunningActivity(event.Final, runningPhaseRead, target, key, event.ToolCallID)
		case "cancel":
			m.updateToolRunningActivity(event.Final, runningPhaseCancel, target, key, event.ToolCallID)
		case "write":
			if target == runningTargetShell {
				m.updateToolRunningActivity(event.Final, runningPhaseWait, target, key, event.ToolCallID)
				return
			}
			m.updateToolRunningActivity(event.Final, runningPhaseThinking, "", key, event.ToolCallID)
		default:
			m.updateToolRunningActivity(event.Final, runningPhaseThinking, "", key, event.ToolCallID)
		}
	default:
		m.updateToolRunningActivity(event.Final, runningPhaseThinking, "", key, event.ToolCallID)
	}
}

func (m *Model) updateToolRunningActivity(
	final bool,
	phase runningActivityPhase,
	target runningActivityTarget,
	key string,
	callID string,
) {
	if key == "" {
		return
	}
	if final {
		m.completeRunningActivity(key)
		return
	}
	m.setRunningToolActivity(phase, target, key, callID)
}

// observeRunningActivityTargets builds a presentation-only owner index from
// projected tool identity. It is populated during live delivery and replay,
// never by scanning rendered transcript blocks.
func (m *Model) observeRunningActivityTargets(events []TranscriptEvent) {
	if m == nil {
		return
	}
	for _, event := range events {
		if event.Kind != TranscriptEventTool || event.Scope != ACPProjectionMain || event.Final {
			continue
		}
		var target runningActivityTarget
		switch names.CanonicalOrSelf(toolSemanticName(event.ToolName, event.ToolKind)) {
		case names.RunCommand:
			target = runningTargetShell
		case names.Spawn:
			target = runningTargetSubagent
		default:
			continue
		}
		owner := runningActivityOwner{
			Key:    m.runningActivityTracker.toolKey(event.TurnID, event.ToolCallID, event.OccurredAt),
			CallID: event.ToolCallID,
			Target: target,
		}
		m.runningActivityTracker.observeOwner("", owner)
		for _, handle := range runningActivityTaskHandles(event.ToolTaskHandle) {
			m.runningActivityTracker.observeOwner(handle, owner)
		}
	}
}

// observeToolPresentationOwner attaches the rendered block identity to the
// same owner index used by the running hint. Durable Task observations can then
// find RunCommand and Spawn owners without rescanning the transcript.
func (m *Model) observeToolPresentationOwner(block *MainACPTurnBlock, event TranscriptEvent) {
	if m == nil || block == nil || event.Kind != TranscriptEventTool ||
		event.Scope != ACPProjectionMain {
		return
	}
	var target runningActivityTarget
	switch names.CanonicalOrSelf(toolSemanticName(event.ToolName, event.ToolKind)) {
	case names.RunCommand:
		target = runningTargetShell
	case names.Spawn:
		target = runningTargetSubagent
	default:
		return
	}
	m.runningActivityTracker.observeOwner(event.ToolTaskHandle, runningActivityOwner{
		Key:     m.runningActivityTracker.toolKey(event.TurnID, event.ToolCallID, event.OccurredAt),
		CallID:  event.ToolCallID,
		Handle:  event.ToolTaskHandle,
		BlockID: block.BlockID(),
		Target:  target,
	})
}

func (m *Model) taskControlActivityTarget(event TranscriptEvent) runningActivityTarget {
	switch strings.ToLower(strings.TrimSpace(event.ToolTaskTargetKind)) {
	case "command", "terminal":
		return runningTargetShell
	case "subagent":
		return runningTargetSubagent
	case "task":
		return runningTargetTask
	}
	switch names.CanonicalOrSelf(toolSemanticName(event.AnchorToolName, "")) {
	case names.RunCommand:
		return runningTargetShell
	case names.Spawn:
		return runningTargetSubagent
	}
	handles := runningActivityTaskHandles(event.ToolTaskHandle)
	if len(handles) == 0 || m == nil {
		return runningTargetTask
	}
	target := m.runningActivityTracker.targetForHandles(handles)
	if target == "" {
		return runningTargetTask
	}
	return target
}

func runningActivityTaskHandles(value string) []string {
	parts := strings.Split(value, ",")
	handles := make([]string, 0, len(parts))
	for _, part := range parts {
		handle := normalizeRunningActivityHandle(part)
		if handle != "" {
			handles = append(handles, handle)
		}
	}
	return handles
}

func runningNarrativeActivityKey(prefix string, event TranscriptEvent) string {
	identity := firstNonEmpty(
		strings.TrimSpace(event.MessageID),
		strings.TrimSpace(event.SourceProjectionID),
		strings.TrimSpace(event.SourceEventID),
	)
	if identity == "" {
		return strings.TrimSpace(prefix)
	}
	return strings.TrimSpace(prefix) + ":" + identity
}
