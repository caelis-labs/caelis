package tuiapp

import (
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/caelis-labs/caelis/agent-sdk/session"
	"github.com/caelis-labs/caelis/control/appserver/eventstream"
	"github.com/caelis-labs/caelis/surfaces/internal/transcript"
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
		_, isUpdate := env.Update.(eventstream.ToolCallUpdate)
		parentCallID := ""
		if env.ParentTool != nil {
			parentCallID = strings.TrimSpace(env.ParentTool.ToolCallID)
		}
		if !isUpdate || parentCallID == "" || parentCallID != strings.TrimSpace(event.ToolCallID) ||
			(!env.Final && !event.Final) {
			return TranscriptEvent{}, false
		}
		// A command Task-stream terminal frame is an observation, but its typed
		// parent relation makes it the lifecycle close for that RunCommand. Child
		// tool observations have a different call ID and remain excluded.
		event.Final = true
		return event, true
	}
	switch env.Update.(type) {
	case eventstream.ToolCall:
		return event, true
	case eventstream.ToolCallUpdate:
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
	applyTranscriptActivity(&m.runningHintTracker, event, m.taskControlActivityTarget)
	m.refreshRunningActivity()
}
func applyTranscriptActivity(tracker *runningHintTracker, event TranscriptEvent, targetFor func(TranscriptEvent) runningActivityTarget) {
	if isRetryingAttemptReset(event) {
		// attempt_reset clears only speculative model output. Active tool
		// owners represent external work started by an earlier completed
		// model step and must remain observable across the retry.
		tracker.setFocus(runningPhaseRetrying, "", "retry", time.Now())
		return
	}
	if event.Kind == TranscriptEventNotice && event.NoticeKind == transcript.NoticeKindModelRetry {
		return
	}
	tracker.clearRetryFocus()
	switch event.Kind {
	case TranscriptEventNarrative:
		switch event.NarrativeKind {
		case TranscriptNarrativeReasoning:
			tracker.setFocus(
				runningPhaseThinking,
				"",
				runningNarrativeActivityKey("reasoning", event),
				time.Now(),
			)
		case TranscriptNarrativeAssistant:
			tracker.setFocus(
				runningPhaseResponding,
				"",
				runningNarrativeActivityKey("response", event),
				time.Now(),
			)
		}
	case TranscriptEventNotice:
		if event.NoticeKind == transcript.NoticeKindCompact ||
			event.NoticeKind == transcript.NoticeKindCompactFailed {
			tracker.completeCompact(time.Now())
		}
	case TranscriptEventPlan:
		tracker.setFocus(runningPhaseThinking, "", "plan", time.Now())
	case TranscriptEventTool:
		applyToolActivity(tracker, event, targetFor)
	case TranscriptEventLifecycle:
		if strings.EqualFold(strings.TrimSpace(event.State), session.LifecycleStatusContextCompacting) {
			tracker.setCompact(time.Now())
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

func applyToolActivity(tracker *runningHintTracker, event TranscriptEvent, targetFor func(TranscriptEvent) runningActivityTarget) {
	start := func(phase runningActivityPhase, target runningActivityTarget, key, callID string) {
		tracker.start(key, phase, target, time.Now(), callID)
	}
	key := tracker.toolKey(event.TurnID, event.ToolCallID, event.OccurredAt)
	if key == "" {
		return
	}
	if event.Final {
		// Standard ACP tool_call_update results may contain only the call ID and
		// status. Invocation identity is sufficient to close an activity that a
		// richer tool_call start opened. Task-stream finals have a distinct
		// runtime TurnID, so also close the indexed parent owner by tool-call ID.
		tracker.completeTool(key, event.ToolCallID, time.Now())
		return
	}

	semanticName := event.ToolName
	switch semanticName {
	case surfaceToolWebSearch:
		start(runningPhaseWebSearch, "", key, event.ToolCallID)
	case surfaceToolWebFetch:
		start(runningPhaseFetch, "", key, event.ToolCallID)
	case surfaceToolRunCommand:
		start(runningPhaseToolWait, runningTargetShell, key, event.ToolCallID)
	case surfaceToolSpawn:
		start(runningPhaseToolWait, runningTargetSubagent, key, event.ToolCallID)
	case "WaitThread":
		start(runningPhaseToolWait, runningTargetSubagent, key, event.ToolCallID)
	case "ReadThread", "ListThreads", "ReadMessages", "ReceiveMessages":
		// Observation has no long-running activity hint.
	case surfaceToolTask:
		action := strings.ToLower(strings.TrimSpace(event.ToolTaskAction))
		target := targetFor(event)
		switch action {
		case "wait":
			start(runningPhaseToolWait, target, key, event.ToolCallID)
		case "cancel":
			start(runningPhaseCancel, target, key, event.ToolCallID)
		}
	default:
		// Standard ACP kind is the primary presentation category for anonymous
		// provider tools. Terminal metadata supplements only an otherwise generic
		// category; it must not turn a read/edit/think operation into a shell wait.
		switch strings.ToLower(strings.TrimSpace(event.ToolKind)) {
		case eventstream.ToolKindExecute:
			start(runningPhaseToolWait, runningTargetShell, key, event.ToolCallID)
		case eventstream.ToolKindSearch:
			start(runningPhaseSearch, "", key, event.ToolCallID)
		case eventstream.ToolKindFetch:
			start(runningPhaseFetch, "", key, event.ToolCallID)
		case eventstream.ToolKindOther, "":
			if standardACPWaitControl(event) {
				start(runningPhaseToolWait, runningTargetSubagent, key, event.ToolCallID)
			} else if event.ToolTerminal {
				start(runningPhaseToolWait, runningTargetShell, key, event.ToolCallID)
			}
		}
	}
}

// standardACPWaitControl recognizes the narrow standard-ACP shape emitted for
// provider collaboration waits. ACP has no dedicated wait kind, so kind=other
// remains authoritative and title/input only refine this presentation choice.
func standardACPWaitControl(event TranscriptEvent) bool {
	return strings.TrimSpace(event.ToolName) == "" &&
		strings.EqualFold(strings.TrimSpace(event.ToolKind), eventstream.ToolKindOther) &&
		strings.EqualFold(strings.TrimSpace(event.ToolTitle), "wait") &&
		strings.EqualFold(strings.TrimSpace(event.ToolTaskAction), "wait") &&
		strings.EqualFold(strings.TrimSpace(event.ToolTaskTargetKind), "subagent")
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
		switch event.ToolName {
		case surfaceToolRunCommand:
			target = runningTargetShell
		case surfaceToolSpawn:
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
	switch event.ToolName {
	case surfaceToolRunCommand:
		target = runningTargetShell
	case surfaceToolSpawn:
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
	switch event.AnchorToolName {
	case surfaceToolRunCommand:
		return runningTargetShell
	case surfaceToolSpawn:
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
