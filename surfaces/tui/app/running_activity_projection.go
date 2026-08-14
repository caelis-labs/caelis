package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	names "github.com/caelis-labs/caelis/agent-sdk/tool/identity"
	"github.com/caelis-labs/caelis/protocol/acp/eventstream"
	"github.com/caelis-labs/caelis/protocol/acp/schema"
	"github.com/caelis-labs/caelis/surfaces/transcript"
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
		foregroundEvent, ok := foregroundRunningActivityEvent(env, event)
		if !ok {
			continue
		}
		m.applyTranscriptRunningActivity(foregroundEvent)
	}
}

// foregroundRunningActivityEvent keeps typed Task/child observation separate
// from the Agent's foreground action. A ToolCall is a stable invocation start
// and a final ToolCallUpdate is a stable invocation result. Non-final updates
// and observations only repair transcript/output state; they must not create,
// replace, or complete the hint area's foreground activity.
func foregroundRunningActivityEvent(env eventstream.Envelope, event TranscriptEvent) (TranscriptEvent, bool) {
	if event.Kind != TranscriptEventTool {
		return event, true
	}
	if event.Observation {
		return TranscriptEvent{}, false
	}
	switch env.Update.(type) {
	case schema.ToolCall:
		return event, true
	case schema.ToolCallUpdate:
		if !env.Final && !event.Final {
			return TranscriptEvent{}, false
		}
		// Canonical yielded command results complete the tool invocation even
		// while their target process remains running in the Task service.
		event.Final = true
		return event, true
	default:
		return event, true
	}
}

func (m *Model) applyTranscriptRunningActivity(event TranscriptEvent) {
	if m == nil || !isForegroundRunningActivityScope(event.Scope) {
		return
	}
	if isRetryingAttemptReset(event) {
		// attempt_reset clears only speculative model output. Active tool
		// owners represent external work started by an earlier completed
		// model step and must remain observable across the retry.
		m.runningHintTracker.setFocus(runningPhaseRetrying, "", "retry", time.Now())
		m.refreshRunningActivity()
		return
	}
	if event.Kind == TranscriptEventNotice && event.NoticeKind == transcript.NoticeKindModelRetry {
		return
	}
	m.runningHintTracker.clearRetryFocus()
	m.refreshRunningActivity()
	switch event.Kind {
	case TranscriptEventNarrative:
		switch event.NarrativeKind {
		case TranscriptNarrativeReasoning:
			m.runningHintTracker.setFocus(
				runningPhaseThinking,
				"",
				runningNarrativeActivityKey("reasoning", event),
				time.Now(),
			)
		case TranscriptNarrativeAssistant:
			m.runningHintTracker.setFocus(
				runningPhaseResponding,
				"",
				runningNarrativeActivityKey("response", event),
				time.Now(),
			)
		}
		m.refreshRunningActivity()
	case TranscriptEventNotice:
		if event.NoticeKind == transcript.NoticeKindCompact ||
			event.NoticeKind == transcript.NoticeKindCompactFailed {
			m.runningHintTracker.completeCompact(time.Now())
			m.refreshRunningActivity()
		}
	case TranscriptEventPlan:
		m.runningHintTracker.setFocus(runningPhaseThinking, "", "plan", time.Now())
		m.refreshRunningActivity()
	case TranscriptEventTool:
		m.applyToolRunningActivity(event)
	case TranscriptEventLifecycle:
		if strings.EqualFold(strings.TrimSpace(event.State), session.LifecycleStatusContextCompacting) {
			m.runningHintTracker.setCompact(time.Now())
			m.refreshRunningActivity()
		} else {
			m.refreshRunningActivity()
		}
	}
}

func isRetryingAttemptReset(event TranscriptEvent) bool {
	return event.Kind == TranscriptEventLifecycle &&
		strings.EqualFold(strings.TrimSpace(event.State), "attempt_reset") &&
		transcript.MetaBool(event.Meta, "caelis", "runtime", "attempt_reset", "retrying")
}

func isForegroundRunningActivityScope(scope ACPProjectionScope) bool {
	return scope == ACPProjectionMain || scope == ACPProjectionParticipant
}

// applyToolRunningActivity intentionally projects only operations that usually
// remain pending on external work. Short local and unknown tools leave the
// current model phase unchanged; add a named case when a new tool has reliable
// long-running semantics instead of inferring activity from text or a timeout.
func (m *Model) applyToolRunningActivity(event TranscriptEvent) {
	key := m.runningHintTracker.toolKey(event.TurnID, event.ToolCallID, event.OccurredAt)
	if key == "" {
		return
	}
	if event.Final {
		// Standard ACP tool_call_update results may contain only the call ID and
		// status. Invocation identity is sufficient to close an activity that a
		// richer tool_call start opened; repeated tool semantics are not required.
		m.completeRunningActivity(key)
		return
	}

	semanticName := names.CanonicalOrSelf(toolSemanticName(event.ToolName, event.ToolKind))
	switch semanticName {
	case names.WebSearch:
		m.setRunningToolActivity(runningPhaseSearch, "", key, event.ToolCallID)
	case names.WebFetch:
		m.setRunningToolActivity(runningPhaseFetch, "", key, event.ToolCallID)
	case names.RunCommand:
		m.setRunningToolActivity(runningPhaseToolWait, runningTargetShell, key, event.ToolCallID)
	case names.Spawn:
		m.setRunningToolActivity(runningPhaseToolWait, runningTargetSubagent, key, event.ToolCallID)
	case names.Task:
		action := strings.ToLower(strings.TrimSpace(event.ToolTaskAction))
		target := m.taskControlActivityTarget(event)
		switch action {
		case "wait":
			m.setRunningToolActivity(runningPhaseToolWait, target, key, event.ToolCallID)
		case "cancel":
			m.setRunningToolActivity(runningPhaseCancel, target, key, event.ToolCallID)
		}
	}
}

// observeRunningActivityTargets builds a presentation-only owner index from
// projected tool identity. It is populated during live delivery and replay,
// never by scanning rendered transcript blocks.
func (m *Model) observeRunningActivityTargets(events []TranscriptEvent) {
	if m == nil {
		return
	}
	for _, event := range events {
		if event.Kind != TranscriptEventTool || event.Scope != ACPProjectionMain || event.Final || event.Observation {
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
			Key:    m.runningHintTracker.toolKey(event.TurnID, event.ToolCallID, event.OccurredAt),
			CallID: event.ToolCallID,
			Target: target,
		}
		m.runningHintTracker.observeOwner("", owner)
		for _, handle := range runningActivityTaskHandles(event.ToolTaskHandle) {
			m.runningHintTracker.observeOwner(handle, owner)
		}
	}
}

// observeToolPresentationOwner attaches the rendered block identity to the
// same owner index used by the running hint. Durable Task observations can then
// find RunCommand and Spawn owners without rescanning the transcript. Typed
// observations never enter this hint-owned index.
func (m *Model) observeToolPresentationOwner(block *MainACPTurnBlock, event TranscriptEvent) {
	if m == nil || block == nil || event.Kind != TranscriptEventTool ||
		event.Scope != ACPProjectionMain || event.Observation {
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
	m.runningHintTracker.observeOwner(event.ToolTaskHandle, runningActivityOwner{
		Key:     m.runningHintTracker.toolKey(event.TurnID, event.ToolCallID, event.OccurredAt),
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
	target := m.runningHintTracker.targetForHandles(handles)
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
